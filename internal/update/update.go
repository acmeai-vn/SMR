// Package update implements the documented release-manifest update path. The
// phase 1 manifest is checksum-verified but not cryptographically signed.
package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const repo = "acmeai-vn/SMR"

type Manifest struct {
	Schema     int              `json:"schema"`
	Version    string           `json:"version"`
	ReleasedAt string           `json:"released_at"`
	Assets     map[string]Asset `json:"assets"`
}

type Asset struct {
	File   string `json:"file"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func Fetch(ctx context.Context, version string) (Manifest, error) {
	base := os.Getenv("SMR_UPDATE_BASE_URL")
	if base == "" {
		if version == "" {
			base = "https://github.com/" + repo + "/releases/latest/download"
		} else {
			if !strings.HasPrefix(version, "v") {
				version = "v" + version
			}
			base = "https://github.com/" + repo + "/releases/download/" + version
		}
	}
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/smr-release.json", nil)
	if err != nil {
		return Manifest{}, err
	}
	req.Header.Set("User-Agent", "SMR/2")
	resp, err := client.Do(req)
	if err != nil {
		return Manifest{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Manifest{}, fmt.Errorf("release manifest returned %d", resp.StatusCode)
	}
	var manifest Manifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&manifest); err != nil {
		return Manifest{}, err
	}
	if manifest.Schema != 1 || manifest.Version == "" {
		return Manifest{}, fmt.Errorf("unsupported release manifest")
	}
	if version != "" && manifest.Version != version {
		return Manifest{}, fmt.Errorf("manifest version %s does not match requested %s", manifest.Version, version)
	}
	return manifest, nil
}

func Install(ctx context.Context, manifest Manifest) error {
	key := runtime.GOOS + "-" + runtime.GOARCH
	asset, ok := manifest.Assets[key]
	if !ok {
		return fmt.Errorf("release has no asset for %s", key)
	}
	if asset.File == "" || asset.SHA256 == "" {
		return fmt.Errorf("release asset is missing file or sha256")
	}
	downloadURL := asset.URL
	if downloadURL == "" {
		base := os.Getenv("SMR_UPDATE_BASE_URL")
		if base == "" {
			base = "https://github.com/" + repo + "/releases/download/" + manifest.Version
		}
		downloadURL = strings.TrimRight(base, "/") + "/" + asset.File
	}
	tempDir, err := os.MkdirTemp("", "smr-update-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)
	archivePath := filepath.Join(tempDir, asset.File)
	if err := download(ctx, downloadURL, archivePath, asset.Size); err != nil {
		return err
	}
	sum, err := fileSHA256(archivePath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(sum, asset.SHA256) {
		return fmt.Errorf("checksum mismatch for %s", asset.File)
	}
	binary, err := extractBinary(archivePath, tempDir, runtime.GOOS)
	if err != nil {
		return err
	}
	current, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(current); err == nil {
		current = resolved
	}
	backup := current + ".old"
	_ = os.Remove(backup)
	if err := os.Rename(current, backup); err != nil {
		return fmt.Errorf("could not move the running binary aside: %w", err)
	}
	if err := copyFile(binary, current, 0o755); err != nil {
		_ = os.Rename(backup, current)
		return err
	}
	return nil
}

func download(ctx context.Context, url, destination string, expectedSize int64) error {
	client := &http.Client{Timeout: 2 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "SMR/2")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	limit := int64(256 << 20)
	if expectedSize > 0 && expectedSize < limit {
		limit = expectedSize + 1
	}
	written, err := io.Copy(file, io.LimitReader(resp.Body, limit))
	if err != nil {
		return err
	}
	if expectedSize > 0 && written != expectedSize {
		return fmt.Errorf("downloaded %d bytes, expected %d", written, expectedSize)
	}
	return file.Sync()
}

func extractBinary(archive, dir, goos string) (string, error) {
	if goos == "windows" {
		reader, err := zip.OpenReader(archive)
		if err != nil {
			return "", err
		}
		defer reader.Close()
		for _, file := range reader.File {
			if filepath.Base(file.Name) != "smr.exe" {
				continue
			}
			source, err := file.Open()
			if err != nil {
				return "", err
			}
			defer source.Close()
			target := filepath.Join(dir, "smr.exe")
			if err := writeFromReader(target, source, 0o755); err != nil {
				return "", err
			}
			return target, nil
		}
		return "", fmt.Errorf("archive has no smr.exe")
	}
	file, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return "", err
	}
	defer gz.Close()
	tarReader := tar.NewReader(gz)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if filepath.Base(header.Name) != "smr" {
			continue
		}
		target := filepath.Join(dir, "smr")
		if err := writeFromReader(target, tarReader, 0o755); err != nil {
			return "", err
		}
		return target, nil
	}
	return "", fmt.Errorf("archive has no smr binary")
}

func writeFromReader(path string, source io.Reader, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, io.LimitReader(source, 256<<20)); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func copyFile(source, destination string, mode os.FileMode) error {
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer file.Close()
	return writeFromReader(destination, file, mode)
}
