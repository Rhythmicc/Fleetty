package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func gpuMetricsError(commandErr error, pciRoot string) string {
	// Missing tools or broken drivers do not prove that hardware is absent.
	// Clear the error only when Linux's PCI inventory confirms there is no
	// NVIDIA display/3D controller. Unreadable inventories retain the warning.
	present, err := hasNVIDIAGPU(pciRoot)
	if err == nil && !present {
		return ""
	}
	return compactCommandError(commandErr)
}

func hasNVIDIAGPU(pciRoot string) (bool, error) {
	devices, err := os.ReadDir(pciRoot)
	if err != nil {
		return false, err
	}
	for _, device := range devices {
		class, err := readPCIAttribute(filepath.Join(pciRoot, device.Name(), "class"), 24)
		if err != nil {
			return false, err
		}
		if class>>16 != 0x03 {
			continue
		}
		vendor, err := readPCIAttribute(filepath.Join(pciRoot, device.Name(), "vendor"), 16)
		if err != nil {
			return false, err
		}
		if vendor == 0x10de {
			return true, nil
		}
	}
	return false, nil
}

func readPCIAttribute(path string, bits int) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(data)), 0, bits)
}
