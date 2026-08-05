package main

import (
	"testing"
)

func TestQAndEscQuitFromTopLevelMonitor(t *testing.T) {
	for _, key := range []string{"q", "esc"} {
		model := &monitorModel{screen: screenMonitor}
		if command := model.handleKey(testKey(key)); command == nil {
			t.Fatalf("key %q at the top level should quit", key)
		}
	}
}

func TestQAndEscNavigateStorageInsteadOfQuitting(t *testing.T) {
	model := &monitorModel{
		screen:      screenMonitor,
		monitorPage: monitorPageStorage,
		storage: &storageMapState{
			Root: "/", Path: "/", DuplicateMode: true,
		},
	}
	if command := model.handleKey(testKey("q")); command == nil {
		t.Fatal("q in duplicate mode should leave the duplicate view")
	}
	if model.storage.DuplicateMode {
		t.Fatal("q should have left duplicate mode")
	}
}

func TestQIsLiteralInInputStates(t *testing.T) {
	password := &monitorModel{screen: screenPassword}
	password.handleKey(testKey("q"))
	if password.password != "q" {
		t.Fatalf("q in password input should be literal, got %q", password.password)
	}

	filter := &monitorModel{screen: screenMonitor, filtering: true}
	filter.handleKey(testKey("q"))
	if filter.filter != "q" {
		t.Fatalf("q in filter input should be literal, got %q", filter.filter)
	}
}

func TestQActsAsEscOnConfirmationScreens(t *testing.T) {
	confirm := &monitorModel{
		screen: screenConfirm, selectedAction: &adminAction{},
	}
	confirm.handleKey(testKey("q"))
	if confirm.screen != screenAdmin {
		t.Fatalf("q on confirm screen went to %v, want admin", confirm.screen)
	}

	terminate := &monitorModel{screen: screenProcessTerminateConfirm}
	terminate.handleKey(testKey("q"))
	if terminate.screen != screenProcessDetail {
		t.Fatalf("q on terminate confirm went to %v, want process detail", terminate.screen)
	}
}

func TestEscQuitsFromHubHomepageLikeQ(t *testing.T) {
	for _, key := range []string{"q", "Q", "esc"} {
		model := newTestHubModel(100, 30, 0)
		_, command := model.Update(testKey(key))
		if command == nil {
			t.Fatalf("key %q on the Hub homepage should quit", key)
		}
	}
}
