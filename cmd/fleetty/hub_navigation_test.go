package main

import (
	"testing"
)

func productionHubNodes() []hubNodeConfig {
	names := []string{"A100", "4090", "5090", "n1", "n2", "n3", "n4", "NAS", "intel9462"}
	profiles := []string{
		machineProfileGPU, machineProfileGPU, machineProfileGPU,
		machineProfileGPU, machineProfileGPU, machineProfileGPU, machineProfileGPU,
		machineProfileNAS, machineProfileGeneral,
	}
	nodes := make([]hubNodeConfig, len(names))
	for index := range names {
		nodes[index] = hubNodeConfig{Name: names[index], Profile: profiles[index]}
	}
	return nodes
}

func newTestHubModel(width, height int, cursor int) *hubModel {
	model := &hubModel{
		config: hubConfig{Nodes: productionHubNodes()},
		width:  width, height: height,
	}
	model.cursor = cursor
	model.clampCursor()
	return model
}

func TestHubUpFromLastGeneralNodeFollowsVisualGrid(t *testing.T) {
	// 132 columns renders three card columns; the flat grouped list is not
	// aligned with the visual grid, so the old stride-based navigation jumped
	// from intel9462 to n3 (position 8-3=5).
	model := newTestHubModel(132, 24, 8)
	model.moveCursorVertical(-1)
	if model.cursor != 7 {
		t.Fatalf("up from intel9462 selected index %d, want NAS (7)", model.cursor)
	}
	model.moveCursorVertical(-1)
	if model.cursor != 6 {
		t.Fatalf("up from NAS selected index %d, want n4 (6)", model.cursor)
	}
	model.moveCursorVertical(-1)
	if model.cursor != 3 {
		t.Fatalf("up from n4 selected index %d, want n1 (3)", model.cursor)
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
