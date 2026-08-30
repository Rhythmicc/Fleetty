package main

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
)

type processSortKey int

const (
	processSortCPU processSortKey = iota
	processSortMemory
	processSortRSS
	processSortElapsed
	processSortPID
	processSortUser
	processSortCommand
	processSortCount
	processRank
)

func (key processSortKey) label() string {
	switch key {
	case processSortMemory:
		return "MEM"
	case processSortRSS:
		return "RSS"
	case processSortElapsed:
		return "ELAPSED"
	case processSortPID:
		return "PID"
	case processSortUser:
		return "USER"
	case processSortCommand:
		return "COMMAND"
	case processRank:
		return "#"
	default:
		return "CPU"
	}
}

// Sort a copy: Hub sessions share snapshots, but each user's ordering is private.
func (m *monitorModel) filteredProcesses() []processInfo {
	query := strings.ToLower(strings.TrimSpace(m.filter))
	processes := make([]processInfo, 0, len(m.snapshot.Processes))
	for _, p := range m.snapshot.Processes {
		if query == "" || strings.Contains(strings.ToLower(fmt.Sprintf("%d %s %s", p.PID, p.User, p.Command)), query) {
			processes = append(processes, p)
		}
	}
	slices.SortFunc(processes, func(a, b processInfo) int {
		order := 0
		switch m.processSort {
		case processSortMemory:
			order = cmp.Compare(a.Memory, b.Memory)
		case processSortRSS:
			order = cmp.Compare(a.RSS, b.RSS)
		case processSortElapsed:
			order = cmp.Compare(a.Elapsed, b.Elapsed)
		case processSortPID:
			order = cmp.Compare(a.PID, b.PID)
		case processSortUser:
			order = strings.Compare(strings.ToLower(a.User), strings.ToLower(b.User))
		case processSortCommand:
			order = strings.Compare(strings.ToLower(a.Command), strings.ToLower(b.Command))
		default:
			order = cmp.Compare(a.CPU, b.CPU)
		}
		if !m.processSortAsc {
			order = -order
		}
		if order == 0 {
			return cmp.Compare(a.PID, b.PID)
		}
		return order
	})
	return processes
}

func (m *monitorModel) selectedProcessIDs() (monitor, admin int) {
	processes := m.filteredProcesses()
	if m.monitorCursor >= 0 && m.monitorCursor < len(processes) {
		monitor = processes[m.monitorCursor].PID
	}
	if m.cursor >= 0 && m.cursor < len(processes) {
		admin = processes[m.cursor].PID
	}
	return
}

func (m *monitorModel) restoreProcessSelection(monitorPID, adminPID int) {
	for index, p := range m.filteredProcesses() {
		if p.PID == monitorPID {
			m.monitorCursor = index
		}
		if p.PID == adminPID {
			m.cursor = index
		}
	}
	m.clampProcessCursor()
	m.clampMonitorProcessCursor(m.monitorRows)
}

func (m *monitorModel) setProcessSort(key processSortKey) {
	monitorPID, adminPID := m.selectedProcessIDs()
	if key == m.processSort {
		m.processSortAsc = !m.processSortAsc
	} else {
		m.processSort = key
		m.processSortAsc = key == processSortPID || key == processSortUser || key == processSortCommand
	}
	m.restoreProcessSelection(monitorPID, adminPID)
	m.status = "Sort " + m.processSortLabel() + ". Click a header or press o to change; O reverses. CPU 100% = one core."
}

func (m *monitorModel) processSortLabel() string {
	arrow := "↓"
	if m.processSortAsc {
		arrow = "↑"
	}
	return m.processSort.label() + " " + arrow
}

func (m *monitorModel) processTableMeta(offset, rows, width int, title string) string {
	count, total := len(m.filteredProcesses()), len(m.snapshot.Processes)
	start, end := min(offset+1, count), min(offset+rows, count)
	meta := fmt.Sprintf("%s · %d–%d / %d", m.processSortLabel(), start, end, count)
	if m.filter != "" {
		meta += fmt.Sprintf(" MATCHED · %d TOTAL", total)
		query := " · FILTER " + truncate(m.filter, 16)
		if lipgloss.Width(meta+query)+lipgloss.Width(title)+6 <= width {
			meta += query
		}
	}
	if lipgloss.Width(meta)+lipgloss.Width(title)+6 > width {
		meta = fmt.Sprintf("%d–%d/%d", start, end, count)
		if m.filter != "" {
			meta += " MATCH"
		}
	}
	return meta
}

type processColumn struct {
	key   processSortKey
	width int
	right bool
}

type processFormat struct {
	mode    int
	columns []processColumn
}

const (
	processFull = iota
	processMedium
	processCompact
)

