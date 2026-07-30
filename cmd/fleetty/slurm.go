package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"charm.land/log/v2"
	"github.com/charmbracelet/x/ansi"
	gossh "golang.org/x/crypto/ssh"
)

const (
	defaultSlurmRefreshInterval = 2 * time.Second
	defaultSlurmTimeout         = 4 * time.Second
	maxSlurmOutput              = 2 << 20
)

type slurmClusterConfig struct {
	Name                string   `json:"name"`
	Description         string   `json:"description,omitempty"`
	Transport           string   `json:"transport,omitempty"`
	Address             string   `json:"address,omitempty"`
	User                string   `json:"user,omitempty"`
	IdentityFile        string   `json:"identity_file,omitempty"`
	HostKey             string   `json:"host_key,omitempty"`
	HostKeys            []string `json:"host_keys,omitempty"`
	Partitions          []string `json:"partitions,omitempty"`
	RefreshSeconds      int      `json:"refresh_seconds,omitempty"`
	TimeoutSeconds      int      `json:"timeout_seconds,omitempty"`
	InsecureSkipHostKey bool     `json:"-"`
}

func (c slurmClusterConfig) refreshInterval() time.Duration {
	if c.RefreshSeconds < 1 {
		return defaultSlurmRefreshInterval
	}
	return time.Duration(min(c.RefreshSeconds, 60)) * time.Second
}

func (c slurmClusterConfig) timeout() time.Duration {
	if c.TimeoutSeconds < 1 {
		return defaultSlurmTimeout
	}
	return time.Duration(min(c.TimeoutSeconds, 30)) * time.Second
}

type slurmPartition struct {
	Name      string
	Default   bool
	Available string
	TimeLimit string
	Nodes     int
	States    []string
	CPUsAlloc int
	CPUsIdle  int
	CPUsOther int
	CPUsTotal int
	GRES      []string
}

type slurmJob struct {
	ID             string
	Partition      string
	Name           string
	User           string
	State          string
	Priority       uint64
	QOS            string
	Elapsed        string
	TimeLimit      string
	Nodes          int
	NodeList       string
	Reason         string
	GRES           string
	RequestedNodes string
	ExpectedNodes  string
	ExcludedNodes  string
	CPUs           int
	MemoryBytes    uint64
	MemoryRaw      string
	Constraints    string
}

type slurmNode struct {
	Name             string
	Partitions       []string
	GRES             []string
	State            string
	CPUsAlloc        int
	CPUsIdle         int
	CPUsOther        int
	CPUsTotal        int
	MemoryTotalBytes uint64
	MemoryFreeBytes  uint64
}

type slurmSnapshot struct {
	Name        string
	Description string
	CollectedAt time.Time
	Version     string
	Partitions  []slurmPartition
	Nodes       []slurmNode
	Jobs        []slurmJob
}

type slurmClusterState struct {
	Snapshot            slurmSnapshot
	Error               string
	Warning             string
	Latency             time.Duration
	Checked             time.Time
	LastSeen            time.Time
	NextRetry           time.Time
	ConsecutiveFailures int
}

func collectSlurmStatesWithPrevious(configs []slurmClusterConfig, previous []slurmClusterState, now time.Time) []slurmClusterState {
	return collectSlurmStates(configs, previous, now, nil)
}

func collectSlurmStates(
	configs []slurmClusterConfig,
	previous []slurmClusterState,
	now time.Time,
	runners []slurmCommandRunner,
) []slurmClusterState {
	states := make([]slurmClusterState, len(configs))
	var done = make(chan struct{}, len(configs))
	for index := range configs {
		if index < len(previous) {
			states[index] = previous[index]
			if states[index].ConsecutiveFailures > 0 && now.Before(states[index].NextRetry) {
				done <- struct{}{}
				continue
			}
			if states[index].Error == "" && !states[index].Checked.IsZero() &&
				now.Sub(states[index].Checked) < configs[index].refreshInterval() {
				done <- struct{}{}
				continue
			}
		}
		go func(index int) {
			started := time.Now()
			var snapshot slurmSnapshot
			var err error
			if runners == nil {
				snapshot, err = collectSlurmSnapshot(configs[index])
			} else {
				snapshot, err = collectSlurmSnapshotPersistent(configs[index], runners, index)
			}
			checked := time.Now()
			state := states[index]
			state.Latency = time.Since(started)
			state.Checked = checked
			if err != nil {
				failure := sanitizeTerminalText(err.Error())
				state.ConsecutiveFailures++
				state.NextRetry = checked.Add(hubOfflineRetryDelay(state.ConsecutiveFailures))
				state.Warning = failure
				if state.Snapshot.CollectedAt.IsZero() || state.ConsecutiveFailures >= 2 {
					state.Error = failure
				}
				log.Warn("Slurm collection failed", "cluster", configs[index].Name,
					"transport", configs[index].Transport, "error", err)
			} else {
				state.Snapshot = snapshot
				state.Error = ""
				state.Warning = ""
				state.LastSeen = checked
				state.NextRetry = time.Time{}
				state.ConsecutiveFailures = 0
			}
			states[index] = state
			done <- struct{}{}
		}(index)
	}
	for range configs {
		<-done
	}
	return states
}

func collectSlurmSnapshotPersistent(
	config slurmClusterConfig,
	runners []slurmCommandRunner,
	index int,
) (slurmSnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), config.timeout())
	defer cancel()
	runner := runners[index]
	if runner == nil {
		var err error
		runner, err = newSlurmRunner(ctx, config)
		if err != nil {
			return slurmSnapshot{}, err
		}
		runners[index] = runner
	}
	snapshot, err := collectSlurmSnapshotWithRunner(ctx, config, runner)
	if err != nil {
		_ = runner.Close()
		runners[index] = nil
	}
	return snapshot, err
}

type slurmCommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
	Close() error
}

type localSlurmRunner struct{}

func (localSlurmRunner) Run(ctx context.Context, command string, args ...string) ([]byte, error) {
	process := exec.CommandContext(ctx, command, args...)
	process.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	output, err := process.CombinedOutput()
	if len(output) > maxSlurmOutput {
		return nil, errors.New("Slurm output exceeds 2 MiB")
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w%s", command, err, compactOutput(string(output)))
	}
	return output, nil
}

func (localSlurmRunner) Close() error { return nil }

type sshSlurmRunner struct {
	name   string
	client *gossh.Client
}

func (r *sshSlurmRunner) Run(ctx context.Context, command string, args ...string) ([]byte, error) {
	session, err := r.client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("open SSH session: %w", err)
	}
	defer session.Close()
	remoteCommand := "env LC_ALL=C LANG=C " + shellJoin(append([]string{command}, args...))
	type result struct {
		output []byte
		err    error
	}
	finished := make(chan result, 1)
	go func() {
		output, runErr := session.CombinedOutput(remoteCommand)
		finished <- result{output: output, err: runErr}
	}()
	select {
	case <-ctx.Done():
		_ = session.Signal(gossh.SIGKILL)
		return nil, ctx.Err()
	case result := <-finished:
		if len(result.output) > maxSlurmOutput {
			return nil, errors.New("Slurm output exceeds 2 MiB")
		}
		if result.err != nil {
			return nil, fmt.Errorf("%s on %s: %w%s", command, r.name, result.err, compactOutput(string(result.output)))
		}
		return result.output, nil
	}
}

func (r *sshSlurmRunner) Close() error { return r.client.Close() }

func newSlurmRunner(ctx context.Context, config slurmClusterConfig) (slurmCommandRunner, error) {
	if config.Transport == "local" {
		return localSlurmRunner{}, nil
	}
	signer, err := loadPrivateKeySigner(config.IdentityFile)
	if err != nil {
		return nil, fmt.Errorf("load identity file: %w", err)
	}
	hostKeyCallback, err := fixedHostKeysCallback(config.Name, config.HostKeys, config.InsecureSkipHostKey)
	if err != nil {
		return nil, err
	}
	address := normalizeSSHAddress(config.Address)
	clientConfig := &gossh.ClientConfig{
		User:            config.User,
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: hostKeyCallback,
		Timeout:         config.timeout(),
	}
	dialer := net.Dialer{Timeout: config.timeout()}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", config.Name, err)
	}
	clientConnection, channels, requests, err := gossh.NewClientConn(connection, address, clientConfig)
	if err != nil {
		connection.Close()
		return nil, fmt.Errorf("SSH handshake %s: %w", config.Name, err)
	}
	return &sshSlurmRunner{name: config.Name, client: gossh.NewClient(clientConnection, channels, requests)}, nil
}

func collectSlurmSnapshot(config slurmClusterConfig) (slurmSnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), config.timeout())
	defer cancel()
	runner, err := newSlurmRunner(ctx, config)
	if err != nil {
		return slurmSnapshot{}, err
	}
	defer runner.Close()
	return collectSlurmSnapshotWithRunner(ctx, config, runner)
}

