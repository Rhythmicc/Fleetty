package main

import (
	"fmt"
	"image/color"
	"math"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// A theme token is one semantic color. Styles always render the dark value.
// Light mode rewrites those exact RGB sequences in the finished view, so
// concurrent SSH sessions can use different themes without mutating the
// shared styles.
type themeToken struct {
	dark, light string
}

var (
	tokenText     = themeToken{"#F5F7FF", "#172033"}
	tokenMuted    = themeToken{"#9CA3BC", "#5B6475"}
	tokenAccent   = themeToken{"#B9A4FF", "#5A3FA3"}
	tokenOK       = themeToken{"#9EE493", "#1F6F50"}
	tokenWarn     = themeToken{"#FFD180", "#8A5A00"}
	tokenCrit     = themeToken{"#FF8A80", "#B42318"}
	tokenBorder   = themeToken{"#40516B", "#C4CBD4"}
	tokenTrack    = themeToken{"#343C49", "#D5D9DF"}
	tokenOnAccent = themeToken{"#10131A", "#F8FAFC"}
	tokenSurface  = themeToken{"#24283B", "#E4E9F0"}
	tokenButton   = themeToken{"#30374A", "#D9E0EA"}
	tokenCritText = themeToken{"#FFF4F2", "#8F1D18"}
	tokenCritWash = themeToken{"#5A3037", "#F4D8D5"}
	tokenCanvas   = themeToken{"#0E1117", "#F4F1EA"}
)

// stateThresholds is the only place a reading becomes ok, warn, or crit.
// Percent metrics share one pair. Temperature, battery, and restart counts
// use the same three colors with cutoffs in their own units.
var stateThresholds = struct {
	Warn, Crit               float64
	TempWarn, TempCrit       int
	BatteryWarn, BatteryCrit float64
	RestartWarn, RestartCrit int
}{
	Warn: 70, Crit: 90,
	TempWarn: 75, TempCrit: 85,
	BatteryWarn: 30, BatteryCrit: 15,
	RestartWarn: 1, RestartCrit: 5,
}

type stateLevel int

const (
	stateOK stateLevel = iota
	stateWarn
	stateCrit
	stateOffline
	stateUnknown
)

func usageLevel(value float64) stateLevel {
	switch {
	case math.IsNaN(value) || math.IsInf(value, 0):
		return stateUnknown
	case value >= stateThresholds.Crit:
		return stateCrit
	case value >= stateThresholds.Warn:
		return stateWarn
	default:
		return stateOK
	}
}

func temperatureLevel(celsius int) stateLevel {
	switch {
	case celsius <= 0:
		return stateUnknown
	case celsius >= stateThresholds.TempCrit:
		return stateCrit
	case celsius >= stateThresholds.TempWarn:
		return stateWarn
	default:
		return stateOK
	}
}

func batteryLevel(percent float64) stateLevel {
	switch {
	case percent <= stateThresholds.BatteryCrit:
		return stateCrit
	case percent <= stateThresholds.BatteryWarn:
		return stateWarn
	default:
		return stateOK
	}
}

var (
	colorText        = lipgloss.Color(tokenText.dark)
	colorMuted       = lipgloss.Color(tokenMuted.dark)
	colorAccent      = lipgloss.Color(tokenAccent.dark)
	colorOK          = lipgloss.Color(tokenOK.dark)
	colorWarn        = lipgloss.Color(tokenWarn.dark)
	colorCrit        = lipgloss.Color(tokenCrit.dark)
	colorBorder      = lipgloss.Color(tokenBorder.dark)
	colorBorderFocus = colorAccent
	colorTrack       = lipgloss.Color(tokenTrack.dark)
	colorOnAccent    = lipgloss.Color(tokenOnAccent.dark)
	colorSurface     = lipgloss.Color(tokenSurface.dark)
	colorButton      = lipgloss.Color(tokenButton.dark)
	colorCritText    = lipgloss.Color(tokenCritText.dark)
	colorCritWash    = lipgloss.Color(tokenCritWash.dark)

	// Every non-focused panel uses the same border. The old per-metric
	// border colors remain as names so call sites stay stable.
	colorPanelBorder   = colorBorder
	colorCPUBorder     = colorBorder
	colorMemoryBorder  = colorBorder
	colorDiskBorder    = colorBorder
	colorNetworkBorder = colorBorder
	colorBatteryBorder = colorBorder
	colorGPUBorder     = colorBorder
	colorProcessBorder = colorBorder

	titleStyle     = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	sectionStyle   = lipgloss.NewStyle().Foreground(colorMuted)
	valueStyle     = lipgloss.NewStyle().Foreground(colorText).Bold(true)
	hubMetricStyle = lipgloss.NewStyle().Foreground(colorText)
	chartStyle     = hubMetricStyle
	barTrackStyle  = lipgloss.NewStyle().Foreground(colorTrack)
	accentStyle    = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	okStyle        = lipgloss.NewStyle().Foreground(colorOK).Bold(true)
	dangerStyle    = lipgloss.NewStyle().Foreground(colorCrit).Bold(true)
	dimStyle       = lipgloss.NewStyle().Foreground(colorMuted)
	warningStyle   = lipgloss.NewStyle().Foreground(colorWarn).Bold(true)

	cpuTitleStyle     = sectionStyle
	memoryTitleStyle  = sectionStyle
	diskTitleStyle    = sectionStyle
	networkTitleStyle = sectionStyle
	batteryTitleStyle = sectionStyle
	gpuTitleStyle     = sectionStyle
	processTitleStyle = sectionStyle

	helpStyle      = dimStyle
	clockStyle     = dimStyle
	liveBadgeStyle = lipgloss.NewStyle().Foreground(colorOnAccent).Background(colorOK).Bold(true).Padding(0, 1)
	warnBadgeStyle = lipgloss.NewStyle().Foreground(colorOnAccent).Background(colorWarn).Bold(true).Padding(0, 1)
	critBadgeStyle = lipgloss.NewStyle().Foreground(colorOnAccent).Background(colorCrit).Bold(true).Padding(0, 1)
	keycapStyle    = lipgloss.NewStyle().Foreground(colorOnAccent).Background(colorAccent).Bold(true).Padding(0, 1)
	hintLabelStyle = dimStyle
	// Column titles stay quiet. A filled header competed with the numbers.
	processHeaderStyle = lipgloss.NewStyle().Foreground(colorMuted).Bold(true)
	inputStyle         = lipgloss.NewStyle().Foreground(colorText).Background(colorSurface).Padding(0, 1)
	// Direction is the arrow, not a second hue.
	networkRXStyle = valueStyle
	networkTXStyle = valueStyle

	processRunningStyle  = okStyle
	processSleepingStyle = dimStyle
	processWaitingStyle  = warningStyle
	processStoppedStyle  = warningStyle
	processZombieStyle   = dangerStyle
	processIdleStyle     = dimStyle
	processDeadStyle     = dangerStyle
	processDefaultStyle  = hubMetricStyle

	gpuIdleStyle   = okStyle
	gpuLightStyle  = okStyle
	gpuActiveStyle = okStyle
	gpuBusyStyle   = warningStyle
	gpuHighStyle   = dangerStyle
	gpuMaxStyle    = dangerStyle

	compactButtonStyle       = lipgloss.NewStyle().Foreground(colorText).Background(colorButton).Padding(0, 1)
	compactDangerButtonStyle = lipgloss.NewStyle().Foreground(colorCritText).Background(colorCritWash).Padding(0, 1)
)

func stateStyle(level stateLevel) lipgloss.Style {
	switch level {
	case stateWarn:
		return warningStyle
	case stateCrit, stateOffline:
		return dangerStyle
	case stateUnknown:
		return dimStyle
	default:
		return okStyle
	}
}

// usageReading keeps a healthy number in neutral text. Warn and crit take
// the shared state color, which is still a bright foreground.
func usageReading(value float64, text string) string {
	if usageLevel(value) == stateOK {
		return valueStyle.Render(text)
	}
	return stateStyle(usageLevel(value)).Render(text)
}

func temperatureReading(celsius int, text string) string {
	return stateStyle(temperatureLevel(celsius)).Render(text)
}

// hubMeterStyle colors a hub reading. Healthy is the ok green, then yellow
// and red. Unknown stays dim. Focus does not change the scale.
func hubMeterStyle(level stateLevel) lipgloss.Style {
	switch level {
	case stateWarn:
		return warningStyle
	case stateCrit, stateOffline:
		return dangerStyle
	case stateUnknown:
		return dimStyle
	default:
		return okStyle
	}
}

// labeledMeter is one thin hub row: "CPU ━━━━━──  62%".
// The empty portion is a faint rule, not a filled block.
func labeledMeter(label string, value float64, width int) string {
	if width < 1 {
		return ""
	}
	if value < 0 {
		value = 0
	}
	if value > 100 {
		value = 100
	}
	name := dimStyle.Render(label)
	level := usageLevel(value)
	style := hubMeterStyle(level)
	percentText := style.Render(fmt.Sprintf("%3.0f%%", value))
	barWidth := width - lipgloss.Width(name) - 1 - 1 - lipgloss.Width(percentText)
	if barWidth < 1 {
		return ansi.Truncate(name+" "+percentText, width, "")
	}
	filled := int(value / 100 * float64(barWidth))
	filled = max(0, min(barWidth, filled))
	bar := style.Render(strings.Repeat("━", filled)) + dimStyle.Render(strings.Repeat("─", barWidth-filled))
	return ansi.Truncate(name+" "+bar+" "+percentText, width, "")
}

// selectedProcessStyle uses explicit xterm indexes. A truecolor accent wash
// is approximated differently by terminals that only advertise 256 colors,
// and the load colors painted on top of the row need a stable background.
// The accent itself is the focused border and the selected hub title.
func selectedProcessStyle(mode colorMode) lipgloss.Style {
	foreground := lipgloss.Color("153")
	background := lipgloss.Color("236")
	if mode == colorModeLight {
		foreground = lipgloss.Color("17")
		background = lipgloss.Color("189")
	}
	return lipgloss.NewStyle().Foreground(foreground).Background(background).Bold(true)
}

func viewColors(mode colorMode) (background, foreground color.Color) {
	if mode == colorModeLight {
		return lipgloss.Color(tokenCanvas.light), lipgloss.Color("#263244")
	}
	return lipgloss.Color(tokenCanvas.dark), lipgloss.Color("#D7DAE0")
}

type themeColorSwap struct{ dark, light string }

var lightThemeColorSwaps = paletteSwaps()

func paletteSwaps() []themeColorSwap {
	tokens := []themeToken{
		tokenText, tokenMuted, tokenAccent, tokenOK, tokenWarn, tokenCrit,
		tokenBorder, tokenTrack, tokenOnAccent, tokenSurface, tokenButton,
		tokenCritText, tokenCritWash, tokenCanvas,
	}
	swaps := make([]themeColorSwap, 0, len(tokens))
	seen := map[string]string{}
	for _, token := range tokens {
		if previous, ok := seen[token.dark]; ok {
			if previous != token.light {
				panic("palette color " + token.dark + " maps to two light values")
			}
			continue
		}
		seen[token.dark] = token.light
		swaps = append(swaps, themeColorSwap{dark: token.dark, light: token.light})
	}
	return swaps
}

// Rendering always uses the immutable dark palette. Light mode rewrites only
// known ANSI RGB sequences in the completed view, keeping theme selection
// session-local without mutating shared styles across concurrent SSH sessions.
func applyLightTheme(rendered string) string {
	for _, swap := range lightThemeColorSwaps {
		dark := ansiRGB(swap.dark)
		light := ansiRGB(swap.light)
		rendered = strings.ReplaceAll(rendered, "38;2;"+dark, "38;2;"+light)
		rendered = strings.ReplaceAll(rendered, "48;2;"+dark, "48;2;"+light)
	}
	return rendered
}

func ansiRGB(hex string) string {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return ""
	}
	red, _ := strconv.ParseUint(hex[0:2], 16, 8)
	green, _ := strconv.ParseUint(hex[2:4], 16, 8)
	blue, _ := strconv.ParseUint(hex[4:6], 16, 8)
	return fmt.Sprintf("%d;%d;%d", red, green, blue)
}
