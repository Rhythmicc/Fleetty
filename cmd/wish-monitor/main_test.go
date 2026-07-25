package main

import "testing"

func TestAdminModelPasswordAndConfirmation(t *testing.T) {
	controller := &adminController{
		password: "correct horse battery staple",
		actions:  []adminAction{{key: '1', label: "Restart monitor", command: "true"}},
	}
	model := &adminModel{controller: controller}

	model.Update(keyMessage('m'))
	if model.mode != adminModePassword {
		t.Fatalf("mode after m = %v, want password prompt", model.mode)
	}
	for _, input := range []byte("correct horse battery staple") {
		model.Update(keyMessage(input))
	}
	model.Update(keyMessage('\r'))
	if model.mode != adminModeMenu {
		t.Fatalf("mode after correct password = %v, want menu", model.mode)
	}

	model.Update(keyMessage('1'))
	if model.mode != adminModeConfirm {
		t.Fatalf("mode after selecting action = %v, want confirmation", model.mode)
	}
	model.Update(keyMessage('y'))
	if model.pending == nil || model.pending.command != "true" {
		t.Fatalf("confirmed action was not queued: %#v", model.pending)
	}
}

func TestAdminModelDisabledAndIncorrectPassword(t *testing.T) {
	disabled := &adminModel{controller: &adminController{}}
	disabled.Update(keyMessage('m'))
	if disabled.mode != adminModeDisabled {
		t.Fatalf("disabled mode = %v, want disabled prompt", disabled.mode)
	}

	model := &adminModel{controller: &adminController{password: "secret"}}
	model.Update(keyMessage('m'))
	model.Update(keyMessage('x'))
	model.Update(keyMessage('\r'))
	if model.mode != adminModePassword || model.status != "Incorrect password." {
		t.Fatalf("incorrect password state = mode %v, status %q", model.mode, model.status)
	}
}