func collectSlurmSnapshotWithRunner(
	ctx context.Context,
	config slurmClusterConfig,
	runner slurmCommandRunner,
) (slurmSnapshot, error) {
	versionOutput, err := runner.Run(ctx, "sinfo", "--version")
	if err != nil {
		return slurmSnapshot{}, err
	}
	partitionOutput, err := runner.Run(ctx, "sinfo", "-h", "-o", "%P\t%a\t%l\t%D\t%t\t%C\t%G")
	if err != nil {
		return slurmSnapshot{}, err
	}
	nodeOutput, err := runner.Run(
		ctx, "sinfo", "--noconvert", "-N", "-h", "-o",
		"%N\t%P\t%G\t%t\t%C\t%m\t%e",
	)
	if err != nil {
		return slurmSnapshot{}, err
	}
	jobOutput, err := runner.Run(
		ctx, "squeue", "--noconvert", "-h", "-o",
		"%i\t%P\t%j\t%u\t%T\t%Q\t%q\t%M\t%l\t%D\t%N\t%b\t%n\t%Y\t%x\t%C\t%m\t%f\t%R",
	)
	if err != nil {
		return slurmSnapshot{}, err
	}
	partitions, err := parseSlurmPartitions(string(partitionOutput), config.Partitions)
	if err != nil {
		return slurmSnapshot{}, err
	}
	nodes, err := parseSlurmNodes(string(nodeOutput), config.Partitions)
	if err != nil {
		return slurmSnapshot{}, err
	}
	jobs, err := parseSlurmJobs(string(jobOutput), config.Partitions)
	if err != nil {
		return slurmSnapshot{}, err
	}
	return slurmSnapshot{
		Name:        config.Name,
		Description: config.Description,
		CollectedAt: time.Now(),
		Version:     strings.TrimSpace(string(versionOutput)),
		Partitions:  partitions,
		Nodes:       nodes,
		Jobs:        jobs,
	}, nil
}

func parseSlurmPartitions(output string, filter []string) ([]slurmPartition, error) {
	allowed := stringSet(filter)
	byName := make(map[string]*slurmPartition)
	var order []string
	for lineNumber, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 7 {
			return nil, fmt.Errorf("parse sinfo line %d: expected 7 fields", lineNumber+1)
		}
		rawName := strings.TrimSpace(fields[0])
		isDefault := strings.HasSuffix(rawName, "*")
		name := strings.TrimSuffix(rawName, "*")
		if len(allowed) > 0 {
			if _, ok := allowed[name]; !ok {
				continue
			}
		}
		nodes, err := strconv.Atoi(strings.TrimSpace(fields[3]))
		if err != nil {
			return nil, fmt.Errorf("parse sinfo nodes for %s: %w", name, err)
		}
		cpus, err := parseSlurmCPUCounts(fields[5])
		if err != nil {
			return nil, fmt.Errorf("parse sinfo CPUs for %s: %w", name, err)
		}
		partition, exists := byName[name]
		if !exists {
			partition = &slurmPartition{
				Name: name, Default: isDefault, Available: strings.TrimSpace(fields[1]),
				TimeLimit: strings.TrimSpace(fields[2]),
			}
			byName[name] = partition
			order = append(order, name)
		}
		partition.Default = partition.Default || isDefault
		partition.Nodes += nodes
		partition.CPUsAlloc += cpus[0]
		partition.CPUsIdle += cpus[1]
		partition.CPUsOther += cpus[2]
		partition.CPUsTotal += cpus[3]
		partition.States = appendUnique(partition.States, strings.TrimSpace(fields[4]))
		if gres := strings.TrimSpace(fields[6]); gres != "" && gres != "(null)" {
			partition.GRES = appendUnique(partition.GRES, gres)
		}
	}
	partitions := make([]slurmPartition, 0, len(order))
	for _, name := range order {
		partitions = append(partitions, *byName[name])
	}
	return partitions, nil
}

func parseSlurmCPUCounts(value string) ([4]int, error) {
	var result [4]int
	fields := strings.Split(strings.TrimSpace(value), "/")
	if len(fields) != 4 {
		return result, errors.New("expected allocated/idle/other/total")
	}
	for index, field := range fields {
		number, err := strconv.Atoi(field)
		if err != nil {
			return result, err
		}
		result[index] = number
	}
	return result, nil
}

func parseSlurmNodes(output string, filter []string) ([]slurmNode, error) {
	allowed := stringSet(filter)
	byName := make(map[string]*slurmNode)
	var order []string
	for lineNumber, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 && len(fields) != 7 {
			return nil, fmt.Errorf("parse sinfo node line %d: expected 3 or 7 fields", lineNumber+1)
		}
		name := strings.TrimSpace(fields[0])
		partition := strings.TrimSuffix(strings.TrimSpace(fields[1]), "*")
		if len(allowed) > 0 {
			if _, ok := allowed[partition]; !ok {
				continue
			}
		}
		node, exists := byName[name]
		if !exists {
			node = &slurmNode{Name: name}
			byName[name] = node
			order = append(order, name)
		}
		node.Partitions = appendUnique(node.Partitions, partition)
		if gres := strings.TrimSpace(fields[2]); gres != "" && gres != "(null)" {
			node.GRES = appendUnique(node.GRES, gres)
		}
		if len(fields) == 7 {
			cpus, err := parseSlurmCPUCounts(fields[4])
			if err != nil {
				return nil, fmt.Errorf("parse sinfo node CPUs for %s: %w", name, err)
			}
			totalMemory, err := parseSlurmMemory(fields[5], 1)
			if err != nil {
				return nil, fmt.Errorf("parse sinfo node memory for %s: %w", name, err)
			}
			freeMemory, err := parseSlurmMemory(fields[6], 1)
			if err != nil {
				return nil, fmt.Errorf("parse sinfo node free memory for %s: %w", name, err)
			}
			node.State = strings.TrimSpace(fields[3])
			node.CPUsAlloc, node.CPUsIdle = cpus[0], cpus[1]
			node.CPUsOther, node.CPUsTotal = cpus[2], cpus[3]
			node.MemoryTotalBytes, node.MemoryFreeBytes = totalMemory, freeMemory
		}
	}
	nodes := make([]slurmNode, 0, len(order))
	for _, name := range order {
		nodes = append(nodes, *byName[name])
	}
	return nodes, nil
}

