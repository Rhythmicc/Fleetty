package main

import (
	"errors"
	"flag"
	"io"
	"os"
	"os/user"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
)

func runTopCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("top", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "optional machine configuration")
	theme := flags.String("theme", envString("DEFAULT_THEME", "dark"), "dark or light")
	layoutPathFlag := flags.String("layout", "", "dashboard layout file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("top does not accept positional arguments")
	}
	mode := parseColorMode(*theme)
	if normalized := strings.ToLower(strings.TrimSpace(*theme)); normalized != "dark" && normalized != "light" {
		return errors.New("theme must be dark or light")
	}

	machine := machineConfig{Profile: machineProfileGPU}
	if runtime.GOOS == "darwin" {
		machine.Profile = machineProfileGeneral
	}
	if strings.TrimSpace(*configPath) != "" {
		loaded, err := loadMachineConfig(*configPath)
		if err != nil {
			return err
		}
		machine = loaded
	}
	if machine.Name == "" {
		machine.Name, _ = os.Hostname()
		machine.Name = sanitizeTerminalText(machine.Name)
	}

	layoutPath, err := resolvePanelLayoutPath(*layoutPathFlag)
	if err != nil {
		return err
	}
	panelLayout, layoutErr := loadPanelLayout(layoutPath)

	admin := newAdminController()
	account, _ := user.Current()
	userName := "local"
	if account != nil && account.Username != "" {
		userName = account.Username
	}
	model := &monitorModel{
		backend:     newLocalMonitorBackend(admin, machine, userName, "local-terminal"),
		admin:       admin,
		user:        userName,
		remote:      "local-terminal",
		nodeName:    machine.Name,
		profile:     machine.Profile,
		status:      "Local metrics refresh every second.",
		colorMode:   mode,
		panelLayout: panelLayout,
		layoutPath:  layoutPath,
		storage:     newStorageMapState(),
	}
	if layoutErr != nil {
		model.status = "Using the default layout: " + layoutErr.Error()
	}
	_, err = tea.NewProgram(model, tea.WithInput(stdin), tea.WithOutput(stdout)).Run()
	return err
}
