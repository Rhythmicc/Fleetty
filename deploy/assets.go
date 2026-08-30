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

//go:embed fleetty-privileged.service
var privilegedService []byte

//go:embed fleetty-compatibility.conf
var nodeCompatibility []byte

//go:embed fleetty-user-compatibility.conf
var userNodeCompatibility []byte

//go:embed fleetty-hub-compatibility.conf
var hubCompatibility []byte

//go:embed fleetty-hub-user-compatibility.conf
var userHubCompatibility []byte

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
	case "privileged-helper":
		if scope == "system" {
			return append([]byte(nil), privilegedService...), "fleetty-privileged.service", nil
		}
	}
	return nil, "", fmt.Errorf("unsupported role %q or scope %q", role, scope)
}

func CompatibilityDropIn(role, scope string) ([]byte, string, error) {
	switch role {
	case "node":
		if scope == "user" {
			return append([]byte(nil), userNodeCompatibility...), "10-fleetty-capabilities.conf", nil
		}
		if scope == "system" {
			return append([]byte(nil), nodeCompatibility...), "10-fleetty-capabilities.conf", nil
		}
	case "hub":
		if scope == "user" {
			return append([]byte(nil), userHubCompatibility...), "10-fleetty-capabilities.conf", nil
		}
		if scope == "system" {
			return append([]byte(nil), hubCompatibility...), "10-fleetty-capabilities.conf", nil
		}
	case "privileged-helper":
		if scope == "system" {
			return nil, "", nil
		}
	}
	return nil, "", fmt.Errorf("unsupported role %q or scope %q", role, scope)
}