func parseSlurmJobs(output string, filter []string) ([]slurmJob, error) {
	allowed := stringSet(filter)
	var jobs []slurmJob
	for lineNumber, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 16 && len(fields) != 19 {
			return nil, fmt.Errorf("parse squeue line %d: expected 16 or 19 fields", lineNumber+1)
		}
		partition := strings.TrimSpace(fields[1])
		if len(allowed) > 0 {
			if _, ok := allowed[partition]; !ok {
				continue
			}
		}
		priority, err := strconv.ParseUint(strings.TrimSpace(fields[5]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse squeue priority on line %d: %w", lineNumber+1, err)
		}
		nodes, err := strconv.Atoi(strings.TrimSpace(fields[9]))
		if err != nil {
			return nil, fmt.Errorf("parse squeue nodes on line %d: %w", lineNumber+1, err)
		}
		job := slurmJob{
			ID: strings.TrimSpace(fields[0]), Partition: partition,
			Name: strings.TrimSpace(fields[2]), User: strings.TrimSpace(fields[3]),
			State: strings.TrimSpace(fields[4]), Priority: priority,
			QOS: strings.TrimSpace(fields[6]), Elapsed: strings.TrimSpace(fields[7]),
			TimeLimit: strings.TrimSpace(fields[8]), Nodes: nodes,
			NodeList:       strings.TrimSpace(fields[10]),
			GRES:           strings.TrimSpace(fields[11]),
			RequestedNodes: strings.TrimSpace(fields[12]),
			ExpectedNodes:  strings.TrimSpace(fields[13]),
			ExcludedNodes:  strings.TrimSpace(fields[14]),
			Reason:         strings.TrimSpace(fields[15]),
		}
		if len(fields) == 19 {
			cpus, err := strconv.Atoi(strings.TrimSpace(fields[15]))
			if err != nil {
				return nil, fmt.Errorf("parse squeue CPUs on line %d: %w", lineNumber+1, err)
			}
			memoryRaw := strings.TrimSpace(fields[16])
			memory, err := parseSlurmMemory(memoryRaw, cpus)
			if err != nil {
				return nil, fmt.Errorf("parse squeue memory on line %d: %w", lineNumber+1, err)
			}
			job.CPUs = cpus
			job.MemoryRaw = memoryRaw
			job.MemoryBytes = memory
			job.Constraints = strings.TrimSpace(fields[17])
			job.Reason = strings.TrimSpace(fields[18])
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func parseSlurmMemory(value string, cpus int) (uint64, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "0" || value == "N/A" || value == "(null)" {
		return 0, nil
	}
	perCPU := strings.HasSuffix(strings.ToLower(value), "c")
	if perCPU {
		value = value[:len(value)-1]
	}
	// Slurm reports unsuffixed memory values in MiB.
	multiplier := uint64(1 << 20)
	switch suffix := strings.ToUpper(value[len(value)-1:]); suffix {
	case "K":
		multiplier = 1 << 10
		value = value[:len(value)-1]
	case "M":
		multiplier = 1 << 20
		value = value[:len(value)-1]
	case "G":
		multiplier = 1 << 30
		value = value[:len(value)-1]
	case "T":
		multiplier = 1 << 40
		value = value[:len(value)-1]
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || number < 0 {
		if err == nil {
			err = errors.New("negative value")
		}
		return 0, err
	}
	bytes := uint64(number * float64(multiplier))
	if perCPU {
		bytes *= uint64(max(1, cpus))
	}
	return bytes, nil
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result[value] = struct{}{}
		}
	}
	return result
}

func appendUnique(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func normalizeSSHAddress(address string) string {
	address = strings.TrimSpace(address)
	if _, _, err := net.SplitHostPort(address); err == nil {
		return address
	}
	return net.JoinHostPort(address, "22")
}

func shellJoin(arguments []string) string {
	quoted := make([]string, len(arguments))
	for index, argument := range arguments {
		quoted[index] = "'" + strings.ReplaceAll(argument, "'", "'\"'\"'") + "'"
	}
	return strings.Join(quoted, " ")
}

func slurmStateCounts(jobs []slurmJob) (running, pending, other int) {
	for _, job := range jobs {
		switch strings.ToUpper(job.State) {
		case "RUNNING", "COMPLETING":
			running++
		case "PENDING", "CONFIGURING":
			pending++
		default:
			other++
		}
	}
	return
}

func slurmJobStateStyle(state string) lipgloss.Style {
	switch strings.ToUpper(state) {
	case "RUNNING", "COMPLETING":
		return processRunningStyle
	case "PENDING", "CONFIGURING":
		return processWaitingStyle
	case "SUSPENDED", "STOPPED":
		return processStoppedStyle
	case "FAILED", "CANCELLED", "TIMEOUT", "NODE_FAIL", "OUT_OF_MEMORY":
		return dangerStyle
	default:
		return processDefaultStyle
	}
}

func sortSlurmJobs(jobs []slurmDisplayJob) {
	rank := func(job slurmDisplayJob) int {
		switch {
		case isSlurmRunning(job.Job.State):
			return 0
		case job.Next:
			return 1
		case isSlurmPending(job.Job.State):
			return 2
		default:
			return 3
		}
	}
	sort.SliceStable(jobs, func(i, j int) bool {
		left, right := rank(jobs[i]), rank(jobs[j])
		if left != right {
			return left < right
		}
		if jobs[i].Cluster != jobs[j].Cluster {
			return jobs[i].Cluster < jobs[j].Cluster
		}
		return jobs[i].Order < jobs[j].Order
	})
}

func isSlurmRunning(state string) bool {
	switch strings.ToUpper(state) {
	case "RUNNING", "COMPLETING":
		return true
	default:
		return false
	}
}

func isSlurmPending(state string) bool {
	switch strings.ToUpper(state) {
	case "PENDING", "CONFIGURING":
		return true
	default:
		return false
	}
}

func slurmEligibleForNext(job slurmJob) bool {
	if !isSlurmPending(job.State) {
		return false
	}
	switch normalizeSlurmReason(job.Reason) {
	case "JobArrayTaskLimit", "Dependency", "DependencyNeverSatisfied":
		return false
	default:
		return true
	}
}

type slurmDisplayJob struct {
	Cluster  string
	Job      slurmJob
	Next     bool
	Order    int
	Snapshot *slurmSnapshot
}

type nodeSlurmQueue struct {
	Cluster     string
	Node        string
	Jobs        []slurmDisplayJob
	CollectedAt time.Time
	Warning     string
}

func (m *hubModel) nodeSlurmQueue(nodeIndex int) *nodeSlurmQueue {
	if nodeIndex < 0 || nodeIndex >= len(m.config.Nodes) {
		return nil
	}
	nodeConfig := m.config.Nodes[nodeIndex]
	if nodeConfig.SlurmCluster == "" || nodeConfig.SlurmNode == "" {
		return nil
	}
	queue := &nodeSlurmQueue{Cluster: nodeConfig.SlurmCluster, Node: nodeConfig.SlurmNode}
	clusterIndex := -1
	for index, cluster := range m.config.SlurmClusters {
		if cluster.Name == nodeConfig.SlurmCluster {
			clusterIndex = index
			break
		}
	}
	if clusterIndex < 0 || clusterIndex >= len(m.slurmStates) {
		queue.Warning = "Waiting for the first Slurm snapshot…"
		return queue
	}
	state := m.slurmStates[clusterIndex]
	queueSnapshot := state.Snapshot
	queue.CollectedAt = state.Snapshot.CollectedAt
	if state.Error != "" {
		queue.Warning = "Slurm source offline: " + state.Error
		return queue
	}
	if state.Warning != "" {
		queue.Warning = "Stale queue: " + state.Warning
	}

	partitions := make(map[string]struct{})
	for _, node := range state.Snapshot.Nodes {
		if node.Name == nodeConfig.SlurmNode {
			for _, partition := range node.Partitions {
				partitions[partition] = struct{}{}
			}
			break
		}
	}
	nextMarked := false
	for order, job := range state.Snapshot.Jobs {
		if isSlurmRunning(job.State) {
			if slurmNodeListContains(job.NodeList, nodeConfig.SlurmNode) {
				queue.Jobs = append(queue.Jobs, slurmDisplayJob{
					Cluster: state.Snapshot.Name, Job: job, Order: order, Snapshot: &queueSnapshot,
				})
			}
			continue
		}
		if !isSlurmPending(job.State) {
			continue
		}
		if _, eligible := partitions[job.Partition]; !eligible {
			continue
		}
		if !slurmPendingJobMatchesNode(job, nodeConfig.SlurmNode, state.Snapshot.Nodes) {
			continue
		}
		next := !nextMarked && slurmEligibleForNext(job)
		nextMarked = nextMarked || next
		queue.Jobs = append(queue.Jobs, slurmDisplayJob{
			Cluster: state.Snapshot.Name, Job: job, Next: next, Order: order, Snapshot: &queueSnapshot,
		})
	}
	sortSlurmJobs(queue.Jobs)
	return queue
}

func slurmPendingJobMatchesNode(job slurmJob, nodeName string, nodes []slurmNode) bool {
	if slurmNodeExpressionPresent(job.NodeList) {
		return slurmNodeListContains(job.NodeList, nodeName)
	}
	if slurmNodeExpressionPresent(job.ExpectedNodes) {
		return slurmNodeListContains(job.ExpectedNodes, nodeName)
	}
	if slurmNodeExpressionPresent(job.RequestedNodes) &&
		!slurmNodeListContains(job.RequestedNodes, nodeName) {
		return false
	}
	if slurmNodeExpressionPresent(job.ExcludedNodes) &&
		slurmNodeListContains(job.ExcludedNodes, nodeName) {
		return false
	}
	requirements := slurmGPUCounts(job.GRES)
	if len(requirements) == 0 {
		return true
	}
	for _, node := range nodes {
		if node.Name != nodeName {
			continue
		}
		available := slurmGPUCounts(strings.Join(node.GRES, ","))
		for gpuType, count := range requirements {
			if gpuType == "" {
				var total int
				for _, availableCount := range available {
					total += availableCount
				}
				if total < count {
					return false
				}
				continue
			}
			if available[gpuType] < count {
				return false
			}
		}
		return true
	}
	return false
}

func slurmNodeExpressionPresent(value string) bool {
	switch strings.TrimSpace(value) {
	case "", "(null)", "N/A", "None":
		return false
	default:
		return true
	}
}

func slurmGPUCounts(value string) map[string]int {
	result := make(map[string]int)
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(strings.TrimPrefix(item, "gres/"))
		if item == "" || item == "(null)" || item == "N/A" {
			continue
		}
		if suffix := strings.IndexByte(item, '('); suffix >= 0 {
			item = item[:suffix]
		}
		parts := strings.Split(item, ":")
		if len(parts) < 2 || parts[0] != "gpu" {
			continue
		}
		gpuType, countField := "", parts[len(parts)-1]
		if len(parts) >= 3 {
			gpuType = parts[1]
		}
		countText := strings.TrimLeftFunc(countField, func(r rune) bool {
			return r < '0' || r > '9'
		})
		end := 0
		for end < len(countText) && countText[end] >= '0' && countText[end] <= '9' {
			end++
		}
		if end == 0 {
			continue
		}
		count, err := strconv.Atoi(countText[:end])
		if err == nil {
			result[gpuType] += count
		}
	}
	return result
}

type slurmNodeFit struct {
	Node            string
	CPURequest      int
	CPUAvailable    int
	MemoryRequest   uint64
	MemoryAvailable uint64
	GPURequest      string
	GPUAvailable    string
	Blockers        []string
}

type slurmJobExplanation struct {
	ReasonCode string
	Badge      string
	Source     string
	Summary    string
	Fits       []slurmNodeFit
}

func explainSlurmJob(job slurmJob, snapshot *slurmSnapshot) slurmJobExplanation {
	reason := normalizeSlurmReason(job.Reason)
	explanation := slurmJobExplanation{
		ReasonCode: reason,
		Badge:      slurmReasonBadge(reason),
		Source:     "SLURM",
		Summary:    slurmReasonSummary(reason),
	}
	if isSlurmRunning(job.State) {
		explanation.Badge = "RUNNING"
		explanation.Summary = "This job is running; Slurm reports no pending blocker."
		return explanation
	}
	if reason == "JobArrayTaskLimit" {
		if limit := slurmArrayThrottle(job.ID); limit > 0 {
			explanation.Summary = fmt.Sprintf(
				"The job array allows at most %d task%s to run concurrently.",
				limit, plural(limit),
			)
			if snapshot != nil {
				running := slurmRunningArraySiblings(job.ID, snapshot.Jobs)
				explanation.Summary += fmt.Sprintf(" %d sibling task%s currently running.", running, plural(running))
			}
		}
		return explanation
	}
	if reason != "Resources" || snapshot == nil {
		return explanation
	}
	explanation.Fits = slurmCandidateNodeFits(job, *snapshot)
	explanation.Source = "SLURM + FLEETTY INFERENCE"
	if len(explanation.Fits) == 0 {
		explanation.Badge = "NO CANDIDATE"
		explanation.Summary = "No configured node matches the partition, node selection, and GPU request."
		return explanation
	}
	best := explanation.Fits[0]
	for _, fit := range explanation.Fits[1:] {
		if len(fit.Blockers) < len(best.Blockers) {
			best = fit
		}
	}
	if len(best.Blockers) == 0 {
		explanation.Badge = "RESOURCES"
		explanation.Summary = "The visible node snapshot fits this request; Slurm may be waiting on scheduling state not exposed here."
		return explanation
	}
	explanation.Badge = strings.Join(best.Blockers, "+")
	explanation.Summary = fmt.Sprintf(
		"Best candidate %s is currently short on %s.",
		best.Node, strings.ToLower(strings.Join(best.Blockers, " and ")),
	)
	return explanation
}

func slurmCandidateNodeFits(job slurmJob, snapshot slurmSnapshot) []slurmNodeFit {
	var result []slurmNodeFit
	for _, node := range snapshot.Nodes {
		if !slurmNodeInPartition(node, job.Partition) ||
			!slurmPendingJobMatchesNode(job, node.Name, snapshot.Nodes) {
			continue
		}
		fit := slurmNodeFit{
			Node:            node.Name,
			CPURequest:      slurmPerNodeCPUs(job),
			CPUAvailable:    node.CPUsIdle,
			MemoryRequest:   job.MemoryBytes,
			MemoryAvailable: slurmNodeMemoryAvailable(node, snapshot.Jobs),
		}
		if !slurmNodeSchedulingState(node.State) {
			fit.Blockers = append(fit.Blockers, "STATE")
		}
		if fit.CPURequest > 0 && fit.CPUAvailable < fit.CPURequest {
			fit.Blockers = append(fit.Blockers, "CPU")
		}
		if fit.MemoryRequest > 0 && node.MemoryTotalBytes > 0 &&
			fit.MemoryAvailable < fit.MemoryRequest {
			fit.Blockers = append(fit.Blockers, "MEM")
		}
		fit.GPURequest, fit.GPUAvailable, fit.Blockers =
			slurmNodeGPUFit(job, node, snapshot.Jobs, fit.Blockers)
		result = append(result, fit)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if len(result[i].Blockers) != len(result[j].Blockers) {
			return len(result[i].Blockers) < len(result[j].Blockers)
		}
		return result[i].Node < result[j].Node
	})
	return result
}

func slurmNodeInPartition(node slurmNode, partition string) bool {
	for _, value := range node.Partitions {
		if value == partition {
			return true
		}
	}
	return false
}

func slurmNodeSchedulingState(state string) bool {
	state = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(state), "*"))
	return state == "" || strings.HasPrefix(state, "idle") ||
		strings.HasPrefix(state, "mix") || strings.HasPrefix(state, "alloc")
}

