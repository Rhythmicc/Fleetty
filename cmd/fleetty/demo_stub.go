//go:build !demo

package main

import "io"

// dispatchDemo is the production stub. Screenshot fixtures are compiled only
// with -tags demo, so a release binary has no demo command.
func dispatchDemo(_ []string, _, _ io.Writer) (bool, error) {
	return false, nil
}
