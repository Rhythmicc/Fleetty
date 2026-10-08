//go:build !linux

package main

import (
	"context"
	"fmt"
)

func readLinuxConnectionCounters(context.Context) (map[string]connectionNetworkCounters, error) {
	return nil, fmt.Errorf("Linux socket diagnostics require Linux")
}