func slurmPerNodeCPUs(job slurmJob) int {
	return (job.CPUs + max(1, job.Nodes) - 1) / max(1, job.Nodes)
}

func slurmNodeMemoryAvailable(node slurmNode, jobs []slurmJob) uint64 {
	if node.MemoryTotalBytes == 0 {
		return 0
	}
	var allocated uint64
	for _, running := range jobs {
		if isSlurmRunning(running.State) && slurmNodeListContains(running.NodeList, node.Name) {
			allocated += running.MemoryBytes
		}
	}
	if allocated >= node.MemoryTotalBytes {
		return 0
	}
	return node.MemoryTotalBytes - allocated
}

func slurmNodeGPUFit(
	job slurmJob,
	node slurmNode,
	jobs []slurmJob,
	blockers []string,
) (string, string, []string) {
	requested := slurmGPUCounts(job.GRES)
	total := slurmGPUCounts(strings.Join(node.GRES, ","))
	if len(requested) == 0 {
		return "—", slurmGPUCountLabel(total), blockers
	}
	used := make(map[string]int)
	for _, running := range jobs {
		if !isSlurmRunning(running.State) || !slurmNodeListContains(running.NodeList, node.Name) {
			continue
		}
		for gpuType, count := range slurmGPUCounts(running.GRES) {
			used[gpuType] += count
		}
	}
	fits := true
	for gpuType, count := range requested {
		if gpuType == "" {
			if slurmGPUTotal(total)-slurmGPUTotal(used) < count {
				fits = false
			}
			continue
		}
		available := total[gpuType] - used[gpuType] - used[""]
		if available < count {
			fits = false
		}
	}
	if !fits {
		blockers = append(blockers, "GPU")
	}
	return slurmGPUCountLabel(requested), slurmGPUAvailableLabel(total, used), blockers
}

