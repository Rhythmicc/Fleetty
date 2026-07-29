package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	panelLayoutVersion     = 2
	maxPanelLayoutFileSize = 64 << 10
)

type dashboardPanelID string

const (
	dashboardPanelOverview  dashboardPanelID = "overview" // legacy v1 group
	dashboardPanelCPU       dashboardPanelID = "cpu"
	dashboardPanelMemory    dashboardPanelID = "memory"
	dashboardPanelDisk      dashboardPanelID = "disk"
	dashboardPanelNetwork   dashboardPanelID = "network"
	dashboardPanelBattery   dashboardPanelID = "battery"
	dashboardPanelGPU       dashboardPanelID = "gpu"
	dashboardPanelNodeQueue dashboardPanelID = "node_queue"
	dashboardPanelProcesses dashboardPanelID = "processes"
)

type widgetSize string

const (
	widgetSizeSmall  widgetSize = "small"
	widgetSizeMedium widgetSize = "medium"
	widgetSizeLarge  widgetSize = "large"
)

type dashboardPanelDescriptor struct {
	ID          dashboardPanelID
	Label       string
	Description string
	DefaultSize widgetSize
	Available   func(*monitorModel) bool
}

var dashboardPanelRegistry = []dashboardPanelDescriptor{
	{
		ID: dashboardPanelCPU, Label: "CPU",
		Description: "Processor utilization, load, and recent history",
		DefaultSize: widgetSizeSmall, Available: func(*monitorModel) bool { return true },
	},
	{
		ID: dashboardPanelMemory, Label: "Memory",
		Description: "Memory utilization and available capacity",
		DefaultSize: widgetSizeSmall, Available: func(*monitorModel) bool { return true },
	},
	{
		ID: dashboardPanelDisk, Label: "Disk",
		Description: "Root filesystem utilization and capacity",
		DefaultSize: widgetSizeSmall, Available: func(*monitorModel) bool { return true },
	},
	{
		ID: dashboardPanelNetwork, Label: "Network",
		Description: "Receive/transmit rate, totals, and recent history",
		DefaultSize: widgetSizeMedium, Available: func(*monitorModel) bool { return true },
	},
	{
		ID: dashboardPanelBattery, Label: "Battery",
		Description: "Charge, power source, status, and remaining time",
		DefaultSize: widgetSizeSmall,
		Available:   func(m *monitorModel) bool { return m.snapshot.Battery != nil },
	},
	{
		ID: dashboardPanelGPU, Label: "GPU",
		Description: "GPU load, memory, engines, and hardware telemetry",
		DefaultSize: widgetSizeLarge,
		Available: func(m *monitorModel) bool {
			return m.profile == machineProfileGPU || m.snapshot.Profile == machineProfileGPU ||
				len(m.snapshot.GPUs) > 0
		},
	},
	{
		ID: dashboardPanelNodeQueue, Label: "Node queue",
		Description: "Slurm jobs assigned or eligible for this node",
		DefaultSize: widgetSizeLarge, Available: func(m *monitorModel) bool { return m.slurmQueue != nil },
	},
	{
		ID: dashboardPanelProcesses, Label: "Processes",
		Description: "Read-only process list and details",
		DefaultSize: widgetSizeLarge, Available: func(*monitorModel) bool { return true },
	},
}

type dashboardPanelPreference struct {
	ID        dashboardPanelID `json:"id"`
	Collapsed bool             `json:"collapsed,omitempty"`
	Size      widgetSize       `json:"size,omitempty"`
}

type panelLayoutConfig struct {
	Version int                        `json:"version"`
	Panels  []dashboardPanelPreference `json:"panels"`
}

func defaultPanelLayout() panelLayoutConfig {
	panels := make([]dashboardPanelPreference, 0, len(dashboardPanelRegistry))
	for _, descriptor := range dashboardPanelRegistry {
		panels = append(panels, dashboardPanelPreference{ID: descriptor.ID, Size: descriptor.DefaultSize})
	}
	return panelLayoutConfig{Version: panelLayoutVersion, Panels: panels}
}

