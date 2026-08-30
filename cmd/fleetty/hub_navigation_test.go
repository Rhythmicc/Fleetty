package main

import (
	"testing"
)

func productionHubGroups() []hubGroupConfig {
	return []hubGroupConfig{
		{ID: "gpu", Title: "GPU COMPUTE", Style: hubGroupStyleGPU},
		{ID: "cpu", Title: "CPU COMPUTE", Style: hubGroupStyleCPU},
		{ID: "services", Title: "CLUSTER SERVICES", Style: hubGroupStyleNetwork},
	}
}

func productionHubNodes() []hubNodeConfig {
	names := []string{"A100", "4090", "5090", "n1", "n2", "n3", "n4", "NAS", "intel9462"}
	profiles := []string{
		machineProfileGPU, machineProfileGPU, machineProfileGPU,
		machineProfileGPU, machineProfileGPU, machineProfileGPU, machineProfileGPU,
		machineProfileNAS, machineProfileCPU,
	}
	nodes := make([]hubNodeConfig, len(names))
	for index := range names {
		group := "gpu"
		if profiles[index] == machineProfileCPU {
			group = "cpu"
		} else if profiles[index] == machineProfileNAS || profiles[index] == machineProfileGeneral {
			group = "services"
		}
		nodes[index] = hubNodeConfig{Name: names[index], Profile: profiles[index], Group: group}
	}
	return nodes
}

func newTestHubModel(width, height int, cursor int) *hubModel {
	model := &hubModel{
		config: hubConfig{Groups: productionHubGroups(), Nodes: productionHubNodes()},
		width:  width, height: height,
	}
	model.cursor = cursor
	model.clampCursor()
	return model
}

func TestHubUpFromCPUNodeFollowsVisualGrid(t *testing.T) {
	// 132 columns renders three card columns; the flat grouped list is not
	// aligned with the visual grid, so navigation must follow the visible
	// grouped rows rather than subtracting a fixed card stride.
	model := newTestHubModel(132, 24, 8)
	model.moveCursorVertical(-1)
	if model.cursor != 6 {
		t.Fatalf("up from intel9462 selected index %d, want n4 (6)", model.cursor)
	}
	model.moveCursorVertical(-1)
	if model.cursor != 3 {
		t.Fatalf("up from n4 selected index %d, want n1 (3)", model.cursor)
	}
}

func TestHubUsesDeclarativeGroupMembership(t *testing.T) {
	model := &hubModel{config: hubConfig{
		Groups: []hubGroupConfig{{ID: "services", Title: "CLUSTER SERVICES", Style: hubGroupStyleNetwork}},
		Nodes: []hubNodeConfig{
			{Name: "NAS", Profile: machineProfileNAS, Group: "services"},
			{Name: "login", Profile: machineProfileGeneral, Group: "services"},
		},
	}}
	groups := model.nodeGroups()
	if len(groups) != 1 || groups[0].title != "CLUSTER SERVICES" {
		t.Fatalf("service groups = %#v", groups)
	}
	if len(groups[0].nodes) != 2 || groups[0].nodes[0] != 0 || groups[0].nodes[1] != 1 {
		t.Fatalf("service nodes = %v, want [0 1]", groups[0].nodes)
	}
}

func TestHubDownFromN3StaysInSameColumn(t *testing.T) {
	model := newTestHubModel(132, 24, 5)
	model.moveCursorVertical(1)
	if model.cursor != 6 {
		t.Fatalf("down from n3 selected index %d, want n4 (6)", model.cursor)
	}
}

func TestHubHorizontalNavigationWrapsAcrossRows(t *testing.T) {
	model := newTestHubModel(132, 24, 0)
	model.moveCursorHorizontal(1)
	if model.cursor != 1 {
		t.Fatalf("right from A100 selected index %d, want 4090 (1)", model.cursor)
	}
	model.moveCursorHorizontal(1)
	if model.cursor != 2 {
		t.Fatalf("right from 4090 selected index %d, want 5090 (2)", model.cursor)
	}
	model.moveCursorHorizontal(1)
	if model.cursor != 3 {
		t.Fatalf("right from 5090 selected index %d, want n1 (3)", model.cursor)
	}
	model.moveCursorHorizontal(-1)
	if model.cursor != 2 {
		t.Fatalf("left from n1 selected index %d, want 5090 (2)", model.cursor)
	}
}

func TestHubQAndEscReturnFromNodeDetail(t *testing.T) {
	for _, key := range []string{"q", "Q", "esc"} {
		model := newTestHubModel(100, 30, 0)
		model.detail = &monitorModel{screen: screenMonitor}
		updated, _ := model.Update(testKey(key))
		hub := updated.(*hubModel)
		if hub.detail != nil {
			t.Fatalf("key %q did not return from node detail", key)
		}
	}
}

func TestHubQStillQuitsFromHomepage(t *testing.T) {
	model := newTestHubModel(100, 30, 0)
	updated, command := model.Update(testKey("Q"))
	if command == nil {
		t.Fatal("uppercase Q on the homepage should quit")
	}
	if _, ok := updated.(*hubModel); !ok {
		t.Fatalf("unexpected model type %T", updated)
	}
}
