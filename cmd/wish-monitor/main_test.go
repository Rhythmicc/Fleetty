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

func TestAdminModelProcessManagerNavigation(t *testing.T) {
	controller := &adminController{
		password: "secret",
		actions:  []adminAction{{key: '4', label: "Manage processes", kind: adminActionProcessManager}},
	}
	model := &adminModel{controller: controller, mode: adminModeMenu}

	model.Update(keyMessage('4'))
	if model.mode != adminModeProcessList || model.processTask != processTaskList {
		t.Fatalf("process manager = mode %v, task %v", model.mode, model.processTask)
	}

	model.processTask = processTaskNone // Simulate the session completing the list request.
	model.Update(keyMessage('d'))
	if model.mode != adminModeProcessDetailPID {
		t.Fatalf("detail prompt mode = %v", model.mode)
	}
	model.Update(keyMessage(0x1b))
	if model.mode != adminModeProcessList {
		t.Fatalf("escape from detail prompt = %v", model.mode)
	}

	model.Update(keyMessage('t'))
	if model.mode != adminModeProcessTerminatePID {
		t.Fatalf("terminate prompt mode = %v", model.mode)
	}
}

func TestProcessInputSafety(t *testing.T) {
	if _, err := validateProcessPID("1"); err == nil {
		t.Fatal("PID 1 should be rejected")
	}
	if got := sanitizeTerminalText("safe\x1b[2J\ntext\x00"); got != "safe[2J\ntext" {
		t.Fatalf("sanitized process text = %q", got)
	}
}