func slurmGPUTotal(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

func slurmGPUCountLabel(counts map[string]int) string {
	if len(counts) == 0 {
		return "—"
	}
	var names []string
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	var labels []string
	for _, name := range names {
		label := name
		if label == "" {
			label = "gpu"
		}
		labels = append(labels, fmt.Sprintf("%s×%d", label, counts[name]))
	}
	return strings.Join(labels, ",")
}

func slurmGPUAvailableLabel(total, used map[string]int) string {
	if len(total) == 0 {
		return "—"
	}
	available := make(map[string]int, len(total))
	for name, count := range total {
		available[name] = max(0, count-used[name]-used[""])
	}
	return slurmGPUCountLabel(available)
}

func normalizeSlurmReason(reason string) string {
	return strings.Trim(strings.TrimSpace(reason), "()")
}

func slurmReasonBadge(reason string) string {
	switch {
	case reason == "", reason == "None":
		return "READY"
	case reason == "JobArrayTaskLimit":
		return "ARRAY CAP"
	case reason == "Resources":
		return "RESOURCES"
	case reason == "Priority":
		return "PRIORITY"
	case strings.Contains(reason, "Dependency"):
		return "DEPENDENCY"
	case strings.Contains(strings.ToLower(reason), "qos"):
		return "QOS LIMIT"
	case strings.Contains(reason, "Node"):
		return "NODE"
	default:
		return strings.ToUpper(truncate(camelWords(reason), 14))
	}
}

func slurmReasonSummary(reason string) string {
	switch {
	case reason == "JobArrayTaskLimit":
		return "Slurm is enforcing this job array's concurrency throttle."
	case reason == "Resources":
		return "Slurm reports that the requested resources are not currently available."
	case reason == "Priority":
		return "A higher-priority eligible job is ahead in the scheduler."
	case strings.Contains(reason, "Dependency"):
		return "A declared job dependency has not completed."
	case strings.Contains(strings.ToLower(reason), "qos"):
		return "A QOS policy or resource limit is preventing the job from starting."
	case strings.Contains(reason, "Node"):
		return "The requested or eligible node is not currently schedulable."
	default:
		if reason == "" || reason == "None" {
			return "Slurm reports no pending reason."
		}
		return "Slurm reports " + camelWords(reason) + "."
	}
}

func camelWords(value string) string {
	var result []rune
	for index, char := range []rune(value) {
		if index > 0 && char >= 'A' && char <= 'Z' {
			result = append(result, ' ')
		}
		result = append(result, char)
	}
	return string(result)
}

func slurmArrayThrottle(id string) int {
	index := strings.LastIndexByte(id, '%')
	if index < 0 {
		return 0
	}
	end := index + 1
	for end < len(id) && id[end] >= '0' && id[end] <= '9' {
		end++
	}
	value, _ := strconv.Atoi(id[index+1 : end])
	return value
}

func slurmRunningArraySiblings(id string, jobs []slurmJob) int {
	base := id
	if index := strings.IndexByte(base, '_'); index >= 0 {
		base = base[:index]
	}
	count := 0
	for _, job := range jobs {
		if !isSlurmRunning(job.State) {
			continue
		}
		jobBase := job.ID
		if index := strings.IndexByte(jobBase, '_'); index >= 0 {
			jobBase = jobBase[:index]
		}
		if jobBase == base {
			count++
		}
	}
	return count
}

func plural(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func slurmNodeListContains(expression, node string) bool {
	expression, node = strings.TrimSpace(expression), strings.TrimSpace(node)
	if expression == "" || node == "" {
		return false
	}
	if expression == node {
		return true
	}
	for _, item := range splitSlurmNodeList(expression) {
		if item == node {
			return true
		}
		open, close := strings.IndexByte(item, '['), strings.LastIndexByte(item, ']')
		if open < 0 || close <= open || !strings.HasPrefix(node, item[:open]) {
			continue
		}
		number := strings.TrimPrefix(node, item[:open])
		for _, group := range strings.Split(item[open+1:close], ",") {
			bounds := strings.SplitN(group, "-", 2)
			if len(bounds) == 1 && number == bounds[0] {
				return true
			}
			if len(bounds) == 2 && numericSlurmNodeInRange(number, bounds[0], bounds[1]) {
				return true
			}
		}
	}
	return false
}

func splitSlurmNodeList(expression string) []string {
	var result []string
	start, depth := 0, 0
	for index, char := range expression {
		switch char {
		case '[':
			depth++
		case ']':
			depth = max(0, depth-1)
		case ',':
			if depth == 0 {
				result = append(result, strings.TrimSpace(expression[start:index]))
				start = index + 1
			}
		}
	}
	result = append(result, strings.TrimSpace(expression[start:]))
	return result
}

func numericSlurmNodeInRange(value, first, last string) bool {
	number, numberErr := strconv.Atoi(value)
	lower, lowerErr := strconv.Atoi(first)
	upper, upperErr := strconv.Atoi(last)
	return numberErr == nil && lowerErr == nil && upperErr == nil && number >= lower && number <= upper
}

func (m *hubModel) moveSlurmFilter(delta int) {
	if len(m.config.SlurmClusters) == 0 {
		return
	}
	m.slurmFilter = min(max(-1, m.slurmFilter+delta), len(m.config.SlurmClusters)-1)
	m.slurmOffset = 0
	m.slurmCursor = 0
	m.slurmJobID, m.slurmJobCluster = "", ""
	if m.slurmFilter < 0 {
		m.status = "Showing jobs from all Slurm clusters."
		return
	}
	m.status = "Showing jobs from " + m.config.SlurmClusters[m.slurmFilter].Name + "."
}

func (m *hubModel) clampSlurmCursor() {
	jobs := m.selectedSlurmJobs()
	if len(jobs) == 0 {
		m.slurmCursor, m.slurmOffset, m.slurmExplain = 0, 0, false
		m.slurmJobID, m.slurmJobCluster = "", ""
		return
	}
	if m.slurmExplain && m.slurmJobID != "" {
		for index, display := range jobs {
			if slurmStableJobID(display.Job) == m.slurmJobID &&
				display.Cluster == m.slurmJobCluster {
				m.slurmCursor = index
				return
			}
		}
		m.slurmExplain = false
		m.slurmJobID, m.slurmJobCluster = "", ""
		m.status = "The selected job left the queue."
	}
	m.slurmCursor = min(max(0, m.slurmCursor), len(jobs)-1)
}

func (m *hubModel) moveSlurmCursor(delta int) {
	jobs := m.selectedSlurmJobs()
	if len(jobs) == 0 {
		return
	}
	m.slurmCursor = min(max(0, m.slurmCursor+delta), len(jobs)-1)
	if m.slurmExplain {
		m.rememberSlurmSelection()
	}
}

func (m *hubModel) rememberSlurmSelection() {
	jobs := m.selectedSlurmJobs()
	if m.slurmCursor < 0 || m.slurmCursor >= len(jobs) {
		m.slurmJobID, m.slurmJobCluster = "", ""
		return
	}
	m.slurmJobID = slurmStableJobID(jobs[m.slurmCursor].Job)
	m.slurmJobCluster = jobs[m.slurmCursor].Cluster
}

func slurmStableJobID(job slurmJob) string {
	if isSlurmPending(job.State) {
		if separator := strings.IndexByte(job.ID, '_'); separator >= 0 &&
			separator+1 < len(job.ID) && job.ID[separator+1] == '[' {
			return job.ID[:separator] + "_[]"
		}
	}
	return job.ID
}

func (m *hubModel) slurmColumns() int {
	columns := 1
	switch {
	case usableWidth(m.width) >= 132:
		columns = 3
	case usableWidth(m.width) >= 76:
		columns = 2
	}
	if len(m.config.SlurmClusters) > 0 {
		columns = min(columns, len(m.config.SlurmClusters))
	}
	return columns
}

func (m *hubModel) slurmClusterCardLineCount() int {
	if len(m.config.SlurmClusters) == 0 {
		return 1
	}
	width := usableWidth(m.width)
	maximum := 3
	if width >= 92 && m.height >= 28 {
		maximum = 5
	}
	columns := max(1, m.slurmColumns())
	rows := (len(m.config.SlurmClusters) + columns - 1) / columns
	for _, lines := range []int{5, 3} {
		if lines > maximum {
			continue
		}
		cardsHeight := rows * (lines + 2)
		// Keep enough room for the page header, footer, and a useful jobs
		// panel. Dense topologies automatically use compact cards.
		if m.height-cardsHeight >= 10 {
			return lines
		}
	}
	return 3
}

func (m *hubModel) slurmClusterAt(x, y int) (int, bool) {
	if len(m.config.SlurmClusters) == 0 || y < 1 || x < 0 {
		return 0, false
	}
	columns := m.slurmColumns()
	width := usableWidth(m.width)
	cardWidth := max(20, (width-(columns-1))/columns)
	cardHeight := m.slurmClusterCardLineCount() + 2
	row := (y - 1) / cardHeight
	column := x / (cardWidth + 1)
	if column >= columns || x%(cardWidth+1) >= cardWidth {
		return 0, false
	}
	index := row*columns + column
	return index, index >= 0 && index < len(m.config.SlurmClusters)
}

func (m *hubModel) slurmJobAt(x, y int) (int, bool) {
	if x < 0 || x >= usableWidth(m.width) {
		return 0, false
	}
	cardsHeight := lipgloss.Height(m.renderSlurmClusterCards(usableWidth(m.width)))
	firstRow := 1 + cardsHeight + 2
	if y < firstRow {
		return 0, false
	}
	index := m.slurmOffset + y - firstRow
	jobs := m.selectedSlurmJobs()
	return index, index >= 0 && index < len(jobs)
}

func (m *hubModel) slurmQueueView() string {
	width := usableWidth(m.width)
	online := 0
	for _, state := range m.slurmStates {
		if state.Error == "" && !state.Snapshot.CollectedAt.IsZero() {
			online++
		}
	}
	title := titleStyle.Render(m.config.displayName()) +
		dimStyle.Render("  /  ") + gpuTitleStyle.Render("SLURM QUEUES") + "  " +
		liveBadgeStyle.Render(fmt.Sprintf("%d/%d ONLINE", online, len(m.config.SlurmClusters)))
	meta := dimStyle.Render(fmt.Sprintf("QUEUE %s  ·  %s",
		slurmRefreshLabel(m.config.SlurmClusters), strings.ToUpper(m.colorMode.String())))
	if width-lipgloss.Width(title)-lipgloss.Width(meta) < 2 {
		meta = ""
	}
	gap := max(2, width-lipgloss.Width(title)-lipgloss.Width(meta))
	header := ansi.Truncate(title+strings.Repeat(" ", gap)+meta, width, "")
	if m.slurmExplain {
		return m.slurmExplanationView(header, width)
	}

	cards := m.renderSlurmClusterCards(width)
	jobs := m.selectedSlurmJobs()
	running, pending, _ := slurmDisplayStateCounts(jobs)
	next := 0
	for _, job := range jobs {
		if job.Next {
			next++
		}
	}
	filter := "ALL CLUSTERS"
	if m.slurmFilter >= 0 && m.slurmFilter < len(m.config.SlurmClusters) {
		filter = strings.ToUpper(m.config.SlurmClusters[m.slurmFilter].Name)
	}
	footer := strings.Join([]string{
		keyHint("tab", "servers"),
		keyHint("←→", "cluster"),
		keyHint("a", "all"),
		keyHint("↑↓", "select"),
		keyHint("enter", "explain"),
		keyHint("r", "refresh"),
		keyHint("t", "theme"),
		keyHint("q", "hub"),
	}, "  ")
	footer = ansi.Truncate(footer, width, "")
	jobPanelHeight := max(3, m.height-lipgloss.Height(header)-lipgloss.Height(cards)-lipgloss.Height(footer))
	jobContentLines := max(1, jobPanelHeight-2)
	tableRows := max(0, jobContentLines-1)
	m.clampSlurmCursor()
	if m.slurmCursor < m.slurmOffset {
		m.slurmOffset = m.slurmCursor
	}
	if tableRows > 0 && m.slurmCursor >= m.slurmOffset+tableRows {
		m.slurmOffset = m.slurmCursor - tableRows + 1
	}
	m.slurmOffset = min(max(0, m.slurmOffset), max(0, len(jobs)-tableRows))
	end := min(len(jobs), m.slurmOffset+tableRows)
	lines := []string{slurmJobTableHeader(width - 4)}
	if len(jobs) == 0 {
		lines = append(lines, dimStyle.Render("No jobs in the selected queue."))
	} else {
		for index, job := range jobs[m.slurmOffset:end] {
			row := renderSlurmJobRow(job, width-4)
			if m.slurmOffset+index == m.slurmCursor {
				row = selectedProcessStyle(m.colorMode).Width(width - 4).Render(row)
			}
			lines = append(lines, row)
		}
	}
	for len(lines) < jobContentLines {
		lines = append(lines, "")
	}
	lines = lines[:jobContentLines]
	jobMeta := fmt.Sprintf("%s  ·  %d RUN  ·  %d NEXT  ·  %d WAIT  ·  %d JOBS",
		filter, running, next, max(0, pending-next), len(jobs))
	jobPanel := btopPanel(width, "JOBS", jobMeta, strings.Join(lines, "\n"), processTitleStyle, colorProcessBorder)
	return strings.Join([]string{header, cards, jobPanel, footer}, "\n")
}

func (m *hubModel) slurmExplanationView(header string, width int) string {
	jobs := m.selectedSlurmJobs()
	if len(jobs) == 0 {
		m.slurmExplain = false
		return m.slurmQueueView()
	}
	m.clampSlurmCursor()
	display := jobs[m.slurmCursor]
	job := display.Job
	explanation := explainSlurmJob(job, display.Snapshot)
	state, stateStyle := slurmDisplayState(display)
	title := fmt.Sprintf("%s / %s", display.Cluster, job.ID)
	meta := stateStyle.Render(state) + dimStyle.Render("  ·  ") +
		slurmExplanationBadgeStyle(explanation).Render(explanation.Badge)

	controllerReason := normalizeSlurmReason(job.Reason)
	if controllerReason == "" {
		controllerReason = "None"
	}
	lines := []string{
		gpuTitleStyle.Render("WHY IT IS NOT RUNNING"),
		"",
		dimStyle.Render("CONTROLLER  ") + valueStyle.Render(controllerReason),
		dimStyle.Render("EVIDENCE    ") + valueStyle.Render(explanation.Source),
	}
	summaryLines := wrapSlurmExplanation(explanation.Summary, max(12, width-16))
	for index, line := range summaryLines {
		prefix := "            "
		if index == 0 {
			prefix = "EXPLANATION "
		}
		lines = append(lines, dimStyle.Render(prefix)+line)
	}
	lines = append(lines,
		"",
		gpuTitleStyle.Render("REQUEST"),
		fmt.Sprintf(
			"CPU %s  ·  MEMORY %s  ·  GPU %s  ·  NODES %d  ·  QOS %s",
			valueOrDash(job.CPUs), slurmMemoryLabel(job.MemoryBytes),
			slurmGPUCountLabel(slurmGPUCounts(job.GRES)), job.Nodes, slurmQOSLabel(job.QOS),
		),
		fmt.Sprintf(
			"PARTITION %s  ·  CONSTRAINT %s  ·  PRIORITY %d",
			valueOrUnknown(job.Partition), slurmOptional(job.Constraints), job.Priority,
		),
	)
	if explanation.ReasonCode == "Resources" {
		lines = append(lines,
			"",
			gpuTitleStyle.Render("CANDIDATE NODE FIT  ")+
				dimStyle.Render("Slurm reason is authoritative; resource dimensions below are Fleetty inference."),
			slurmNodeFitHeader(width-4),
		)
		if len(explanation.Fits) == 0 {
			lines = append(lines, dimStyle.Render("No matching candidate nodes are visible in this snapshot."))
		} else {
			for _, fit := range explanation.Fits[:min(len(explanation.Fits), max(1, m.height-18))] {
				lines = append(lines, renderSlurmNodeFit(fit, width-4))
			}
		}
	}
	contentHeight := max(1, m.height-lipgloss.Height(header)-3)
	for len(lines) < contentHeight {
		lines = append(lines, "")
	}
	if len(lines) > contentHeight {
		lines = lines[:contentHeight]
	}
	panel := btopPanel(width, title, meta, strings.Join(lines, "\n"), processTitleStyle, colorProcessBorder)
	footer := ansi.Truncate(strings.Join([]string{
		keyHint("esc/q", "queue"),
		keyHint("↑↓", "previous/next job"),
		keyHint("enter", "queue"),
		keyHint("t", "theme"),
	}, "  "), width, "")
	return strings.Join([]string{header, panel, footer}, "\n")
}

func wrapSlurmExplanation(value string, width int) []string {
	width = max(1, width)
	words := strings.Fields(value)
	if len(words) == 0 {
		return []string{""}
	}
	lines := []string{words[0]}
	for _, word := range words[1:] {
		last := len(lines) - 1
		if len([]rune(lines[last]))+1+len([]rune(word)) <= width {
			lines[last] += " " + word
		} else {
			lines = append(lines, word)
		}
	}
	return lines
}

func slurmNodeFitHeader(width int) string {
	header := fmt.Sprintf("%-18s %-16s %-22s %-22s %s",
		"NODE", "CPU FREE / REQ", "MEM FREE / REQ", "GPU FREE / REQ", "VERDICT")
	return dimStyle.Copy().Bold(true).Render(truncate(header, width))
}

func renderSlurmNodeFit(fit slurmNodeFit, width int) string {
	verdict := "FITS"
	style := processRunningStyle
	if len(fit.Blockers) > 0 {
		verdict = strings.Join(fit.Blockers, "+")
		style = warningStyle
	}
	row := fmt.Sprintf("%-18s %7d / %-7d %-10s / %-10s %-10s / %-10s %s",
		truncate(fit.Node, 18),
		fit.CPUAvailable, fit.CPURequest,
		slurmMemoryLabel(fit.MemoryAvailable), slurmMemoryLabel(fit.MemoryRequest),
		truncate(fit.GPUAvailable, 10), truncate(fit.GPURequest, 10),
		verdict,
	)
	return style.Render(truncate(row, width))
}

func slurmExplanationBadgeStyle(explanation slurmJobExplanation) lipgloss.Style {
	if explanation.Badge == "RUNNING" {
		return processRunningStyle
	}
	if explanation.ReasonCode == "Priority" {
		return gpuTitleStyle
	}
	return warningStyle
}

func slurmMemoryLabel(value uint64) string {
	if value == 0 {
		return "—"
	}
	return bytes(value)
}

func slurmOptional(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "(null)" || value == "N/A" {
		return "—"
	}
	return value
}

func valueOrUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}

func valueOrDash(value int) string {
	if value <= 0 {
		return "—"
	}
	return strconv.Itoa(value)
}

func (m *hubModel) renderSlurmClusterCards(width int) string {
	if len(m.config.SlurmClusters) == 0 {
		return btopPanel(width, "SLURM", "NOT CONFIGURED", dimStyle.Render("No Slurm clusters are configured."),
			gpuTitleStyle, colorGPUBorder)
	}
	columns := m.slurmColumns()
	contentLines := m.slurmClusterCardLineCount()
	cardWidth := max(20, (width-(columns-1))/columns)
	var rows []string
	for start := 0; start < len(m.config.SlurmClusters); start += columns {
		end := min(start+columns, len(m.config.SlurmClusters))
		var cards []string
		for index := start; index < end; index++ {
			if len(cards) > 0 {
				cards = append(cards, " ")
			}
			cards = append(cards, m.renderSlurmClusterCard(index, cardWidth, contentLines))
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, cards...))
	}
	return strings.Join(rows, "\n")
}

