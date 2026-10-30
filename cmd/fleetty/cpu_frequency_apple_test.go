package main

import (
	"encoding/binary"
	"math"
	"reflect"
	"testing"
)

func TestAppleActiveFrequency(t *testing.T) {
	states := []appleCPUState{{"DOWN", 9000}, {"IDLE", 1000}, {"P0", 10}, {"P1", 30}}
	if got := appleActiveFrequency(states, []float64{1000, 3000}); got != 2500 {
		t.Fatalf("active weighted frequency = %v, want 2500 (excluding sleep)", got)
	}
	cases := []struct {
		name        string
		states      []appleCPUState
		frequencies []float64
	}{
		{"sleep only", []appleCPUState{{"DOWN", 9000}, {"IDLE", 1000}}, []float64{1000}},
		{"zero active", []appleCPUState{{"OFF", 100}, {"P0", 0}}, []float64{1000}},
		{"unknown name", []appleCPUState{{"", 10}}, []float64{1000}},
		{"reset active", []appleCPUState{{"P0", -1}}, []float64{1000}},
		{"reset idle", []appleCPUState{{"IDLE", -1}, {"P0", 10}}, []float64{1000}},
		{"state mismatch", states, []float64{1000}},
		{"missing table", states, nil},
		{"invalid table", states, []float64{1000, math.NaN()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := appleActiveFrequency(tc.states, tc.frequencies); got != 0 {
				t.Fatalf("unmeasured/invalid frequency = %v", got)
			}
		})
	}
}

func appleTestDVFS(values ...uint32) []byte {
	data := make([]byte, 8*len(values))
	for i, value := range values {
		binary.LittleEndian.PutUint32(data[8*i:], value)
		binary.LittleEndian.PutUint32(data[8*i+4:], 900)
	}
	return data
}

func TestAppleDVFSUnitsAndValidation(t *testing.T) {
	for _, model := range []string{"Apple M1", "Apple M2 Pro", "Apple M3 Ultra"} {
		if got := appleDVFSFrequencies(appleTestDVFS(600_000_000, 3_200_000_000), model); !reflect.DeepEqual(got, []float64{600, 3200}) {
			t.Fatalf("Hz table on %s: %v", model, got)
		}
	}
	for _, model := range []string{"Apple M4", "Apple M5 Max"} {
		if got := appleDVFSFrequencies(appleTestDVFS(600_000, 4_608_000), model); !reflect.DeepEqual(got, []float64{600, 4608}) {
			t.Fatalf("kHz table on %s: %v", model, got)
		}
	}
	for _, data := range [][]byte{nil, {1, 2, 3}, appleTestDVFS(0), appleTestDVFS(3200, 3200), appleTestDVFS(3200, 1000)} {
		if got := appleDVFSFrequencies(data, "Apple M5"); got != nil {
			t.Fatalf("malformed table accepted: %v", got)
		}
	}
	for _, model := range []string{"Intel Core i9", "Apple M0", "unknown"} {
		if got := appleDVFSFrequencies(appleTestDVFS(600_000), model); got != nil {
			t.Fatalf("unknown hardware %s accepted: %v", model, got)
		}
	}
}

func TestAppleCPUFrequencyTableDescriptors(t *testing.T) {
	if got := appleCPUFrequencyKeys(nil); !reflect.DeepEqual(got, map[string]string{"E": "voltage-states1-sram", "P": "voltage-states5-sram"}) {
		t.Fatalf("two-tier pmgr layout = %v", got)
	}
	// M5 Max describes three tiers even though no efficiency cores are fitted.
	data := []byte{22, 0, 0, 0, 0, 0, 0, 0, 23, 1, 0, 0, 0, 0, 0, 0, 5, 2, 0, 0, 0, 0, 0, 0}
	if got := appleCPUFrequencyKeys(data); !reflect.DeepEqual(got, map[string]string{"E": "voltage-states22-sram", "M": "voltage-states23-sram", "P": "voltage-states5-sram"}) {
		t.Fatalf("M5 descriptor = %v", got)
	}
	if got := appleCPUFrequencyKeys(data[:16]); !reflect.DeepEqual(got, map[string]string{"E": "voltage-states22-sram", "P": "voltage-states23-sram"}) {
		t.Fatalf("two-tier descriptor = %v", got)
	}
	for _, invalid := range [][]byte{{1}, {1, 9, 0, 0, 0, 0, 0, 0}, append(data[:8:8], data[:8]...)} {
		if got := appleCPUFrequencyKeys(invalid); got != nil {
			t.Fatalf("invalid descriptor accepted: %v", got)
		}
	}
}

func TestAppleCPUChannelMapping(t *testing.T) {
	cores := []appleCPUCore{{9, 0x102, "P"}, {0, 0, "E"}, {5, 0x100, "P"}, {1, 1, "E"}}
	got := mapAppleCPUChannels([]string{"PCPU2", "ECPU1", "PCPU0", "ECPU0"}, cores)
	want := map[string]string{"ECPU0": "cpu0", "ECPU1": "cpu1", "PCPU0": "cpu5", "PCPU2": "cpu9"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("physical to logical ID mapping = %v, want %v", got, want)
	}
	// Missing one P channel must not shift the remaining P IDs. E still works.
	got = mapAppleCPUChannels([]string{"PCPU2", "ECPU1", "ECPU0"}, cores)
	if !reflect.DeepEqual(got, map[string]string{"ECPU0": "cpu0", "ECPU1": "cpu1"}) {
		t.Fatalf("partial channels shifted IDs: %v", got)
	}
	got = mapAppleCPUChannels([]string{"MCPU10", "MCPU01", "MCPU00", "PCPU0"}, []appleCPUCore{{0, 0, "M"}, {1, 1, "M"}, {2, 256, "M"}, {3, 512, "P"}})
	if !reflect.DeepEqual(got, map[string]string{"MCPU00": "cpu0", "MCPU01": "cpu1", "MCPU10": "cpu2", "PCPU0": "cpu3"}) {
		t.Fatalf("M5 multi-cluster mapping = %v", got)
	}
	got = mapAppleCPUChannels([]string{"PACC1_PCPU10", "PACC0_PCPU2", "PACC0_PCPU1"}, []appleCPUCore{{0, 1, "P"}, {1, 2, "P"}, {2, 256, "P"}})
	if got["PACC0_PCPU1"] != "cpu0" || got["PACC0_PCPU2"] != "cpu1" || got["PACC1_PCPU10"] != "cpu2" {
		t.Fatalf("numeric channel ordering = %v", got)
	}
	for _, names := range [][]string{{"ECPU-1", "ECPU0"}, {"ECPU0", "ECPU0"}, {"GPU0"}} {
		if got := mapAppleCPUChannels(names, cores); len(got) != 0 {
			t.Fatalf("ambiguous channels mapped: %v", got)
		}
	}
	if got := mapAppleCPUChannels([]string{"ECPU0", "ECPU1"}, []appleCPUCore{{0, 0, "E"}, {0, 1, "E"}}); len(got) != 0 {
		t.Fatalf("duplicate logical IDs mapped: %v", got)
	}
}
