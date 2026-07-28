package deployassets

import (
	"strings"
	"testing"
)

func TestServiceUnitsMatchScope(t *testing.T) {
	for _, test := range []struct {
		role     string
		scope    string
		service  string
		contains string
	}{
		{role: "node", scope: "system", service: "fleetty.service", contains: "User=root"},
		{role: "hub", scope: "system", service: "fleetty-hub.service", contains: "User=root"},
		{role: "privileged-helper", scope: "system", service: "fleetty-privileged.service", contains: "CapabilityBoundingSet=CAP_KILL CAP_SYS_BOOT"},
		{role: "node", scope: "user", service: "fleetty.service", contains: "ExecStart=%h/.local/bin/fleetty"},
		{role: "hub", scope: "user", service: "fleetty-hub.service", contains: "ExecStart=%h/.local/bin/fleetty"},
	} {
		t.Run(test.role+"-"+test.scope, func(t *testing.T) {
			unit, service, err := ServiceUnit(test.role, test.scope)
			if err != nil {
				t.Fatal(err)
			}
			if service != test.service || !strings.Contains(string(unit), test.contains) {
				t.Fatalf("service=%q unit=%s", service, unit)
			}
			if test.scope == "user" && strings.Contains(string(unit), "User=root") {
				t.Fatal("user unit must not request root")
			}
			if test.role == "node" && test.scope == "system" &&
				!strings.Contains(string(unit), "AmbientCapabilities=CAP_SETUID CAP_SETGID") {
				t.Fatal("system node unit must retain capabilities required to query a user's PM2 daemon")
			}
			if test.scope == "user" && strings.Contains(string(unit), "AmbientCapabilities=") {
				t.Fatal("user unit must not request ambient capabilities")
			}
		})
	}
}