func (m *hubModel) renderSlurmClusterCard(index, width, contentLines int) string {
	config := m.config.SlurmClusters[index]
	state := slurmClusterState{}
	if index < len(m.slurmStates) {
		state = m.slurmStates[index]
	}
	titleForCard := gpuTitleStyle
	border := colorGPUBorder
	if index == m.slurmFilter {
		titleForCard = accentStyle
		border = lipgloss.Color("#B9A4FF")
	}
	meta := "CHECKING"
	content := fitSlurmClusterCardLines([]string{
		gpuTitleStyle.Render("● COLLECTING"),
		dimStyle.Render(truncate(config.Description, width-4)),
		dimStyle.Render("Waiting for queue data…"),
	}, contentLines)
	if state.Error != "" {
		meta = "OFFLINE"
		lastSeen := "Never seen online"
		if !state.LastSeen.IsZero() {
			lastSeen = "Last seen " + hubAge(time.Since(state.LastSeen)) + " ago"
		}
		content = fitSlurmClusterCardLines([]string{
			dangerStyle.Render("● SOURCE OFFLINE"),
			dimStyle.Render(lastSeen),
			warningStyle.Render(truncate(state.Error, width-4)),
		}, contentLines)
	} else if !state.Snapshot.CollectedAt.IsZero() {
		meta = "LIVE"
		if state.Warning != "" {
			meta = "STALE"
		}
		snapshot := state.Snapshot
		content = []string{
			slurmClusterQueueSummaryLine(snapshot),
			slurmClusterResourceLine(snapshot),
			slurmClusterGPUTypeSummaryLine(snapshot),
		}
		for _, partition := range snapshot.Partitions[:min(len(snapshot.Partitions), max(0, contentLines-len(content)))] {
			name := partition.Name
			if partition.Default {
				name += "*"
			}
			stateLabel := strings.Join(partition.States, ",")
			line := fmt.Sprintf("%-14s %dN  CPU %d/%d  %s",
				truncate(name, 14), partition.Nodes, partition.CPUsAlloc,
				partition.CPUsTotal, stateLabel)
			content = append(content, slurmPartitionStyle(partition).Render(truncate(line, width-4)))
		}
		content = fitSlurmClusterCardLines(content, contentLines)
	}
	return btopPanel(width, config.Name, meta, strings.Join(content, "\n"), titleForCard, border)
}