func normalizePanelLayout(layout panelLayoutConfig) panelLayoutConfig {
	layout = migrateLegacyPanelLayout(layout)
	known := make(map[dashboardPanelID]struct{}, len(dashboardPanelRegistry))
	for _, descriptor := range dashboardPanelRegistry {
		known[descriptor.ID] = struct{}{}
	}
	normalized := panelLayoutConfig{Version: panelLayoutVersion}
	seen := make(map[dashboardPanelID]struct{}, len(known))
	for _, panel := range layout.Panels {
		if _, ok := known[panel.ID]; !ok {
			continue
		}
		if _, duplicate := seen[panel.ID]; duplicate {
			continue
		}
		descriptor, _ := dashboardPanelByID(panel.ID)
		panel.Size = normalizeWidgetSize(panel.Size, descriptor.DefaultSize)
		normalized.Panels = append(normalized.Panels, panel)
		seen[panel.ID] = struct{}{}
	}
	for _, descriptor := range dashboardPanelRegistry {
		if _, ok := seen[descriptor.ID]; !ok {
			normalized.Panels = append(normalized.Panels, dashboardPanelPreference{
				ID: descriptor.ID, Size: descriptor.DefaultSize,
			})
		}
	}
	return normalized
}

func migrateLegacyPanelLayout(layout panelLayoutConfig) panelLayoutConfig {
	if len(layout.Panels) == 0 {
		return defaultPanelLayout()
	}
	var migrated []dashboardPanelPreference
	for _, panel := range layout.Panels {
		if panel.ID != dashboardPanelOverview {
			migrated = append(migrated, panel)
			continue
		}
		for _, id := range []dashboardPanelID{
			dashboardPanelCPU, dashboardPanelMemory, dashboardPanelDisk,
			dashboardPanelNetwork, dashboardPanelBattery,
		} {
			descriptor, _ := dashboardPanelByID(id)
			migrated = append(migrated, dashboardPanelPreference{
				ID: id, Collapsed: panel.Collapsed, Size: descriptor.DefaultSize,
			})
		}
	}
	layout.Version = panelLayoutVersion
	layout.Panels = migrated
	return layout
}

func normalizeWidgetSize(size, fallback widgetSize) widgetSize {
	switch size {
	case widgetSizeSmall, widgetSizeMedium, widgetSizeLarge:
		return size
	default:
		return fallback
	}
}

func dashboardPanelByID(id dashboardPanelID) (dashboardPanelDescriptor, bool) {
	for _, descriptor := range dashboardPanelRegistry {
		if descriptor.ID == id {
			return descriptor, true
		}
	}
	return dashboardPanelDescriptor{}, false
}

func (m *monitorModel) ensurePanelLayout() {
	m.panelLayout = normalizePanelLayout(m.panelLayout)
	if m.layoutCursor >= len(m.panelLayout.Panels) {
		m.layoutCursor = max(0, len(m.panelLayout.Panels)-1)
	}
}

func (m *monitorModel) orderedDashboardPanels() []dashboardPanelPreference {
	m.ensurePanelLayout()
	result := make([]dashboardPanelPreference, 0, len(m.panelLayout.Panels))
	for _, panel := range m.panelLayout.Panels {
		descriptor, ok := dashboardPanelByID(panel.ID)
		if ok && descriptor.Available(m) {
			result = append(result, panel)
		}
	}
	return result
}

func (m *monitorModel) dashboardPanelCollapsed(id dashboardPanelID) bool {
	m.ensurePanelLayout()
	for _, panel := range m.panelLayout.Panels {
		if panel.ID == id {
			return panel.Collapsed
		}
	}
	return false
}

func (m *monitorModel) dashboardPanelSize(id dashboardPanelID) widgetSize {
	m.ensurePanelLayout()
	for _, panel := range m.panelLayout.Panels {
		if panel.ID == id {
			return panel.Size
		}
	}
	descriptor, ok := dashboardPanelByID(id)
	if ok {
		return descriptor.DefaultSize
	}
	return widgetSizeSmall
}

func (m *monitorModel) setDashboardPanelCollapsed(id dashboardPanelID, collapsed bool) {
	m.ensurePanelLayout()
	for index := range m.panelLayout.Panels {
		if m.panelLayout.Panels[index].ID == id {
			m.panelLayout.Panels[index].Collapsed = collapsed
			return
		}
	}
}

func (m *monitorModel) dashboardPanelAvailable(id dashboardPanelID) bool {
	descriptor, ok := dashboardPanelByID(id)
	return ok && descriptor.Available(m)
}

func (m *monitorModel) selectedLayoutPanel() *dashboardPanelPreference {
	m.ensurePanelLayout()
	if m.layoutCursor < 0 || m.layoutCursor >= len(m.panelLayout.Panels) {
		return nil
	}
	return &m.panelLayout.Panels[m.layoutCursor]
}

