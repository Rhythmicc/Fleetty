//go:build linux || darwin

package main

import (
	byteutil "bytes"
	"errors"
	"fmt"
	"sort"

	"golang.org/x/sys/unix"
)

func storageExtendedMetadataEqual(first, second string) (bool, error) {
	firstAttributes, err := readStorageExtendedMetadata(first)
	if err != nil {
		return false, fmt.Errorf("inspect extended metadata for %s: %w", first, err)
	}
	secondAttributes, err := readStorageExtendedMetadata(second)
	if err != nil {
		return false, fmt.Errorf("inspect extended metadata for %s: %w", second, err)
	}
	if len(firstAttributes) != len(secondAttributes) {
		return false, nil
	}
	for name, firstValue := range firstAttributes {
		secondValue, ok := secondAttributes[name]
		if !ok || !byteutil.Equal(firstValue, secondValue) {
			return false, nil
		}
	}
	return true, nil
}

func readStorageExtendedMetadata(path string) (map[string][]byte, error) {
	size, err := unix.Listxattr(path, nil)
	if err != nil {
		if errors.Is(err, unix.ENOTSUP) {
			return map[string][]byte{}, nil
		}
		return nil, err
	}
	if size == 0 {
		return map[string][]byte{}, nil
	}
	var namesBuffer []byte
	listed := false
	for attempts := 0; attempts < 3; attempts++ {
		namesBuffer = make([]byte, size)
		read, listErr := unix.Listxattr(path, namesBuffer)
		if errors.Is(listErr, unix.ERANGE) {
			size, err = unix.Listxattr(path, nil)
			if err != nil {
				return nil, err
			}
			continue
		}
		if listErr != nil {
			return nil, listErr
		}
		namesBuffer = namesBuffer[:read]
		listed = true
		break
	}
	if !listed {
		return nil, errors.New("extended metadata changed repeatedly while listing")
	}
	if len(namesBuffer) == 0 {
		return map[string][]byte{}, nil
	}
	rawNames := byteutil.Split(namesBuffer, []byte{0})
	names := make([]string, 0, len(rawNames))
	for _, raw := range rawNames {
		if len(raw) > 0 {
			names = append(names, string(raw))
		}
	}
	sort.Strings(names)
	result := make(map[string][]byte, len(names))
	for _, name := range names {
		value, valueErr := readStorageExtendedAttribute(path, name)
		if valueErr != nil {
			return nil, valueErr
		}
		result[name] = value
	}
	return result, nil
}

func readStorageExtendedAttribute(path, name string) ([]byte, error) {
	size, err := unix.Getxattr(path, name, nil)
	if err != nil {
		return nil, err
	}
	if size == 0 {
		return []byte{}, nil
	}
	for attempts := 0; attempts < 3; attempts++ {
		value := make([]byte, size)
		read, getErr := unix.Getxattr(path, name, value)
		if errors.Is(getErr, unix.ERANGE) {
			size, err = unix.Getxattr(path, name, nil)
			if err != nil {
				return nil, err
			}
			continue
		}
		if getErr != nil {
			return nil, getErr
		}
		return value[:read], nil
	}
	return nil, errors.New("extended metadata changed repeatedly while reading")
}
