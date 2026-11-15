package main

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type hubPeerLink struct {
	node, sockets, sampled int
	rx, tx                 uint64
}

type hubLinkObservation struct {
	node      int
	sampledAt time.Time
	available bool
	peers     []hubPeerLink
}

type hubLinkEdge struct {
	a, b           int
	ab, ba         uint64
	known, partial bool
}

type hubLinkGraph struct {
	sampledAt         time.Time
	eligible, sampled int
	unmeasured        int
	edges             []hubLinkEdge
}

func (m *hubModel) hubCompactLinks() bool {
	_, sidebar := hubRackWidths(m.width)
	return sidebar == 0 && m.linksView && m.linksExplicit
}

func (m *hubModel) showHubLinks() bool {
	_, sidebar := hubRackWidths(m.width)
	return m.linksView && (sidebar > 0 || m.linksExplicit)
}

func (m *hubModel) refreshHubLinks() {
	if m.service != nil && m.detail == nil && m.showHubLinks() {
		m.links = m.service.linksForView()
	}
}

func (s *hubService) linksForView() hubLinkGraph {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if !now.Before(s.linksViewUntil) {
		select {
		case s.linksWake <- struct{}{}:
		default:
		}
	}
	// Every open Links view renews the same lease on its regular Hub tick.
	s.linksViewUntil = now.Add(2 * s.config.refreshInterval())
	return s.links
}

func (s *hubService) runLinks(ctx context.Context) {
	// Do not contend with the overview RPC pool. At one-second refresh,
	// reuse the dedicated transport instead of doing an SSH handshake per tick.
	pools := make([]*rpcClientPool, len(s.config.Nodes))
	closePools := func() {
		for i, pool := range pools {
			if pool != nil && pool.client != nil {
				_ = pool.client.Close()
			}
			pools[i] = nil
		}
	}
	defer closePools()
	s.runLinkSampler(ctx, func(ctx context.Context, index int) hubLinkObservation {
		if pools[index] == nil {
			pools[index] = newRPCPool(s.config.Nodes[index])
		}
		return fetchHubLinkObservation(ctx, s.config.Nodes, index, pools[index])
	}, closePools)
}

func fetchHubLinkObservation(ctx context.Context, nodes []hubNodeConfig, index int, pool *rpcClientPool) hubLinkObservation {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	response, err := pool.call(ctx, nodeRPCRequest{Version: nodeRPCVersion, Operation: rpcSnapshot, IncludeProcesses: true})
	snapshot := response.Snapshot
	observation := hubLinkObservation{node: index, sampledAt: snapshot.CollectedAt}
	if err == nil && snapshot.NetworkConnectionMode && (len(snapshot.NetworkConnections) > 0 || snapshot.NetworkProcessError == "") {
		observation.available = true
		observation.peers = hubPeerLinks(nodes, index, snapshot.NetworkConnections)
	}
	return observation
}

func mergeHubLinks(observations []hubLinkObservation) hubLinkGraph {
	type report struct {
		edge                hubLinkEdge
		sampled, unmeasured int
		at                  time.Time
	}
	reports := make(map[[2]int]report)
	var graph hubLinkGraph
	for _, observation := range observations {
		if !observation.available {
			continue
		}
		graph.sampled++
		for _, peer := range observation.peers {
			a, b := observation.node, peer.node
			ab, ba := peer.tx, peer.rx
			if a > b {
				a, b, ab, ba = b, a, ba, ab
			}
			pair := [2]int{a, b}
			unmeasured := peer.sockets - peer.sampled
			prior, seen := reports[pair]
			// The same bytes can appear at both endpoints. Use one report,
			// preferring more measured sockets, fewer gaps, then newer data.
			if seen && (prior.sampled > peer.sampled ||
				prior.sampled == peer.sampled && (prior.unmeasured < unmeasured ||
					prior.unmeasured == unmeasured && !observation.sampledAt.After(prior.at))) {
				continue
			}
			reports[pair] = report{
				hubLinkEdge{a, b, ab, ba, peer.sampled > 0, unmeasured > 0},
				peer.sampled, unmeasured, observation.sampledAt,
			}
		}
	}
	for _, report := range reports {
		edge := report.edge
		if !edge.known {
			graph.unmeasured++
			continue
		}
		if edge.ab == 0 && edge.ba == 0 {
			continue
		}
		graph.edges = append(graph.edges, edge)
	}
	sort.Slice(graph.edges, func(i, j int) bool {
		a, b := graph.edges[i], graph.edges[j]
		if a.ab+a.ba != b.ab+b.ba {
			return a.ab+a.ba > b.ab+b.ba
		}
		if a.a != b.a {
			return a.a < b.a
		}
		return a.b < b.b
	})
	return graph
}

func hubLinkIP(endpoint string) netip.Addr {
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		return netip.Addr{}
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.Zone() != "" {
		return netip.Addr{}
	}
	return ip
}

