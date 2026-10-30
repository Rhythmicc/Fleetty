package main

import (
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

const appleFrequencySource = "IOReport · active residency"

// The private IOReport API is dynamically resolved: no cgo/toolchain helper,
// root privilege, subprocess or additional installed binary is required.
// Bindings follow the API signatures used by Stats and macmon:
// https://github.com/vladkens/macmon/blob/main/src/sources.rs
type appleCPUAPI struct {
	release             func(uintptr)
	typeID              func(uintptr) uintptr
	dataTypeID          func() uintptr
	arrayTypeID         func() uintptr
	dictTypeID          func() uintptr
	numberTypeID        func() uintptr
	stringCreate        func(uintptr, string, uint32) uintptr
	stringCString       func(uintptr, []byte, int64, uint32) bool
	dictValue           func(uintptr, uintptr) uintptr
	arrayCount          func(uintptr) int64
	arrayValue          func(uintptr, int64) uintptr
	dataLength          func(uintptr) int64
	dataPointer         func(uintptr) *byte
	numberValue         func(uintptr, int64, *int64) bool
	copyChannels        func(uintptr, uintptr, uint64, uint64, uint64) uintptr
	subscribe           func(uintptr, uintptr, *uintptr, uint64, uintptr) uintptr
	samples             func(uintptr, uintptr, uintptr) uintptr
	delta               func(uintptr, uintptr, uintptr) uintptr
	channelName         func(uintptr) uintptr
	stateCount          func(uintptr) int32
	stateName           func(uintptr, int32) uintptr
	stateResidency      func(uintptr, int32) int64
	serviceNameMatching func(string) uintptr
	matchingServices    func(uint32, uintptr, *uint32) int32
	iteratorNext        func(uint32) uint32
	objectRelease       func(uint32) int32
	entryFromPath       func(uint32, string) uint32
	childIterator       func(uint32, string, *uint32) int32
	properties          func(uint32, *uintptr, uintptr, uint32) int32
}

var loadAppleCPUAPI = sync.OnceValues(func() (*appleCPUAPI, error) {
	a := new(appleCPUAPI)
	libs := []struct {
		path     string
		bindings map[string]any
	}{
		{"/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", map[string]any{
			"CFRelease": &a.release, "CFGetTypeID": &a.typeID,
			"CFDataGetTypeID": &a.dataTypeID, "CFArrayGetTypeID": &a.arrayTypeID,
			"CFDictionaryGetTypeID": &a.dictTypeID, "CFNumberGetTypeID": &a.numberTypeID,
			"CFStringCreateWithCString": &a.stringCreate, "CFStringGetCString": &a.stringCString,
			"CFDictionaryGetValue": &a.dictValue, "CFArrayGetCount": &a.arrayCount,
			"CFArrayGetValueAtIndex": &a.arrayValue, "CFDataGetLength": &a.dataLength,
			"CFDataGetBytePtr": &a.dataPointer, "CFNumberGetValue": &a.numberValue,
		}},
		{"/usr/lib/libIOReport.dylib", map[string]any{
			"IOReportCopyChannelsInGroup": &a.copyChannels,
			"IOReportCreateSubscription":  &a.subscribe, "IOReportCreateSamples": &a.samples,
			"IOReportCreateSamplesDelta": &a.delta, "IOReportChannelGetChannelName": &a.channelName,
			"IOReportStateGetCount": &a.stateCount, "IOReportStateGetNameForIndex": &a.stateName,
			"IOReportStateGetResidency": &a.stateResidency,
		}},
		{"/System/Library/Frameworks/IOKit.framework/IOKit", map[string]any{
			"IOServiceNameMatching": &a.serviceNameMatching, "IOServiceGetMatchingServices": &a.matchingServices,
			"IOIteratorNext": &a.iteratorNext, "IOObjectRelease": &a.objectRelease,
			"IORegistryEntryFromPath": &a.entryFromPath, "IORegistryEntryGetChildIterator": &a.childIterator,
			"IORegistryEntryCreateCFProperties": &a.properties,
		}},
	}
	var handles []uintptr
	success := false
	defer func() {
		if !success {
			for i := len(handles) - 1; i >= 0; i-- {
				_ = purego.Dlclose(handles[i])
			}
		}
	}()
	for _, lib := range libs {
		handle, err := purego.Dlopen(lib.path, purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			return nil, err
		}
		handles = append(handles, handle)
		for name, target := range lib.bindings {
			symbol, err := purego.Dlsym(handle, name)
			if err != nil {
				return nil, err
			}
			purego.RegisterFunc(target, symbol)
		}
	}
	// Successfully bound system libraries stay loaded for the process lifetime.
	success = true
	return a, nil
})

func (a *appleCPUAPI) string(value string) uintptr {
	return a.stringCreate(0, value, 0x08000100) // kCFStringEncodingUTF8
}

func (a *appleCPUAPI) text(ref uintptr) string {
	if ref == 0 {
		return ""
	}
	buffer := make([]byte, 512)
	if !a.stringCString(ref, buffer, int64(len(buffer)), 0x08000100) {
		return ""
	}
	return strings.TrimRight(string(buffer), "\x00")
}

func (a *appleCPUAPI) get(dict uintptr, key string) uintptr {
	if dict == 0 || a.typeID(dict) != a.dictTypeID() {
		return 0
	}
	name := a.string(key)
	if name == 0 {
		return 0
	}
	defer a.release(name)
	return a.dictValue(dict, name)
}

func (a *appleCPUAPI) bytes(dict uintptr, key string) []byte {
	value := a.get(dict, key)
	if value == 0 || a.typeID(value) != a.dataTypeID() {
		return nil
	}
	length := a.dataLength(value)
	if length <= 0 || length > 1<<20 {
		return nil
	}
	pointer := a.dataPointer(value)
	if pointer == nil {
		return nil
	}
	return append([]byte(nil), unsafe.Slice(pointer, int(length))...)
}

func (a *appleCPUAPI) coreTopology() []appleCPUCore {
	entry := a.entryFromPath(0, "IODeviceTree:/cpus")
	if entry == 0 {
		return nil
	}
	defer a.objectRelease(entry)
	var iterator uint32
	if a.childIterator(entry, "IODeviceTree", &iterator) != 0 || iterator == 0 {
		return nil
	}
	defer a.objectRelease(iterator)
	var cores []appleCPUCore
	for child := a.iteratorNext(iterator); child != 0; child = a.iteratorNext(iterator) {
		var props uintptr
		status := a.properties(child, &props, 0, 0)
		a.objectRelease(child)
		if status != 0 || props == 0 {
			continue
		}
		physical := a.bytes(props, "cpu-id")
		kind := strings.TrimRight(string(a.bytes(props, "cluster-type")), "\x00")
		logicalRef := a.get(props, "logical-cpu-id")
		var logical int64
		if len(physical) == 4 && logicalRef != 0 && a.typeID(logicalRef) == a.numberTypeID() &&
			a.numberValue(logicalRef, 4, &logical) && logical >= 0 && logical < 4096 { // kCFNumberSInt64Type
			id := int(physical[0]) | int(physical[1])<<8 | int(physical[2])<<16 | int(physical[3])<<24
			cores = append(cores, appleCPUCore{logical: int(logical), physical: id, kind: kind})
		}
		a.release(props)
	}
	return cores
}

func (a *appleCPUAPI) frequencyTables() map[string][]float64 {
	model, err := unix.Sysctl("machdep.cpu.brand_string")
	if err != nil {
		return nil
	}
	tables := make(map[string][]float64)
	for _, name := range []string{"pmgr", "pmgr-child"} {
		var iterator uint32
		matching := a.serviceNameMatching(name)
		if matching == 0 {
			continue
		}
		if a.matchingServices(0, matching, &iterator) != 0 || iterator == 0 {
			continue
		}
		for entry := a.iteratorNext(iterator); entry != 0; entry = a.iteratorNext(iterator) {
			var props uintptr
			status := a.properties(entry, &props, 0, 0)
			a.objectRelease(entry)
			if status != 0 || props == 0 {
				continue
			}
			for kind, key := range appleCPUFrequencyKeys(a.bytes(props, "acc-clusters")) {
				frequencies := appleDVFSFrequencies(a.bytes(props, key), model)
				if len(frequencies) > 0 && len(tables[kind]) == 0 {
					tables[kind] = frequencies
				}
			}
			a.release(props)
		}
		a.objectRelease(iterator)
	}
	return tables
}

type appleCPUResources struct {
	api                              *appleCPUAPI
	channels, subscription, previous uintptr
}

func (r *appleCPUResources) close() {
	for _, ref := range []uintptr{r.previous, r.subscription, r.channels} {
		if ref != 0 {
			r.api.release(ref)
		}
	}
	r.previous, r.subscription, r.channels = 0, 0, 0
}

type appleCPUFrequencyReader struct {
	resources *appleCPUResources
	cores     []appleCPUCore
	tables    map[string][]float64
	cleanup   runtime.Cleanup
}

func newPlatformCPUFrequencyReader() cpuFrequencyReader {
	a, err := loadAppleCPUAPI()
	if err != nil {
		return nil
	}
	cores, tables := a.coreTopology(), a.frequencyTables()
	if len(cores) == 0 || len(tables) == 0 {
		return nil
	}
	group, subgroup := a.string("CPU Stats"), a.string("CPU Core Performance States")
	if group == 0 || subgroup == 0 {
		if group != 0 {
			a.release(group)
		}
		if subgroup != 0 {
			a.release(subgroup)
		}
		return nil
	}
	channels := a.copyChannels(group, subgroup, 0, 0, 0)
	a.release(group)
	a.release(subgroup)
	if channels == 0 {
		return nil
	}
	r := &appleCPUResources{api: a, channels: channels}
	var subscribed uintptr
	r.subscription = a.subscribe(0, channels, &subscribed, 0, 0)
	if subscribed != 0 {
		a.release(subscribed)
	}
	if r.subscription == 0 {
		r.close()
		return nil
	}
	reader := &appleCPUFrequencyReader{resources: r, cores: cores, tables: tables}
	reader.cleanup = runtime.AddCleanup(reader, (*appleCPUResources).close, r)
	return reader
}

func (r *appleCPUFrequencyReader) read() (map[string]float64, string) {
	defer runtime.KeepAlive(r) // Prevent native cleanup during a purego call.
	resources, a := r.resources, r.resources.api
	current := a.samples(resources.subscription, resources.channels, 0)
	if current == 0 {
		if resources.previous != 0 {
			a.release(resources.previous)
		}
		resources.previous = 0
		return nil, appleFrequencySource
	}
	previous := resources.previous
	resources.previous = current
	if previous == 0 {
		return nil, appleFrequencySource
	}
	defer a.release(previous)
	delta := a.delta(previous, current, 0)
	if delta == 0 {
		return nil, appleFrequencySource
	}
	defer a.release(delta)
	array := a.get(delta, "IOReportChannels")
	if array == 0 || a.typeID(array) != a.arrayTypeID() {
		return nil, appleFrequencySource
	}
	count := a.arrayCount(array)
	if count <= 0 || count > 4096 {
		return nil, appleFrequencySource
	}
	samples := make(map[string][]appleCPUState)
	var names []string
	for i := int64(0); i < count; i++ {
		channel := a.arrayValue(array, i)
		if channel == 0 || a.typeID(channel) != a.dictTypeID() {
			continue
		}
		name := a.text(a.channelName(channel))
		stateCount := a.stateCount(channel)
		if name == "" || stateCount < 1 || stateCount > 256 {
			continue
		}
		states := make([]appleCPUState, 0, stateCount)
		for j := int32(0); j < stateCount; j++ {
			states = append(states, appleCPUState{name: a.text(a.stateName(channel, j)), residency: a.stateResidency(channel, j)})
		}
		if _, duplicate := samples[name]; duplicate {
			return nil, appleFrequencySource
		}
		samples[name] = states
		names = append(names, name)
	}
	frequencies := make(map[string]float64)
	for name, cpu := range mapAppleCPUChannels(names, r.cores) {
		for _, kind := range []string{"E", "M", "P"} {
			if !strings.Contains(name, kind+"CPU") {
				continue
			}
			frequency := appleActiveFrequency(samples[name], r.tables[kind])
			if validCPUFrequency(frequency) {
				frequencies[cpu] = frequency
			}
			break
		}
	}
	return frequencies, appleFrequencySource
}

var _ cpuFrequencyReader = (*appleCPUFrequencyReader)(nil)