func fitSlurmClusterCardLines(lines []string, count int) []string {
	count = max(1, count)
	if len(lines) > count {
		lines = lines[:count]
	}
	for len(lines) < count {
		lines = append(lines, "")
	}
	return lines
}

func slurmClusterQueueSummaryLine(snapshot slurmSnapshot) string {
	running, pending, other := slurmStateCounts(snapshot.Jobs)
	next := 0
	for _, job := range snapshot.Jobs {
		if slurmEligibleForNext(job) {
			next = 1
			break
		}
	}
	line := fmt.Sprintf("%s %d  ·  %s %d  ·  %s %d",
		processRunningStyle.Render("RUN"), running,
		processWaitingStyle.Render("NEXT"), next,
		gpuTitleStyle.Render("WAIT"), max(0, pending-next))
	if other > 0 {
		line += "  ·  " + dimStyle.Render(fmt.Sprintf("OTHER %d", other))
	}
	return line
}

func slurmClusterResourceLine(snapshot slurmSnapshot) string {
	totalGPUs, _ := slurmClusterGPUInventory(snapshot)
	busyGPUs := 0
	for _, job := range snapshot.Jobs {
		if !isSlurmRunning(job.State) {
			continue
		}
		for _, count := range slurmGPUCounts(job.GRES) {
			busyGPUs += count
		}
	}
	allocatedCPUs, totalCPUs := 0, 0
	for _, partition := range snapshot.Partitions {
		allocatedCPUs += partition.CPUsAlloc
		totalCPUs += partition.CPUsTotal
	}
	return dimStyle.Render(fmt.Sprintf("NODES %d  ·  GPU %d/%d  ·  CPU %d/%d",
		len(snapshot.Nodes), min(busyGPUs, totalGPUs), totalGPUs,
		allocatedCPUs, totalCPUs))
}

func slurmClusterGPUTypeSummaryLine(snapshot slurmSnapshot) string {
	_, inventory := slurmClusterGPUInventory(snapshot)
	if len(inventory) == 0 {
		return gpuTitleStyle.Render("GPU  —")
	}
	names := make([]string, 0, len(inventory))
	for name := range inventory {
		names = append(names, name)
	}
	sort.Strings(names)
	const visibleTypes = 2
	parts := make([]string, 0, min(len(names), visibleTypes)+1)
	for _, name := range names[:min(len(names), visibleTypes)] {
		label := strings.ToUpper(strings.TrimSpace(strings.ReplaceAll(name, "_", " ")))
		if label == "" {
			label = "GENERIC"
		}
		parts = append(parts, fmt.Sprintf("%s×%d", label, inventory[name]))
	}
	if hidden := len(names) - len(parts); hidden > 0 {
		parts = append(parts, fmt.Sprintf("+%d TYPES", hidden))
	}
	return gpuTitleStyle.Render("GPU  " + strings.Join(parts, "  ·  "))
}

func slurmClusterGPUInventory(snapshot slurmSnapshot) (int, map[string]int) {
	inventory := make(map[string]int)
	total := 0
	for _, node := range snapshot.Nodes {
		for _, gres := range node.GRES {
			for gpuType, count := range slurmGPUCounts(gres) {
				inventory[gpuType] += count
				total += count
			}
		}
	}
	return total, inventory
}

func slurmRefreshLabel(clusters []slurmClusterConfig) string {
	if len(clusters) == 0 {
		return "—"
	}
	minimum := clusters[0].refreshInterval()
	maximum := minimum
	for _, cluster := range clusters[1:] {
		interval := cluster.refreshInterval()
		if interval < minimum {
			minimum = interval
		}
		if interval > maximum {
			maximum = interval
		}
	}
	if minimum == maximum {
		return fmt.Sprintf("%ds", int(minimum.Seconds()))
	}
	return fmt.Sprintf("%d–%ds", int(minimum.Seconds()), int(maximum.Seconds()))
}

func slurmPartitionStyle(partition slurmPartition) lipgloss.Style {
	for _, state := range partition.States {
		switch {
		case strings.HasPrefix(state, "down"), strings.HasPrefix(state, "drain"), strings.HasPrefix(state, "fail"):
			return dangerStyle
		case strings.HasPrefix(state, "alloc"), strings.HasPrefix(state, "mix"):
			return processRunningStyle
		}
	}
	if strings.EqualFold(partition.Available, "up") {
		return processSleepingStyle
	}
	return warningStyle
}

func (m *hubModel) selectedSlurmJobs() []slurmDisplayJob {
	var jobs []slurmDisplayJob
	for index := range m.slurmStates {
		state := &m.slurmStates[index]
		if m.slurmFilter >= 0 && index != m.slurmFilter {
			continue
		}
		if state.Error != "" || state.Snapshot.CollectedAt.IsZero() {
			continue
		}
		nextMarked := false
		for order, job := range state.Snapshot.Jobs {
			next := !nextMarked && slurmEligibleForNext(job)
			nextMarked = nextMarked || next
			jobs = append(jobs, slurmDisplayJob{
				Cluster: state.Snapshot.Name, Job: job, Next: next, Order: order,
				Snapshot: &state.Snapshot,
			})
		}
	}
	sortSlurmJobs(jobs)
	return jobs
}

func slurmDisplayStateCounts(jobs []slurmDisplayJob) (running, pending, other int) {
	for _, job := range jobs {
		switch strings.ToUpper(job.Job.State) {
		case "RUNNING", "COMPLETING":
			running++
		case "PENDING", "CONFIGURING":
			pending++
		default:
			other++
		}
	}
	return
}

func slurmJobTableHeader(width int) string {
	var header string
	switch {
	case width >= 152:
		reasonWidth := max(8, width-144)
		header = fmt.Sprintf("%-14s %-15s %-10s %-11s %10s %-12s %-9s %-9s %5s %-16s %-22s %-*s",
			"CLUSTER", "JOB ID", "USER", "STATE", "WEIGHT", "QOS", "ELAPSED", "LIMIT", "NODES", "PARTITION", "NAME",
			reasonWidth, "NODE / BLOCKER")
	case width >= 112:
		reasonWidth := max(8, width-104)
		header = fmt.Sprintf("%-12s %-13s %-9s %-10s %10s %-10s %-8s %5s %-18s %-*s",
			"CLUSTER", "JOB ID", "USER", "STATE", "WEIGHT", "QOS", "ELAPSED", "NODES", "NAME",
			reasonWidth, "NODE / BLOCKER")
	case width >= 80:
		header = fmt.Sprintf("%-12s %-13s %-9s %-10s %10s %-10s %-8s",
			"CLUSTER", "JOB ID", "USER", "STATE", "WEIGHT", "QOS", "ELAPSED")
	default:
		header = fmt.Sprintf("%-13s %-9s %10s %-10s %s", "JOB ID", "STATE", "WEIGHT", "QOS", "USER")
	}
	return dimStyle.Copy().Bold(true).Render(truncate(header, width))
}

