package main

import (
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// IOReport exposes time spent in each DVFS state, not a clock register. Only
// active residency contributes to the interval's frequency (as in Stats/macmon).
// A wholly sleeping core has no measured active frequency; do not invent one.
type appleCPUState struct {
	name      string
	residency int64
}

func appleActiveFrequency(states []appleCPUState, frequencies []float64) float64 {
	var active []int64
	for _, state := range states {
		if state.name == "" || state.residency < 0 {
			return 0 // Counter reset or invalid delta.
		}
		switch state.name {
		case "IDLE", "DOWN", "OFF":
			continue
		}
		active = append(active, state.residency)
	}
	if len(active) == 0 || len(active) != len(frequencies) {
		return 0 // Unknown state/table schema, not a guessed frequency.
	}
	var total, weighted float64
	for i, residency := range active {
		if !validCPUFrequency(frequencies[i]) {
			return 0
		}
		total += float64(residency)
		weighted += float64(residency) * frequencies[i]
	}
	if total == 0 {
		return 0
	}
	return weighted / total
}

// pmgr DVFS records are (frequency, voltage) little-endian uint32 pairs.
// M1–M3 use Hz; M4 and later use kHz. Tables describe state values only;
// IOReport residency is always required before reporting a measured frequency.
func appleDVFSFrequencies(data []byte, model string) []float64 {
	var generation int
	if _, err := fmt.Sscanf(model, "Apple M%d", &generation); err != nil || generation < 1 || len(data) == 0 || len(data)%8 != 0 {
		return nil
	}
	scale := 1000.0
	if generation <= 3 {
		scale = 1_000_000
	}
	frequencies := make([]float64, 0, len(data)/8)
	for offset := 0; offset < len(data); offset += 8 {
		frequency := float64(binary.LittleEndian.Uint32(data[offset:])) / scale
		if !validCPUFrequency(frequency) || (len(frequencies) > 0 && frequency <= frequencies[len(frequencies)-1]) {
			return nil
		}
		frequencies = append(frequencies, frequency)
	}
	return frequencies
}

func appleCPUFrequencyKeys(clusters []byte) map[string]string {
	if len(clusters) == 0 {
		// M1–M4 pmgr layout, before the acc-clusters descriptor.
		return map[string]string{"E": "voltage-states1-sram", "P": "voltage-states5-sram"}
	}
	if len(clusters)%8 != 0 {
		return nil
	}
	keys := make(map[string]string)
	for offset := 0; offset < len(clusters); offset += 8 {
		var kind string
		switch clusters[offset+1] {
		case 0:
			kind = "E"
		case 1:
			kind = "M"
		case 2:
			kind = "P"
		default:
			return nil
		}
		if _, duplicate := keys[kind]; duplicate {
			return nil
		}
		keys[kind] = fmt.Sprintf("voltage-states%d-sram", clusters[offset])
	}
	// Two-tier descriptors call their higher tier type 1 (PCPU).
	if keys["P"] == "" {
		keys["P"] = keys["M"]
		delete(keys, "M")
	}
	return keys
}

type appleCPUCore struct {
	logical, physical int
	kind              string
}

// Match within a core type by physical CPU order, not CFArray order: IOReport
// can reorder channels. Require exact cardinality to avoid shifted CPU labels.
func mapAppleCPUChannels(names []string, cores []appleCPUCore) map[string]string {
	result := make(map[string]string)
	logicalIDs, physicalIDs := make(map[int]bool), make(map[int]bool)
	for _, core := range cores {
		if core.logical < 0 || core.physical < 0 || logicalIDs[core.logical] || physicalIDs[core.physical] {
			return result
		}
		logicalIDs[core.logical], physicalIDs[core.physical] = true, true
	}
	seen := make(map[string]bool)
	for _, name := range names {
		if seen[name] {
			return result
		}
		seen[name] = true
	}
	for _, kind := range []string{"E", "M", "P"} {
		var channels []string
		var typedCores []appleCPUCore
		for _, name := range names {
			_, suffix, found := strings.Cut(name, kind+"CPU")
			if !found || suffix == "" {
				continue
			}
			if number, err := strconv.Atoi(suffix); err == nil && number >= 0 && suffix[0] >= '0' && suffix[0] <= '9' {
				channels = append(channels, name)
			}
		}
		for _, core := range cores {
			if core.kind == kind {
				typedCores = append(typedCores, core)
			}
		}
		if len(channels) != len(typedCores) {
			continue
		}
		sort.Slice(channels, func(i, j int) bool {
			leftPrefix, leftIndex, _ := strings.Cut(channels[i], kind+"CPU")
			rightPrefix, rightIndex, _ := strings.Cut(channels[j], kind+"CPU")
			if leftPrefix != rightPrefix {
				return leftPrefix < rightPrefix
			}
			left, _ := strconv.Atoi(leftIndex)
			right, _ := strconv.Atoi(rightIndex)
			return left < right
		})
		sort.Slice(typedCores, func(i, j int) bool { return typedCores[i].physical < typedCores[j].physical })
		for i, name := range channels {
			result[name] = fmt.Sprintf("cpu%d", typedCores[i].logical)
		}
	}
	return result
}
