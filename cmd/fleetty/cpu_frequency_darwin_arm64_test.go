package main

import (
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestAppleCPUFrequencyNative(t *testing.T) {
	reader, ok := newPlatformCPUFrequencyReader().(*appleCPUFrequencyReader)
	if !ok {
		t.Skip("IOReport hardware unavailable (e.g. a virtual Mac)")
	}
	defer func() { reader.cleanup.Stop(); reader.resources.close() }()
	t.Logf("CPU topology: %+v; DVFS tables: %+v", reader.cores, reader.tables)
	if first, source := reader.read(); len(first) != 0 || source != appleFrequencySource {
		t.Fatalf("first sample must only establish baseline: %v, %s", first, source)
	}
	time.Sleep(150 * time.Millisecond)
	frequencies, source := reader.read()
	if len(frequencies) == 0 || source != appleFrequencySource {
		t.Fatalf("no native frequency sample: %v, %s", frequencies, source)
	}
	known := make(map[string][]float64)
	for _, core := range reader.cores {
		known["cpu"+strconv.Itoa(core.logical)] = reader.tables[core.kind]
	}
	for cpu, frequency := range frequencies {
		table := known[cpu]
		if len(table) == 0 || frequency < table[0]-0.001 || frequency > table[len(table)-1]+0.001 {
			t.Fatalf("%s=%v outside its actual DVFS table %v", cpu, frequency, table)
		}
	}
	t.Logf("Native per-core MHz: %v", frequencies)
	// A separate collector owns a separate subscription and baseline.
	other, ok := newPlatformCPUFrequencyReader().(*appleCPUFrequencyReader)
	if !ok {
		t.Fatal("second native subscription unavailable")
	}
	defer func() { other.cleanup.Stop(); other.resources.close() }()
	if first, _ := other.read(); len(first) != 0 {
		t.Fatal("independent reader inherited a baseline")
	}
}

func TestAppleCPUFrequencySampleFailureResetsBaseline(t *testing.T) {
	var released []uintptr
	a := &appleCPUAPI{
		release: func(ref uintptr) { released = append(released, ref) },
		samples: func(_, _, _ uintptr) uintptr { return 0 },
	}
	resources := &appleCPUResources{api: a, previous: 42}
	reader := &appleCPUFrequencyReader{resources: resources}
	if frequencies, _ := reader.read(); len(frequencies) != 0 || resources.previous != 0 || !reflect.DeepEqual(released, []uintptr{42}) {
		t.Fatalf("failed read retained/leaked old sample: %v / %v / %v", frequencies, resources.previous, released)
	}
	a.samples = func(_, _, _ uintptr) uintptr { return 43 }
	if frequencies, _ := reader.read(); len(frequencies) != 0 || resources.previous != 43 {
		t.Fatal("sample after failure reused a stale baseline")
	}
	resources.subscription, resources.channels = 41, 40
	resources.close()
	resources.close()
	if !reflect.DeepEqual(released, []uintptr{42, 43, 41, 40}) {
		t.Fatalf("native references not released exactly once: %v", released)
	}
}