// A link requires an observed socket and an unambiguous match to a configured
// node IP. The monitoring SSH ports do not count as workload traffic.
func hubPeerLinks(nodes []hubNodeConfig, selected int, connections []networkConnectionInfo) []hubPeerLink {
	identities := make(map[netip.Addr]int, len(nodes))
	ports := make([]string, len(nodes))
	for index, node := range nodes {
		address := normalizeNodeAddress(node.Address)
		_, ports[index], _ = net.SplitHostPort(address)
		if ip := hubLinkIP(address); ip.IsValid() {
			if _, exists := identities[ip]; exists {
				identities[ip] = -1
			} else {
				identities[ip] = index
			}
		}
	}
	peers := make(map[int]hubPeerLink)
	for _, conn := range connections {
		if conn.State != "ESTABLISHED" && !strings.HasPrefix(conn.Protocol, "UDP") {
			continue
		}
		if ip := hubLinkIP(conn.Local); !ip.IsValid() {
			continue
		}
		index, ok := identities[hubLinkIP(conn.Remote)]
		if !ok || index < 0 || index == selected {
			continue
		}
		if strings.HasPrefix(conn.Protocol, "TCP") {
			_, localPort, _ := net.SplitHostPort(conn.Local)
			_, remotePort, _ := net.SplitHostPort(conn.Remote)
			if localPort == ports[selected] || remotePort == ports[index] {
				continue
			}
		}
		link := peers[index]
		link.node = index
		link.sockets++
		if conn.RateAvailable {
			link.sampled++
			link.rx += conn.RX
			link.tx += conn.TX
		}
		peers[index] = link
	}
	result := make([]hubPeerLink, 0, len(peers))
	for _, link := range peers {
		result = append(result, link)
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.rx+a.tx != b.rx+b.tx {
			return a.rx+a.tx > b.rx+b.tx
		}
		if (a.sampled > 0) != (b.sampled > 0) {
			return a.sampled > 0
		}
		return a.node < b.node
	})
	return result
}

func (m *hubModel) hubLinksPageSize() int {
	budget := m.hubRackBudget()
	if m.hubFramedMode() && !m.hubCompactLinks() {
		return max(1, budget-4)
	}
	if budget < 4 {
		return 1
	}
	if m.linksStacked() {
		return max(1, (budget-3)/2)
	}
	return budget - 3
}

func (m *hubModel) moveHubLinksPage(delta int) {
	last := max(0, len(m.links.edges)-1) / m.hubLinksPageSize()
	m.linksPage = max(0, min(last, min(m.linksPage, last)+delta))
}

func (m *hubModel) linksStacked() bool {
	return m.width < 36 && m.hubRackBudget() >= 5
}

const hubLinkRateWidth = 6 // ≥ plus up to four digits and a unit.

func hubLinkRateColumns(forward, reverse string, first, second lipgloss.Style) string {
	return first.Render(fixedCell(forward, hubLinkRateWidth, true)) + "  " +
		second.Render(fixedCell(reverse, hubLinkRateWidth, true))
}

func hubLinkRates(link hubLinkEdge) string {
	bound := ""
	if link.partial {
		bound = "≥"
	}
	style := func(rate uint64, direction lipgloss.Style) lipgloss.Style {
		if rate == 0 {
			return dimStyle
		}
		return direction
	}
	return hubLinkRateColumns(bound+hubCompactRate(link.ab), bound+hubCompactRate(link.ba),
		style(link.ab, networkRXStyle), style(link.ba, networkTXStyle))
}