func renderSlurmJobRow(display slurmDisplayJob, width int) string {
	job := display.Job
	stateLabel, stateStyle := slurmDisplayState(display)
	reason := slurmDisplayReason(display)
	reasonStyle := dimStyle
	if display.Next {
		reasonStyle = warningStyle
	} else if isSlurmPending(job.State) {
		reasonStyle = gpuTitleStyle
	}
	switch {
	case width >= 152:
		reasonWidth := max(8, width-144)
		return strings.Join([]string{
			fixedCell(display.Cluster, 14, false),
			valueStyle.Render(fixedCell(job.ID, 15, false)),
			fixedCell(job.User, 10, false),
			stateStyle.Render(fixedCell(stateLabel, 11, false)),
			valueStyle.Render(fixedCell(strconv.FormatUint(job.Priority, 10), 10, true)),
			gpuTitleStyle.Render(fixedCell(slurmQOSLabel(job.QOS), 12, false)),
			fixedCell(job.Elapsed, 9, true),
			fixedCell(job.TimeLimit, 9, true),
			fixedCell(strconv.Itoa(job.Nodes), 5, true),
			fixedCell(job.Partition, 16, false),
			fixedCell(job.Name, 22, false),
			reasonStyle.Render(fixedCell(reason, reasonWidth, false)),
		}, " ")
	case width >= 112:
		reasonWidth := max(8, width-104)
		return strings.Join([]string{
			fixedCell(display.Cluster, 12, false),
			valueStyle.Render(fixedCell(job.ID, 13, false)),
			fixedCell(job.User, 9, false),
			stateStyle.Render(fixedCell(stateLabel, 10, false)),
			valueStyle.Render(fixedCell(strconv.FormatUint(job.Priority, 10), 10, true)),
			gpuTitleStyle.Render(fixedCell(slurmQOSLabel(job.QOS), 10, false)),
			fixedCell(job.Elapsed, 8, true),
			fixedCell(strconv.Itoa(job.Nodes), 5, true),
			fixedCell(job.Name, 18, false),
			reasonStyle.Render(fixedCell(reason, reasonWidth, false)),
		}, " ")
	case width >= 80:
		return strings.Join([]string{
			fixedCell(display.Cluster, 12, false),
			valueStyle.Render(fixedCell(job.ID, 13, false)),
			fixedCell(job.User, 9, false),
			stateStyle.Render(fixedCell(stateLabel, 10, false)),
			valueStyle.Render(fixedCell(strconv.FormatUint(job.Priority, 10), 10, true)),
			gpuTitleStyle.Render(fixedCell(slurmQOSLabel(job.QOS), 10, false)),
			fixedCell(job.Elapsed, 8, true),
		}, " ")
	default:
		return valueStyle.Render(fixedCell(job.ID, 13, false)) + " " +
			stateStyle.Render(fixedCell(stateLabel, 9, false)) + " " +
			valueStyle.Render(fixedCell(strconv.FormatUint(job.Priority, 10), 10, true)) + " " +
			gpuTitleStyle.Render(fixedCell(slurmQOSLabel(job.QOS), 10, false)) + " " +
			fixedCell(job.User, max(6, width-46), false)
	}
}

func slurmDisplayReason(display slurmDisplayJob) string {
	if isSlurmRunning(display.Job.State) {
		if slurmNodeExpressionPresent(display.Job.NodeList) {
			return display.Job.NodeList
		}
		return normalizeSlurmReason(display.Job.Reason)
	}
	return explainSlurmJob(display.Job, display.Snapshot).Badge
}

func slurmQOSLabel(qos string) string {
	qos = strings.TrimSpace(qos)
	if qos == "" || strings.EqualFold(qos, "(null)") {
		return "-"
	}
	return qos
}

func slurmDisplayState(display slurmDisplayJob) (string, lipgloss.Style) {
	switch {
	case isSlurmRunning(display.Job.State):
		return "RUNNING", processRunningStyle
	case display.Next:
		return "NEXT", processWaitingStyle
	case isSlurmPending(display.Job.State):
		return "QUEUED", gpuTitleStyle
	default:
		return strings.ToUpper(display.Job.State), slurmJobStateStyle(display.Job.State)
	}
}

func (m *monitorModel) clampQueueOffset(rows int) {
	if m.slurmQueue == nil || len(m.slurmQueue.Jobs) == 0 {
		m.queueOffset = 0
		return
	}
	m.queueOffset = min(max(0, m.queueOffset), max(0, len(m.slurmQueue.Jobs)-rows))
}

func (m *monitorModel) slurmNodePanel(width, rowLimit int) string {
	queue := m.slurmQueue
	if queue == nil {
		return ""
	}
	meta := fmt.Sprintf("%s  ·  %s", queue.Cluster, queue.Node)
	if !queue.CollectedAt.IsZero() {
		meta += "  ·  " + hubAge(time.Since(queue.CollectedAt)) + " AGO"
	}
	lines := []string{
		strings.Join([]string{
			processRunningStyle.Render("● RUNNING"),
			processWaitingStyle.Render("◆ NEXT"),
			gpuTitleStyle.Render("○ QUEUED"),
		}, dimStyle.Render("  ·  ")),
		slurmNodeJobHeader(width - 4),
	}
	if queue.Warning != "" {
		lines = append(lines, warningStyle.Render(truncate(queue.Warning, width-4)))
	}
	if len(queue.Jobs) == 0 {
		lines = append(lines, dimStyle.Render("No running or eligible queued jobs for this node."))
	} else {
		m.clampQueueOffset(rowLimit)
		end := min(len(queue.Jobs), m.queueOffset+rowLimit)
		for _, job := range queue.Jobs[m.queueOffset:end] {
			lines = append(lines, renderSlurmNodeJobRow(job, width-4))
		}
		remaining := len(queue.Jobs) - end
		if (remaining > 0 || m.queueOffset > 0) && rowLimit > 1 {
			more := fmt.Sprintf("  %d–%d/%d", m.queueOffset+1, end, len(queue.Jobs))
			lines[len(lines)-1] = ansi.Truncate(lines[len(lines)-1],
				max(1, width-lipgloss.Width(more)-4), "") + dimStyle.Render(more)
		}
	}
	titleForPanel := processTitleStyle
	border := colorProcessBorder
	if m.monitorFocus == monitorFocusQueue {
		titleForPanel = accentStyle
		border = lipgloss.Color("#B9A4FF")
	}
	return btopPanel(width, "NODE QUEUE", meta, strings.Join(lines, "\n"), titleForPanel, border)
}

func slurmNodeJobHeader(width int) string {
	var header string
	switch {
	case width >= 108:
		header = fmt.Sprintf("%-15s %-10s %-9s %10s %-12s %-9s %-9s %-16s %s",
			"JOB ID", "USER", "STATE", "WEIGHT", "QOS", "ELAPSED", "LIMIT", "PARTITION", "NAME / BLOCKER")
	case width >= 76:
		header = fmt.Sprintf("%-14s %-9s %-9s %10s %-10s %-8s %s",
			"JOB ID", "USER", "STATE", "WEIGHT", "QOS", "ELAPSED", "NAME / BLOCKER")
	case width >= 54:
		header = fmt.Sprintf("%-13s %-9s %10s %-10s %s", "JOB ID", "STATE", "WEIGHT", "QOS", "NAME")
	default:
		header = fmt.Sprintf("%-10s %-7s %8s %-8s", "JOB ID", "STATE", "WEIGHT", "QOS")
	}
	return dimStyle.Copy().Bold(true).Render(truncate(header, width))
}

func renderSlurmNodeJobRow(display slurmDisplayJob, width int) string {
	job := display.Job
	state, style := slurmDisplayState(display)
	detail := job.Name
	if isSlurmPending(job.State) && job.Reason != "" {
		detail += "  [" + slurmDisplayReason(display) + "]"
	}
	switch {
	case width >= 108:
		fixed := 15 + 1 + 10 + 1 + 9 + 1 + 10 + 1 + 12 + 1 + 9 + 1 + 9 + 1 + 16 + 1
		return strings.Join([]string{
			valueStyle.Render(fixedCell(job.ID, 15, false)),
			fixedCell(job.User, 10, false),
			style.Render(fixedCell(state, 9, false)),
			valueStyle.Render(fixedCell(strconv.FormatUint(job.Priority, 10), 10, true)),
			gpuTitleStyle.Render(fixedCell(slurmQOSLabel(job.QOS), 12, false)),
			fixedCell(job.Elapsed, 9, true),
			fixedCell(job.TimeLimit, 9, true),
			fixedCell(job.Partition, 16, false),
			fixedCell(detail, max(8, width-fixed), false),
		}, " ")
	case width >= 76:
		fixed := 14 + 1 + 9 + 1 + 9 + 1 + 10 + 1 + 10 + 1 + 8 + 1
		return strings.Join([]string{
			valueStyle.Render(fixedCell(job.ID, 14, false)),
			fixedCell(job.User, 9, false),
			style.Render(fixedCell(state, 9, false)),
			valueStyle.Render(fixedCell(strconv.FormatUint(job.Priority, 10), 10, true)),
			gpuTitleStyle.Render(fixedCell(slurmQOSLabel(job.QOS), 10, false)),
			fixedCell(job.Elapsed, 8, true),
			fixedCell(detail, max(8, width-fixed), false),
		}, " ")
	case width >= 54:
		fixed := 13 + 1 + 9 + 1 + 10 + 1 + 10 + 1
		return valueStyle.Render(fixedCell(job.ID, 13, false)) + " " +
			style.Render(fixedCell(state, 9, false)) + " " +
			valueStyle.Render(fixedCell(strconv.FormatUint(job.Priority, 10), 10, true)) + " " +
			gpuTitleStyle.Render(fixedCell(slurmQOSLabel(job.QOS), 10, false)) + " " +
			fixedCell(detail, max(6, width-fixed), false)
	default:
		return strings.Join([]string{
			valueStyle.Render(fixedCell(job.ID, 10, false)),
			style.Render(fixedCell(state, 7, false)),
			valueStyle.Render(fixedCell(strconv.FormatUint(job.Priority, 10), 8, true)),
			gpuTitleStyle.Render(fixedCell(slurmQOSLabel(job.QOS), 8, false)),
		}, " ")
	}
}