// Header, rows and mouse hit boxes all use these same terminal-cell widths.
func newProcessFormat(width int) processFormat {
	f := processFormat{mode: processCompact}
	f.columns = []processColumn{{processRank, 4, true}, {processSortPID, 9, false}}
	if width >= 78 {
		f.mode = processMedium
		f.columns[1].width = 11 // Includes the state marker before the PID.
		f.columns = append(f.columns, processColumn{processSortUser, 13, false})
	}
	cpuWidth := 6
	if f.mode != processCompact {
		cpuWidth = 8 // Preserve multi-core CPU values such as 1200.0%.
	}
	f.columns = append(f.columns, processColumn{processSortCPU, cpuWidth, true}, processColumn{processSortMemory, 6, true})
	if f.mode != processCompact {
		f.columns = append(f.columns, processColumn{processSortRSS, 10, false})
	}
	if width >= 112 {
		f.mode = processFull
		f.columns = append(f.columns, processColumn{processSortElapsed, 9, false})
	}
	used := len(f.columns) // One space between columns, including COMMAND.
	for _, column := range f.columns {
		used += column.width
	}
	f.columns = append(f.columns, processColumn{processSortCommand, max(1, width-4-used), false})
	return f
}

func (f processFormat) header() string {
	parts := make([]string, 0, len(f.columns))
	for _, column := range f.columns {
		label := column.key.label()
		if column.key == processSortPID {
			label = "  " + label
		}
		parts = append(parts, fixedCell(label, column.width, column.right))
	}
	return strings.Join(parts, " ")
}

func (f processFormat) renderHeader(sortKey processSortKey) string {
	parts := make([]string, 0, len(f.columns))
	for _, column := range f.columns {
		label := column.key.label()
		if column.key == processSortPID {
			label = "  " + label
		}
		style := processHeaderStyle
		if column.key == sortKey {
			style = style.Underline(true)
		}
		parts = append(parts, style.Render(fixedCell(label, column.width, column.right)))
	}
	return strings.Join(parts, processHeaderStyle.Render(" "))
}

func (f processFormat) sortKeyAt(x int) (processSortKey, bool) {
	left := 0
	for _, column := range f.columns {
		if x >= left && x < left+column.width {
			return column.key, column.key != processRank
		}
		left += column.width + 1
	}
	return 0, false
}

func processStateMarker(state string) string {
	if state == "" {
		return "?"
	}
	switch state[0] {
	case 'R':
		return "▶"
	case 'S':
		return "·"
	case 'D':
		return "!"
	case 'T', 't':
		return "■"
	case 'Z':
		return "×"
	case 'I':
		return "○"
	case 'X', 'x':
		return "×"
	default:
		return "?"
	}
}

func processColumnValue(key processSortKey, p processInfo, rank int) string {
	switch key {
	case processRank:
		return strconv.Itoa(rank)
	case processSortPID:
		return processStateMarker(p.State) + " " + strconv.Itoa(p.PID)
	case processSortUser:
		return p.User
	case processSortCPU:
		return fmt.Sprintf("%.1f%%", p.CPU)
	case processSortMemory:
		return fmt.Sprintf("%.1f%%", p.Memory)
	case processSortRSS:
		return bytes(p.RSS)
	case processSortElapsed:
		return elapsed(p.Elapsed)
	default:
		return p.Command
	}
}

// row is the unstyled representation used for column/layout checks.
func (f processFormat) row(p processInfo) string {
	parts := make([]string, 0, len(f.columns))
	for _, column := range f.columns {
		parts = append(parts, fixedCell(processColumnValue(column.key, p, 1), column.width, column.right))
	}
	return strings.Join(parts, " ")
}

func processLoadStyle(value float64, memory bool) lipgloss.Style {
	low, medium, high := 10.0, 50.0, 90.0
	if memory {
		low, medium, high = 5, 15, 30
	}
	switch {
	case value < 0.1:
		return gpuIdleStyle
	case value < low:
		return gpuLightStyle
	case value < medium:
		return gpuActiveStyle
	case value < high:
		return gpuBusyStyle
	default:
		return gpuMaxStyle
	}
}

func (f processFormat) renderRow(p processInfo, rank int, selected bool, mode colorMode, memoryTotal uint64) string {
	base := processDefaultStyle
	if selected {
		base = selectedProcessStyle(mode)
	}
	paint := func(text string, style lipgloss.Style) string {
		if selected {
			style = style.Background(base.GetBackground()).Bold(true)
		}
		return style.Render(text)
	}
	parts := make([]string, 0, len(f.columns))
	for _, column := range f.columns {
		text := fixedCell(processColumnValue(column.key, p, rank), column.width, column.right)
		style := base
		switch column.key {
		case processRank:
			if !selected {
				style = dimStyle
			}
		case processSortCPU:
			style = processLoadStyle(p.CPU, false)
		case processSortMemory:
			style = processLoadStyle(p.Memory, true)
		case processSortRSS:
			style = processLoadStyle(percent(p.RSS, memoryTotal), true)
		case processSortPID:
			// Only the single marker encodes state; the PID stays neutral.
			parts = append(parts, paint(processStateMarker(p.State), processStateStyle(p.State))+
				paint(" "+fixedCell(strconv.Itoa(p.PID), column.width-2, false), base))
			continue
		}
		parts = append(parts, paint(text, style))
	}
	return strings.Join(parts, base.Render(" "))
}

func (m *monitorModel) sortProcessHeaderAt(x, width int) {
	if key, ok := newProcessFormat(width).sortKeyAt(x); ok {
		m.setProcessSort(key)
	}
}