func (m *monitorModel) moveSelectedLayoutPanel(delta int) {
	m.ensurePanelLayout()
	target := m.layoutCursor + delta
	if target < 0 || target >= len(m.panelLayout.Panels) {
		m.status = "Panel is already at the layout boundary."
		return
	}
	m.panelLayout.Panels[m.layoutCursor], m.panelLayout.Panels[target] =
		m.panelLayout.Panels[target], m.panelLayout.Panels[m.layoutCursor]
	m.layoutCursor = target
	m.status = "Panel order updated for this session."
}

func (m *monitorModel) toggleSelectedLayoutPanel() {
	panel := m.selectedLayoutPanel()
	if panel == nil {
		return
	}
	panel.Collapsed = !panel.Collapsed
	state := "visible"
	if panel.Collapsed {
		state = "hidden"
	}
	descriptor, _ := dashboardPanelByID(panel.ID)
	m.status = fmt.Sprintf("%s %s.", descriptor.Label, state)
	if panel.ID == dashboardPanelNodeQueue && panel.Collapsed {
		m.monitorFocus = monitorFocusProcesses
	}
	if panel.ID == dashboardPanelProcesses && panel.Collapsed &&
		m.dashboardPanelAvailable(dashboardPanelNodeQueue) &&
		!m.dashboardPanelCollapsed(dashboardPanelNodeQueue) {
		m.monitorFocus = monitorFocusQueue
	}
}

func (m *monitorModel) resizeSelectedLayoutPanel(delta int) {
	panel := m.selectedLayoutPanel()
	if panel == nil {
		return
	}
	m.resizeWidgetPreference(panel, delta)
}

func (m *monitorModel) resizeDashboardWidget(id dashboardPanelID, delta int) {
	m.ensurePanelLayout()
	for index := range m.panelLayout.Panels {
		if m.panelLayout.Panels[index].ID == id {
			m.resizeWidgetPreference(&m.panelLayout.Panels[index], delta)
			return
		}
	}
}

func (m *monitorModel) resizeWidgetPreference(panel *dashboardPanelPreference, delta int) {
	sizes := []widgetSize{widgetSizeSmall, widgetSizeMedium, widgetSizeLarge}
	current := 0
	for index, size := range sizes {
		if panel.Size == size {
			current = index
			break
		}
	}
	target := min(max(0, current+delta), len(sizes)-1)
	if target == current {
		m.status = "Widget size limit reached."
		return
	}
	panel.Size = sizes[target]
	descriptor, _ := dashboardPanelByID(panel.ID)
	m.status = fmt.Sprintf("%s size: %s.", descriptor.Label, strings.ToUpper(string(panel.Size)))
}

func (m *monitorModel) resetPanelLayout() {
	m.panelLayout = defaultPanelLayout()
	m.layoutCursor = 0
	m.status = "Default widget order, sizes, and visibility restored for this session."
}

type layoutEditorButton struct {
	key, label string
	action     func(*monitorModel)
}

var layoutEditorButtons = []layoutEditorButton{
	{key: "K", label: "move up", action: func(m *monitorModel) { m.moveSelectedLayoutPanel(-1) }},
	{key: "J", label: "move down", action: func(m *monitorModel) { m.moveSelectedLayoutPanel(1) }},
	{key: "-", label: "smaller", action: func(m *monitorModel) { m.resizeSelectedLayoutPanel(-1) }},
	{key: "+", label: "larger", action: func(m *monitorModel) { m.resizeSelectedLayoutPanel(1) }},
	{key: "space", label: "hide", action: func(m *monitorModel) { m.toggleSelectedLayoutPanel() }},
	{key: "s", label: "save", action: func(m *monitorModel) {
		if err := savePanelLayout(m.layoutPath, m.panelLayout); err != nil {
			m.status = "Could not save layout: " + err.Error()
		} else {
			m.status = "Layout saved as the local user's default."
		}
	}},
	{key: "r", label: "reset", action: func(m *monitorModel) { m.resetPanelLayout() }},
}