func (m *hubModel) renderHubLinksContent(width, contentRows int) []string {
	switch {
	case m.links.sampledAt.IsZero():
		return []string{dimStyle.Render("Reading cluster socket connections…")}
	case m.links.eligible == 0:
		return []string{dimStyle.Render("Waiting for online nodes…")}
	case m.links.sampled == 0:
		return []string{dimStyle.Render("Connection data unavailable.")}
	}
	count := len(m.links.edges)
	if count == 0 {
		message := "No active inter-node traffic"
		if m.links.unmeasured > 0 {
			message = "Link rates unavailable"
		}
		return []string{dimStyle.Render(message)}
	}
	var lines []string
	headerLeft := "NODE A ↔ NODE B"
	lines = append(lines, hubRackAlign(
		processHeaderStyle.Render(headerLeft),
		hubLinkRateColumns("A → B", "B → A", networkRXStyle, networkTXStyle),
		width,
	))

	hasFooter := m.links.unmeasured > 0 || count > max(1, contentRows-1)
	pageSize := max(1, contentRows-1)
	if hasFooter && contentRows >= 2 {
		pageSize = max(1, contentRows-2)
	}
	page := min(m.linksPage, (count-1)/pageSize)
	first, last := page*pageSize, min((page+1)*pageSize, count)

	var maxRate uint64 = 1 << 20
	for _, link := range m.links.edges {
		if link.ab+link.ba > maxRate {
			maxRate = link.ab + link.ba
		}
	}

	for _, link := range m.links.edges[first:last] {
		rates := hubLinkRates(link)
		a, b := m.config.Nodes[link.a].Name, m.config.Nodes[link.b].Name
		room := max(6, width-lipgloss.Width(rates)-2)
		barStr := ""
		if room >= 24 {
			barCells := 5
			room -= barCells + 2
			pct := float64(link.ab+link.ba) / float64(maxRate) * 100
			_, barStr = hubCompactInlineBar(pct, barCells)
			barStr += "  "
		}
		aWidth := min(lipgloss.Width(a), max((room-3)/2, room-3-lipgloss.Width(b)))
		bWidth := max(1, room-3-aWidth)
		pair := valueStyle.Render(ansi.Truncate(a, aWidth, "…")) + dimStyle.Render(" ↔ ") +
			valueStyle.Render(ansi.Truncate(b, bWidth, "…"))
		lines = append(lines, hubRackAlign(pair, barStr+rates, width))
	}
	if hasFooter {
		hint, status := "", ""
		if hidden := m.links.unmeasured; hidden > 0 {
			hint = dimStyle.Render(fmt.Sprintf("%d unmeasured", hidden))
		}
		if pageSize < count {
			status = fmt.Sprintf("%d–%d / %d", first+1, last, count)
			if !m.queueExpanded {
				pageHint := keyHint("pg↑↓", "links")
				if hint == "" {
					hint = pageHint
				} else if lipgloss.Width(hint)+lipgloss.Width(pageHint)+len(status)+4 <= width {
					hint = pageHint + "  " + hint
				}
			}
		}
		if hint != "" || status != "" {
			lines = append(lines, hubRackAlign(hint, dimStyle.Render(status), width))
		}
	}
	return lines
}

func (m *hubModel) renderHubLinks(width int) []string {
	title := networkRXStyle.Render("CLUSTER LINKS")
	lines := []string{title}
	switch {
	case m.links.sampledAt.IsZero():
		return append(lines, dimStyle.Render("Reading cluster connections…"))
	case m.links.eligible == 0:
		return append(lines, dimStyle.Render("Waiting for online nodes…"))
	}
	lines[0] = hubRackAlign(title, dimStyle.Render(fmt.Sprintf("%d/%d observed", m.links.sampled, m.links.eligible)), width)
	if m.links.sampled == 0 {
		return append(lines, dimStyle.Render("Connection data unavailable."))
	}
	count := len(m.links.edges)
	if count == 0 {
		message := "No measured traffic"
		if m.links.unmeasured > 0 {
			message = "Link rates unavailable"
		}
		return append(lines, dimStyle.Render(message))
	}
	budget := m.hubRackBudget()
	if budget >= 4 {
		label := "A ↔ B · B/s"
		if width >= 38 {
			label = "NODE A ↔ NODE B · B/s"
		}
		lines = append(lines, hubRackAlign(dimStyle.Render(label),
			hubLinkRateColumns("A → B", "B → A", networkRXStyle, networkTXStyle), width))
	}
	pageSize := m.hubLinksPageSize()
	page := min(m.linksPage, (count-1)/pageSize)
	first, last := page*pageSize, min((page+1)*pageSize, count)
	if budget < 4 {
		label := fmt.Sprintf("%d/%d B/s", first+1, count)
		if width >= 36 {
			label = "LINKS " + label
		}
		lines[0] = hubRackAlign(networkRXStyle.Render(label),
			hubLinkRateColumns("A → B", "B → A", networkRXStyle, networkTXStyle), width)
	}
	for _, link := range m.links.edges[first:last] {
		rates := hubLinkRates(link)
		a, b := m.config.Nodes[link.a].Name, m.config.Nodes[link.b].Name
		stacked := m.linksStacked()
		room := width - 3
		if !stacked {
			room -= lipgloss.Width(rates) + 2
		}
		room = max(2, room)
		aWidth := min(lipgloss.Width(a), max(room/2, room-lipgloss.Width(b)))
		pair := valueStyle.Render(ansi.Truncate(a, aWidth, "…")) + dimStyle.Render(" ↔ ") +
			valueStyle.Render(ansi.Truncate(b, room-aWidth, "…"))
		if stacked {
			lines = append(lines, pair, hubRackAlign("", rates, width))
		} else {
			lines = append(lines, hubRackAlign(pair, rates, width))
		}
	}
	if budget >= 4 {
		hint, status := "", ""
		if hidden := m.links.unmeasured; hidden > 0 {
			hint = dimStyle.Render(fmt.Sprintf("%d unmeasured", hidden))
		}
		if pageSize < count {
			status = fmt.Sprintf("%d–%d / %d", first+1, last, count)
			if !m.queueExpanded {
				pageHint := keyHint("pg↑↓", "links")
				if hint == "" {
					hint = pageHint
				} else if lipgloss.Width(hint)+lipgloss.Width(pageHint)+len(status)+4 <= width {
					hint = pageHint + "  " + hint
				}
			}
		}
		if hint != "" || status != "" {
			lines = append(lines, hubRackAlign(hint, dimStyle.Render(status), width))
		}
	}
	return lines
}

