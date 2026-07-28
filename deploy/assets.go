package deployassets

import (
	_ "embed"
	"fmt"
)

//go:embed fleetty.service
var nodeService []byte

//go:embed fleetty-hub.service
var hubService []byte

func ServiceUnit(role string) ([]byte, string, error) {
	switch role {
	case "node":
		return append([]byte(nil), nodeService...), "fleetty.service", nil
	case "hub":
		return append([]byte(nil), hubService...), "fleetty-hub.service", nil
	default:
		return nil, "", fmt.Errorf("unsupported role %q", role)
	}
}
