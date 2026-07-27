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
	ID        string
	Partition string
	Name      string
	User      string
	State     string
	Elapsed   string
	TimeLimit string
	Nodes     int
	NodeList  string
	Reason    string
}

type slurmNode struct {
	Name       string
	Partitions []string
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
	key, err := os.ReadFile(config.IdentityFile)
	if err != nil {
		return nil, fmt.Errorf("read identity file: %w", err)
	}
	signer, err := gossh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("parse identity file: %w", err)
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
	nodeOutput, err := runner.Run(ctx, "sinfo", "-N", "-h", "-o", "%N\t%P")
	if err != nil {
		return slurmSnapshot{}, err
	}
	jobOutput, err := runner.Run(ctx, "squeue", "-h", "-o", "%i\t%P\t%j\t%u\t%T\t%M\t%l\t%D\t%N\t%R")
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
		if len(fields) != 2 {
			return nil, fmt.Errorf("parse sinfo node line %d: expected 2 fields", lineNumber+1)
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
		if len(fields) != 10 {
			return nil, fmt.Errorf("parse squeue line %d: expected 10 fields", lineNumber+1)
		}
		partition := strings.TrimSpace(fields[1])
		if len(allowed) > 0 {
			if _, ok := allowed[partition]; !ok {
				continue
			}
		}
		nodes, err := strconv.Atoi(strings.TrimSpace(fields[7]))
		if err != nil {
			return nil, fmt.Errorf("parse squeue nodes on line %d: %w", lineNumber+1, err)
		}
		jobs = append(jobs, slurmJob{
			ID: strings.TrimSpace(fields[0]), Partition: partition,
			Name: strings.TrimSpace(fields[2]), User: strings.TrimSpace(fields[3]),
			State: strings.TrimSpace(fields[4]), Elapsed: strings.TrimSpace(fields[5]),
			TimeLimit: strings.TrimSpace(fields[6]), Nodes: nodes,
			NodeList: strings.TrimSpace(fields[8]), Reason: strings.TrimSpace(fields[9]),
		})
	}
	return jobs, nil
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

type slurmDisplayJob struct {
	Cluster string
	Job     slurmJob
	Next    bool
	Order   int
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
					Cluster: state.Snapshot.Name, Job: job, Order: order,
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
		next := !nextMarked
		nextMarked = true
		queue.Jobs = append(queue.Jobs, slurmDisplayJob{
			Cluster: state.Snapshot.Name, Job: job, Next: next, Order: order,
		})
	}
	sortSlurmJobs(queue.Jobs)
	return queue
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
	if m.slurmFilter < 0 {
		m.status = "Showing jobs from all Slurm clusters."
		return
	}
	m.status = "Showing jobs from " + m.config.SlurmClusters[m.slurmFilter].Name + "."
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

func (m *hubModel) slurmClusterAt(x, y int) (int, bool) {
	if y < 1 || x < 0 {
		return 0, false
	}
	columns := m.slurmColumns()
	width := usableWidth(m.width)
	cardWidth := max(20, (width-(columns-1))/columns)
	row := (y - 1) / hubCardHeight
	if (y-1)%hubCardHeight >= hubCardHeight {
		return 0, false
	}
	column := x / (cardWidth + 1)
	if column >= columns || x%(cardWidth+1) >= cardWidth {
		return 0, false
	}
	index := row*columns + column
	return index, index >= 0 && index < len(m.config.SlurmClusters)
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
		keyHint("↑↓", "scroll"),
		keyHint("r", "refresh"),
		keyHint("t", "theme"),
		keyHint("q", "quit"),
	}, "  ")
	footer = ansi.Truncate(footer, width, "")
	tableRows := max(1, m.height-lipgloss.Height(header)-lipgloss.Height(cards)-lipgloss.Height(footer)-3)
	m.slurmOffset = min(max(0, m.slurmOffset), max(0, len(jobs)-tableRows))
	end := min(len(jobs), m.slurmOffset+tableRows)
	lines := []string{slurmJobTableHeader(width - 4)}
	if len(jobs) == 0 {
		lines = append(lines, dimStyle.Render("No jobs in the selected queue."))
	} else {
		for _, job := range jobs[m.slurmOffset:end] {
			lines = append(lines, renderSlurmJobRow(job, width-4))
		}
	}
	jobMeta := fmt.Sprintf("%s  ·  %d RUN  ·  %d NEXT  ·  %d WAIT  ·  %d JOBS",
		filter, running, next, max(0, pending-next), len(jobs))
	jobPanel := btopPanel(width, "JOBS", jobMeta, strings.Join(lines, "\n"), processTitleStyle, colorProcessBorder)
	return strings.Join([]string{header, cards, jobPanel, footer}, "\n")
}

