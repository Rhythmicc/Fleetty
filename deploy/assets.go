package deployassets

import (
	_ "embed"
	"fmt"
)

//go:embed fleetty.service
var nodeService []byte

//go:embed fleetty-hub.service
var hubService []byte

//go:embed fleetty-user.service
var userNodeService []byte

//go:embed fleetty-hub-user.service
var userHubService []byte

func ServiceUnit(role, scope string) ([]byte, string, error) {
	switch role {
	case "node":
		if scope == "user" {
			return append([]byte(nil), userNodeService...), "fleetty.service", nil
		}
		if scope == "system" {
			return append([]byte(nil), nodeService...), "fleetty.service", nil
		}
	case "hub":
		if scope == "user" {
			return append([]byte(nil), userHubService...), "fleetty-hub.service", nil
		}
		if scope == "system" {
			return append([]byte(nil), hubService...), "fleetty-hub.service", nil
		}
	}
	return nil, "", fmt.Errorf("unsupported role %q or scope %q", role, scope)
}
