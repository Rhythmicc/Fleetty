// Package releaseasset downloads and verifies Fleetty release artifacts.
package releaseasset

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	MaxChecksumsSize    = 2 << 20
	maxReleaseAssetSize = 256 << 20
	maxReleaseRedirects = 5
)

func ValidateBaseURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("parse release base_url: %w", err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return errors.New("release base_url must be an absolute HTTPS URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("release base_url must not contain credentials, a query, or a fragment")
	}
	return nil
}

func NewClient() *http.Client {
	return &http.Client{
		Timeout: 3 * time.Minute,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= maxReleaseRedirects {
				return errors.New("too many release download redirects")
			}
			if request.URL.Scheme != "https" {
				return errors.New("release download redirected away from HTTPS")
			}
			return nil
		},
	}
}

func Bytes(ctx context.Context, client *http.Client, address string, maximum int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "fleetty-release-updater")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status %s", response.Status)
	}
	if response.ContentLength > maximum {
		return nil, fmt.Errorf("download exceeds %d bytes", maximum)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("download exceeds %d bytes", maximum)
	}
	return data, nil
}

func Checksums(data []byte) (map[string]string, error) {
	checksums := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || !validSHA256(fields[0]) {
			return nil, fmt.Errorf("invalid release checksum line %q", scanner.Text())
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, "/\\") {
			return nil, fmt.Errorf("invalid release asset name %q", name)
		}
		if _, exists := checksums[name]; exists {
			return nil, fmt.Errorf("duplicate checksum for %s", name)
		}
		checksums[name] = strings.ToLower(fields[0])
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(checksums) == 0 {
		return nil, errors.New("release checksums are empty")
	}
	return checksums, nil
}

func Asset(
	ctx context.Context,
	client *http.Client,
	address, cacheDir, asset, expectedHash string,
) (string, error) {
	if filepath.Base(asset) != asset || strings.ContainsAny(asset, "/\\") || asset == "." || asset == ".." || asset == "" || !validSHA256(expectedHash) {
		return "", errors.New("invalid release asset or checksum")
	}
	destination := filepath.Join(cacheDir, asset)
	if info, err := os.Lstat(destination); err == nil {
		if info.Mode().IsRegular() {
			actual, hashErr := FileSHA256(destination)
			if hashErr == nil && actual == expectedHash {
				if chmodErr := os.Chmod(destination, 0o700); chmodErr != nil {
					return "", chmodErr
				}
				return destination, nil
			}
		}
		if removeErr := os.Remove(destination); removeErr != nil {
			return "", fmt.Errorf("replace invalid cached release asset: %w", removeErr)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "fleetty-release-updater")
	request.Header.Set("Accept", "application/octet-stream")
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", asset, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: unexpected HTTP status %s", asset, response.Status)
	}
	if response.ContentLength > maxReleaseAssetSize {
		return "", fmt.Errorf("download %s exceeds %d bytes", asset, maxReleaseAssetSize)
	}
	temporary, err := os.CreateTemp(cacheDir, "."+asset+"-*")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o700); err != nil {
		return "", err
	}
	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, digest), io.LimitReader(response.Body, maxReleaseAssetSize+1))
	if err != nil {
		return "", fmt.Errorf("download %s: %w", asset, err)
	}
	if written > maxReleaseAssetSize {
		return "", fmt.Errorf("download %s exceeds %d bytes", asset, maxReleaseAssetSize)
	}
	actualHash := hex.EncodeToString(digest.Sum(nil))
	if actualHash != expectedHash {
		return "", fmt.Errorf("download %s checksum mismatch", asset)
	}
	if err := temporary.Sync(); err != nil {
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return "", err
	}
	committed = true
	return destination, nil
}

func FileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