func (m *hubModel) renderSlurmClusterCards(width int) string {
	if len(m.config.SlurmClusters) == 0 {
		return btopPanel(width, "SLURM", "NOT CONFIGURED", dimStyle.Render("No Slurm clusters are configured."),
			gpuTitleStyle, colorGPUBorder)
	}
	columns := m.slurmColumns()
	cardWidth := max(20, (width-(columns-1))/columns)
	var rows []string
	for start := 0; start < len(m.config.SlurmClusters); start += columns {
		end := min(start+columns, len(m.config.SlurmClusters))
		var cards []string
		for index := start; index < end; index++ {
			if len(cards) > 0 {
				cards = append(cards, " ")
			}
			cards = append(cards, m.renderSlurmClusterCard(index, cardWidth))
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, cards...))
	}
	return strings.Join(rows, "\n")
}

func (m *hubModel) renderSlurmClusterCard(index, width int) string {
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
	content := []string{
		dimStyle.Render(truncate(config.Description, width-4)),
		dimStyle.Render(strings.ToUpper(config.Transport)),
		"Waiting for the first queue snapshot…",
		dimStyle.Render(strings.Repeat("·", max(1, width-4))),
		dimStyle.Render("Click or use ← → to filter"),
	}
	if state.Error != "" {
		meta = "OFFLINE"
		source := "LOCAL"
		if config.Transport == "ssh" {
			source = "SSH  " + normalizeSSHAddress(config.Address)
		}
		lastSeen := "Never seen online"
		if !state.LastSeen.IsZero() {
			lastSeen = "Last seen " + hubAge(time.Since(state.LastSeen)) + " ago"
		}
		content = []string{
			dangerStyle.Render("● SLURM SOURCE OFFLINE"),
			dimStyle.Render(source),
			dimStyle.Render(lastSeen),
			warningStyle.Render(truncate(state.Error, width-4)),
			dimStyle.Render("[r] retry now"),
		}
	} else if !state.Snapshot.CollectedAt.IsZero() {
		meta = fmt.Sprintf("%dms", state.Latency.Milliseconds())
		if state.Warning != "" {
			meta = "STALE"
		}
		snapshot := state.Snapshot
		running, pending, other := slurmStateCounts(snapshot.Jobs)
		next := min(1, pending)
		content = []string{
			fmt.Sprintf("%s %d   %s %d   %s %d   %s %d",
				processRunningStyle.Render("RUN"), running,
				processWaitingStyle.Render("NEXT"), next,
				gpuTitleStyle.Render("WAIT"), max(0, pending-next),
				dimStyle.Render("OTHER"), other),
			dimStyle.Render(fmt.Sprintf("%d partitions  ·  %s", len(snapshot.Partitions), snapshot.Version)),
		}
		for _, partition := range snapshot.Partitions {
			name := partition.Name
			if partition.Default {
				name += "*"
			}
			stateLabel := strings.Join(partition.States, ",")
			line := fmt.Sprintf("%-16s %2dN  CPU %d/%d  %s",
				truncate(name, 16), partition.Nodes, partition.CPUsAlloc, partition.CPUsTotal, stateLabel)
			content = append(content, slurmPartitionStyle(partition).Render(truncate(line, width-4)))
			if len(content) == 5 {
				break
			}
		}
		for len(content) < 5 {
			content = append(content, "")
		}
	}
	return btopPanel(width, config.Name, meta, strings.Join(content, "\n"), titleForCard, border)
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
	for index, state := range m.slurmStates {
		if m.slurmFilter >= 0 && index != m.slurmFilter {
			continue
		}
		if state.Error != "" || state.Snapshot.CollectedAt.IsZero() {
			continue
		}
		nextMarked := false
		for order, job := range state.Snapshot.Jobs {
			next := !nextMarked && isSlurmPending(job.State)
			nextMarked = nextMarked || next
			jobs = append(jobs, slurmDisplayJob{
				Cluster: state.Snapshot.Name, Job: job, Next: next, Order: order,
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
	case width >= 124:
		reasonWidth := max(10, width-120)
		header = fmt.Sprintf("%-14s %-15s %-10s %-11s %-9s %-9s %5s %-16s %-22s %-*s",
			"CLUSTER", "JOB ID", "USER", "STATE", "ELAPSED", "LIMIT", "NODES", "PARTITION", "NAME",
			reasonWidth, "NODE / REASON")
	case width >= 84:
		reasonWidth := max(8, width-82)
		header = fmt.Sprintf("%-12s %-13s %-9s %-10s %-8s %5s %-18s %-*s",
			"CLUSTER", "JOB ID", "USER", "STATE", "ELAPSED", "NODES", "NAME", reasonWidth, "NODE / REASON")
	case width >= 58:
		header = fmt.Sprintf("%-12s %-13s %-9s %-10s %-8s", "CLUSTER", "JOB ID", "USER", "STATE", "ELAPSED")
	default:
		header = "JOB ID        STATE     USER"
	}
	return dimStyle.Copy().Bold(true).Render(truncate(header, width))
}

func renderSlurmJobRow(display slurmDisplayJob, width int) string {
	job := display.Job
	stateLabel, stateStyle := slurmDisplayState(display)
	reasonStyle := dimStyle
	if display.Next {
		reasonStyle = warningStyle
	} else if isSlurmPending(job.State) {
		reasonStyle = gpuTitleStyle
	}
	switch {
	case width >= 124:
		reasonWidth := max(10, width-120)
		return strings.Join([]string{
			fixedCell(display.Cluster, 14, false),
			valueStyle.Render(fixedCell(job.ID, 15, false)),
			fixedCell(job.User, 10, false),
			stateStyle.Render(fixedCell(stateLabel, 11, false)),
			fixedCell(job.Elapsed, 9, true),
			fixedCell(job.TimeLimit, 9, true),
			fixedCell(strconv.Itoa(job.Nodes), 5, true),
			fixedCell(job.Partition, 16, false),
			fixedCell(job.Name, 22, false),
			reasonStyle.Render(fixedCell(job.Reason, reasonWidth, false)),
		}, " ")
	case width >= 84:
		reasonWidth := max(8, width-82)
		return strings.Join([]string{
			fixedCell(display.Cluster, 12, false),
			valueStyle.Render(fixedCell(job.ID, 13, false)),
			fixedCell(job.User, 9, false),
			stateStyle.Render(fixedCell(stateLabel, 10, false)),
			fixedCell(job.Elapsed, 8, true),
			fixedCell(strconv.Itoa(job.Nodes), 5, true),
			fixedCell(job.Name, 18, false),
			reasonStyle.Render(fixedCell(job.Reason, reasonWidth, false)),
		}, " ")
	case width >= 58:
		return strings.Join([]string{
			fixedCell(display.Cluster, 12, false),
			valueStyle.Render(fixedCell(job.ID, 13, false)),
			fixedCell(job.User, 9, false),
			stateStyle.Render(fixedCell(stateLabel, 10, false)),
			fixedCell(job.Elapsed, 8, true),
		}, " ")
	default:
		return valueStyle.Render(fixedCell(job.ID, 13, false)) + " " +
			stateStyle.Render(fixedCell(stateLabel, 9, false)) + " " +
			fixedCell(job.User, max(6, width-24), false)
	}
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

func (m *monitorModel) slurmQueueRows() int {
	if m.slurmQueue == nil {
		return 0
	}
	limit := 4
	switch {
	case m.height < 34:
		limit = 1
	case m.height < 42:
		limit = 2
	case m.height >= 54:
		limit = 5
	}
	return min(limit, max(1, len(m.slurmQueue.Jobs)))
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
		for index, job := range queue.Jobs {
			if index >= rowLimit {
				break
			}
			lines = append(lines, renderSlurmNodeJobRow(job, width-4))
		}
		remaining := len(queue.Jobs) - min(len(queue.Jobs), rowLimit)
		if remaining > 0 && rowLimit > 1 {
			lines[len(lines)-1] = ansi.Truncate(lines[len(lines)-1],
				max(1, width-lipgloss.Width(fmt.Sprintf("  +%d more", remaining))-4), "") +
				dimStyle.Render(fmt.Sprintf("  +%d more", remaining))
		}
	}
	return btopPanel(width, "NODE QUEUE", meta, strings.Join(lines, "\n"), processTitleStyle, colorProcessBorder)
}

func slurmNodeJobHeader(width int) string {
	var header string
	switch {
	case width >= 108:
		header = fmt.Sprintf("%-15s %-10s %-9s %-9s %-9s %-16s %s",
			"JOB ID", "USER", "STATE", "ELAPSED", "LIMIT", "PARTITION", "NAME / REASON")
	case width >= 72:
		header = fmt.Sprintf("%-14s %-9s %-9s %-8s %s", "JOB ID", "USER", "STATE", "ELAPSED", "NAME / REASON")
	default:
		header = fmt.Sprintf("%-13s %-9s %s", "JOB ID", "STATE", "NAME")
	}
	return dimStyle.Copy().Bold(true).Render(truncate(header, width))
}

func renderSlurmNodeJobRow(display slurmDisplayJob, width int) string {
	job := display.Job
	state, style := slurmDisplayState(display)
	detail := job.Name
	if isSlurmPending(job.State) && job.Reason != "" {
		detail += "  " + job.Reason
	}
	switch {
	case width >= 108:
		fixed := 15 + 1 + 10 + 1 + 9 + 1 + 9 + 1 + 9 + 1 + 16 + 1
		return strings.Join([]string{
			valueStyle.Render(fixedCell(job.ID, 15, false)),
			fixedCell(job.User, 10, false),
			style.Render(fixedCell(state, 9, false)),
			fixedCell(job.Elapsed, 9, true),
			fixedCell(job.TimeLimit, 9, true),
			fixedCell(job.Partition, 16, false),
			fixedCell(detail, max(8, width-fixed), false),
		}, " ")
	case width >= 72:
		fixed := 14 + 1 + 9 + 1 + 9 + 1 + 8 + 1
		return strings.Join([]string{
			valueStyle.Render(fixedCell(job.ID, 14, false)),
			fixedCell(job.User, 9, false),
			style.Render(fixedCell(state, 9, false)),
			fixedCell(job.Elapsed, 8, true),
			fixedCell(detail, max(8, width-fixed), false),
		}, " ")
	default:
		fixed := 13 + 1 + 9 + 1
		return valueStyle.Render(fixedCell(job.ID, 13, false)) + " " +
			style.Render(fixedCell(state, 9, false)) + " " +
			fixedCell(detail, max(6, width-fixed), false)
	}
}