const (
	hubLinkComputeSlots = 4
	hubLinkTotalSlots   = hubLinkComputeSlots + 2
)

type hubLinkPoll struct {
	next                   time.Time
	online, busy, priority bool
	failures               int
}

func hubLinkPriority(config hubConfig, node hubNodeConfig) bool {
	host, _, _ := net.SplitHostPort(normalizeNodeAddress(node.Address))
	if node.Profile == machineProfileNAS || strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback() {
		return true
	}
	for _, group := range config.Groups {
		if group.ID == node.Group {
			return group.Style == hubGroupStyleNetwork
		}
	}
	return false
}

// One shared scheduler follows the configured cadence per node, not per
// sweep. A slow request can keep only its own node busy; compute nodes cannot
// consume the two slots reserved for storage and other service nodes.
func (s *hubService) runLinkSampler(ctx context.Context, fetch func(context.Context, int) hubLinkObservation, idle func()) {
	interval := s.config.refreshInterval()
	polls := make([]hubLinkPoll, len(s.config.Nodes))
	observations := make([]hubLinkObservation, len(polls))
	for i, node := range s.config.Nodes {
		polls[i].priority = hubLinkPriority(s.config, node)
	}
	type result struct {
		index       int
		observation hubLinkObservation
	}
	results := make(chan result, hubLinkTotalSlots)
	var workers sync.WaitGroup
	defer workers.Wait()
	timer := time.NewTimer(interval)
	defer timer.Stop()
	active, compute := 0, 0
	dirty, haveResult := true, false
	canStart := func(poll hubLinkPoll) bool {
		return poll.online && !poll.busy && active < hubLinkTotalSlots &&
			(poll.priority || compute < hubLinkComputeSlots)
	}

	for ctx.Err() == nil {
		now := time.Now()
		eligible := 0
		s.mu.RLock()
		viewing := now.Before(s.linksViewUntil)
		for i, state := range s.states {
			online := state.Error == "" && !state.Snapshot.CollectedAt.IsZero()
			if online {
				eligible++
			}
			if polls[i].online != online {
				polls[i].online = online
				polls[i].next, polls[i].failures = time.Time{}, 0
				observations[i] = hubLinkObservation{}
				dirty = true
			}
		}
		s.mu.RUnlock()

		timer.Stop()
		var tick <-chan time.Time
		if viewing {
			if dirty && (haveResult || eligible == 0) {
				graph := mergeHubLinks(observations)
				graph.eligible, graph.sampledAt = eligible, now
				s.mu.Lock()
				s.links = graph
				s.mu.Unlock()
				dirty = false
			}
			for {
				best := -1
				for i, poll := range polls {
					if !canStart(poll) || now.Before(poll.next) {
						continue
					}
					if best < 0 || (poll.priority && !polls[best].priority) ||
						(poll.priority == polls[best].priority && poll.next.Before(polls[best].next)) {
						best = i
					}
				}
				if best < 0 {
					break
				}
				polls[best].busy, polls[best].next = true, now.Add(interval)
				active++
				if !polls[best].priority {
					compute++
				}
				workers.Go(func() {
					results <- result{best, fetch(ctx, best)}
				})
			}
			// Check for changes in online status even when all due nodes
			// are busy or have backed off.
			next := now.Add(interval)
			for _, poll := range polls {
				if canStart(poll) && poll.next.Before(next) {
					next = poll.next
				}
			}
			timer.Reset(time.Until(next))
			tick = timer.C
		} else if active == 0 {
			idle()
			clear(observations)
			haveResult, dirty = false, true
			s.mu.Lock()
			s.links = hubLinkGraph{}
			s.mu.Unlock()
		}

		select {
		case <-ctx.Done():
			return
		case <-s.linksWake:
		case <-tick:
		case sample := <-results:
			poll := &polls[sample.index]
			poll.busy = false
			active--
			if !poll.priority {
				compute--
			}
			if poll.online {
				observations[sample.index] = sample.observation
				if sample.observation.available {
					poll.failures = 0
				} else {
					poll.failures++
					delay := hubOfflineRetryDelay(poll.failures)
					if delay < interval {
						delay = interval
					}
					poll.next = time.Now().Add(delay)
				}
				haveResult, dirty = true, true
			}
		}
	}
}