func (m *monitorModel) layoutView() string {
	m.ensurePanelLayout()
	width := usableWidth(m.width)
	header := dashboardHeaderNamed("LAYOUT", width, m.snapshot.CollectedAt, m.colorMode, m.nodeName)
	buttons := make([]string, 0, len(layoutEditorButtons)*2)
	for index, button := range layoutEditorButtons {
		if index > 0 {
			buttons = append(buttons, " ")
		}
		buttons = append(buttons, compactButton(button.key, button.label, false))
	}
	actionLine := lipgloss.JoinHorizontal(lipgloss.Top, buttons...)
	actionLine = truncateANSI(actionLine, width)

	rows := make([]string, 0, len(m.panelLayout.Panels))
	contentWidth := max(12, width-4)
	for index, panel := range m.panelLayout.Panels {
		descriptor, ok := dashboardPanelByID(panel.ID)
		if !ok {
			continue
		}
		available := descriptor.Available(m)
		state := processRunningStyle.Render("● VISIBLE")
		if !available {
			state = processIdleStyle.Render("○ AUTO HIDDEN")
		} else if panel.Collapsed {
			state = processWaitingStyle.Render("◆ HIDDEN")
		}
		labelWidth := min(22, max(12, contentWidth/4))
		prefix := fmt.Sprintf("%d  %-*s", index+1, labelWidth, strings.ToUpper(descriptor.Label))
		size := gpuTitleStyle.Render(strings.ToUpper(string(panel.Size)))
		line := accentStyle.Render(prefix) + "  " + size + "  " + state
		remaining := contentWidth - lipgloss.Width(line) - 3
		if remaining > 12 {
			line += dimStyle.Render("  ·  " + truncate(descriptor.Description, remaining))
		}
		line = truncateANSI(line, contentWidth)
		if index == m.layoutCursor {
			line = selectedRowStyle.Width(contentWidth).Render(line)
		}
		rows = append(rows, line)
	}
	panel := btopPanel(width, "DASHBOARD PANELS", "SESSION LOCAL", strings.Join(rows, "\n"), processTitleStyle, colorProcessBorder)
	footer := renderLayoutFooter(width, m.status, m.layoutPath != "")

	m.layoutButtonY = lipgloss.Height(header)
	m.layoutFirstRowY = m.layoutButtonY + lipgloss.Height(actionLine) + 1
	return strings.Join([]string{header, actionLine, panel, footer}, "\n")
}

func renderLayoutFooter(width int, status string, canSave bool) string {
	saveHint := "save unavailable over SSH"
	if canSave {
		saveHint = "s saves for this local user"
	}
	hint := "[↑↓] select  [K/J] reorder  [-/+] size  [space] hide  [esc] apply  ·  " + saveHint
	if strings.TrimSpace(status) != "" && width >= 90 {
		hint += "  ·  " + status
	}
	return dimStyle.Render(truncate(hint, width))
}

func (m *monitorModel) handleLayoutButtonClick(x int) {
	left := 0
	for _, button := range layoutEditorButtons {
		buttonWidth := compactButtonWidth(button.key, button.label)
		if x >= left && x < left+buttonWidth {
			button.action(m)
			return
		}
		left += buttonWidth + 1
	}
}

func truncateANSI(value string, width int) string {
	return ansi.Truncate(value, max(1, width), "")
}

func resolvePanelLayoutPath(requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		requested = strings.TrimSpace(os.Getenv("FLEETTY_LAYOUT_FILE"))
	}
	if requested == "" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("resolve user configuration directory: %w", err)
		}
		requested = filepath.Join(configDir, "fleetty", "layout.json")
	}
	absolute, err := filepath.Abs(requested)
	if err != nil {
		return "", fmt.Errorf("resolve layout path: %w", err)
	}
	return absolute, nil
}

func loadPanelLayout(path string) (panelLayoutConfig, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaultPanelLayout(), nil
	}
	if err != nil {
		return defaultPanelLayout(), fmt.Errorf("read layout: %w", err)
	}
	if len(data) > maxPanelLayoutFileSize {
		return defaultPanelLayout(), errors.New("layout file is too large")
	}
	var layout panelLayoutConfig
	if err := json.Unmarshal(data, &layout); err != nil {
		return defaultPanelLayout(), fmt.Errorf("parse layout: %w", err)
	}
	if layout.Version < 0 || layout.Version > panelLayoutVersion {
		return defaultPanelLayout(), fmt.Errorf("unsupported layout version %d", layout.Version)
	}
	return normalizePanelLayout(layout), nil
}

func savePanelLayout(path string, layout panelLayoutConfig) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("layout persistence is unavailable for this session")
	}
	layout = normalizePanelLayout(layout)
	data, err := json.MarshalIndent(layout, "", "  ")
	if err != nil {
		return fmt.Errorf("encode layout: %w", err)
	}
	data = append(data, '\n')
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create layout directory: %w", err)
	}
	temp, err := os.CreateTemp(parent, ".layout-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary layout: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("secure temporary layout: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write layout: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync layout: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close layout: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace layout: %w", err)
	}
	return nil
}
