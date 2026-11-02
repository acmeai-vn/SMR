// Package hostcfg manages the small, reviewable changes SMR makes to Codex and
// Claude Code configuration. Every change is backed up before it is written.
package hostcfg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/acmeai-vn/SMR/internal/fileperm"
)

const (
	Codex  = "codex"
	Claude = "claude"

	codexMarkerStart  = "# >>> SMR managed block"
	codexMarkerEnd    = "# <<< SMR managed block"
	claudePlaceholder = "smr-local-placeholder"
	defaultListen     = "127.0.0.1:8787"
)

type Detection struct {
	Host             string `json:"host"`
	Path             string `json:"path"`
	Exists           bool   `json:"exists"`
	Provider         string `json:"provider,omitempty"`
	BaseURL          string `json:"base_url,omitempty"`
	Model            string `json:"model,omitempty"`
	APIKeyConfigured bool   `json:"api_key_configured"`
	// Notes holds stable codes, not sentences, so every surface can localise
	// them. A code may carry one argument after a colon, such as
	// "codex.env_key:OPENAI_API_KEY".
	Notes []string `json:"notes,omitempty"`
}

type Change struct {
	Host         string `json:"host"`
	Path         string `json:"path"`
	Before       string `json:"before"`
	After        string `json:"after"`
	Diff         string `json:"diff"`
	Existed      bool   `json:"existed"`
	ExpectedHash string `json:"expected_hash"`
}

type backupMeta struct {
	Host       string `json:"host"`
	Path       string `json:"path"`
	Existed    bool   `json:"existed"`
	BackupFile string `json:"backup_file,omitempty"`
	CreatedAt  string `json:"created_at"`
}

func CodexPath() (string, error) {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return filepath.Join(home, "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "config.toml"), nil
}

func ClaudePath() (string, error) {
	if home := os.Getenv("CLAUDE_CONFIG_DIR"); home != "" {
		return filepath.Join(home, "settings.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

func HostPath(host string) (string, error) {
	switch host {
	case Codex:
		return CodexPath()
	case Claude:
		return ClaudePath()
	default:
		return "", fmt.Errorf("unknown host %q", host)
	}
}

func Detect() []Detection {
	codex, _ := DetectCodex()
	claude, _ := DetectClaude()
	return []Detection{codex, claude}
}

func DetectCodex() (Detection, error) {
	path, err := CodexPath()
	if err != nil {
		return Detection{Host: Codex}, err
	}
	detection := Detection{Host: Codex, Path: path}
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		detection.Notes = append(detection.Notes, "codex.no_config")
		return detection, nil
	}
	if err != nil {
		return detection, err
	}
	detection.Exists = true
	lines := strings.Split(string(body), "\n")
	section := ""
	provider := ""
	baseURL := ""
	model := ""
	envKey := ""
	for _, raw := range lines {
		line := strings.TrimSpace(stripComment(raw))
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = unquote(strings.TrimSpace(value))
		if section == "" && key == "model" {
			model = value
		}
		if strings.HasPrefix(section, "model_providers.") {
			name := strings.TrimPrefix(section, "model_providers.")
			switch key {
			case "base_url":
				provider, baseURL = name, value
			case "env_key":
				envKey = value
			}
		}
	}
	detection.Provider = provider
	detection.BaseURL = baseURL
	detection.Model = model
	if envKey != "" && os.Getenv(envKey) != "" {
		detection.APIKeyConfigured = true
		detection.Notes = append(detection.Notes, "codex.env_key:"+envKey)
	}
	if provider == "" {
		detection.Notes = append(detection.Notes, "codex.no_provider")
	}
	return detection, nil
}

func DetectClaude() (Detection, error) {
	path, err := ClaudePath()
	if err != nil {
		return Detection{Host: Claude}, err
	}
	detection := Detection{Host: Claude, Path: path}
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		detection.Notes = append(detection.Notes, "claude.no_settings")
		return detection, nil
	}
	if err != nil {
		return detection, err
	}
	detection.Exists = true
	var settings map[string]any
	if err := json.Unmarshal(body, &settings); err != nil {
		detection.Notes = append(detection.Notes, "claude.invalid_json")
		return detection, nil
	}
	env, _ := settings["env"].(map[string]any)
	if env != nil {
		detection.BaseURL, _ = env["ANTHROPIC_BASE_URL"].(string)
		_, hasKey := env["ANTHROPIC_API_KEY"].(string)
		_, hasToken := env["ANTHROPIC_AUTH_TOKEN"].(string)
		detection.APIKeyConfigured = hasKey || hasToken
	}
	if detection.BaseURL == "" {
		detection.Notes = append(detection.Notes, "claude.no_base_url")
	}
	detection.Notes = append(detection.Notes, "claude.needs_key")
	return detection, nil
}

func Preview(host, dataDir string) (Change, error) {
	return PreviewFor(host, dataDir, defaultListen)
}

// PreviewFor builds the proposed host config for the actual SMR listener. The
// setup UI must use this form when the user changes listen or ui_listen.
func PreviewFor(host, dataDir, listen string) (Change, error) {
	path, err := HostPath(host)
	if err != nil {
		return Change{}, err
	}
	listen = strings.TrimSpace(listen)
	if listen == "" {
		listen = defaultListen
	}
	beforeBytes, readErr := os.ReadFile(path)
	existed := readErr == nil
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return Change{}, readErr
	}
	before := string(beforeBytes)
	after, err := desired(host, before, existed, "http://"+listen)
	if err != nil {
		return Change{}, err
	}
	diff := LineDiff(before, after)
	sum := sha256.Sum256([]byte(host + "\x00" + path + "\x00" + before + "\x00" + after))
	return Change{
		Host:         host,
		Path:         path,
		Before:       before,
		After:        after,
		Diff:         diff,
		Existed:      existed,
		ExpectedHash: hex.EncodeToString(sum[:]),
	}, nil
}

func Apply(change Change, dataDir string) error {
	return ApplyFor(change, dataDir, defaultListen)
}

// ApplyFor applies a preview produced for the current SMR listener.
func ApplyFor(change Change, dataDir, listen string) error {
	current, err := PreviewFor(change.Host, dataDir, listen)
	if err != nil {
		return err
	}
	if change.ExpectedHash == "" || current.ExpectedHash != change.ExpectedHash {
		return fmt.Errorf("host config changed after preview; review the diff again")
	}
	if _, err := writeBackup(current, dataDir); err != nil {
		return err
	}
	return writeFileAtomic(current.Path, []byte(current.After))
}

func desired(host, before string, existed bool, baseURL string) (string, error) {
	switch host {
	case Codex:
		return desiredCodex(before, existed, baseURL)
	case Claude:
		return desiredClaude(before, existed, baseURL)
	default:
		return "", fmt.Errorf("unknown host %q", host)
	}
}

func desiredCodex(before string, existed bool, baseURL string) (string, error) {
	newline := "\n"
	if strings.Contains(before, "\r\n") {
		newline = "\r\n"
	}
	block := strings.Join([]string{
		codexMarkerStart,
		"[model_providers.smr]",
		`name = "Smart Model Router"`,
		`base_url = "` + baseURL + `/v1"`,
		`wire_api = "responses"`,
		codexMarkerEnd,
	}, newline)
	start := strings.Index(before, codexMarkerStart)
	end := strings.Index(before, codexMarkerEnd)
	if (start >= 0) != (end >= 0) || (start >= 0 && end < start) {
		return "", fmt.Errorf("Codex config has an incomplete SMR marker block")
	}
	if start >= 0 {
		end += len(codexMarkerEnd)
		return before[:start] + block + before[end:], nil
	}
	trimmed := strings.TrimRight(before, "\r\n")
	if !existed || trimmed == "" {
		return block + newline, nil
	}
	return trimmed + newline + newline + block + newline, nil
}

func desiredClaude(before string, existed bool, baseURL string) (string, error) {
	settings := make(map[string]any)
	if existed && strings.TrimSpace(before) != "" {
		if err := json.Unmarshal([]byte(before), &settings); err != nil {
			return "", fmt.Errorf("Claude settings.json is not valid JSON; refusing to overwrite")
		}
	}
	env := make(map[string]any)
	if existing, ok := settings["env"].(map[string]any); ok {
		for key, value := range existing {
			env[key] = value
		}
	}
	env["ANTHROPIC_BASE_URL"] = baseURL
	env["ANTHROPIC_AUTH_TOKEN"] = claudePlaceholder
	settings["env"] = env
	body, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body) + "\n", nil
}

func lineKind(current, desired string) string {
	if current == "" {
		return "new"
	}
	if desired == current {
		return "same"
	}
	return "update"
}

func LineDiff(before, after string) string {
	oldLines := strings.Split(strings.ReplaceAll(before, "\r\n", "\n"), "\n")
	newLines := strings.Split(strings.ReplaceAll(after, "\r\n", "\n"), "\n")
	var out strings.Builder
	out.WriteString("--- current\n+++ proposed\n")
	n, m := len(oldLines), len(newLines)
	if n*m > 4_000_000 {
		for _, line := range oldLines {
			out.WriteString("- " + line + "\n")
		}
		for _, line := range newLines {
			out.WriteString("+ " + line + "\n")
		}
		return out.String()
	}
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case oldLines[i] == newLines[j]:
			out.WriteString("  " + oldLines[i] + "\n")
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			out.WriteString("- " + oldLines[i] + "\n")
			i++
		default:
			out.WriteString("+ " + newLines[j] + "\n")
			j++
		}
	}
	for ; i < n; i++ {
		out.WriteString("- " + oldLines[i] + "\n")
	}
	for ; j < m; j++ {
		out.WriteString("+ " + newLines[j] + "\n")
	}
	return out.String()
}

func writeBackup(change Change, dataDir string) (string, error) {
	hostDir := filepath.Join(dataDir, "backups", change.Host)
	if err := os.MkdirAll(hostDir, 0o700); err != nil {
		return "", err
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	meta := backupMeta{Host: change.Host, Path: change.Path, Existed: change.Existed, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if change.Existed {
		meta.BackupFile = stamp + ".bak"
		if err := writeFileAtomic(filepath.Join(hostDir, meta.BackupFile), []byte(change.Before)); err != nil {
			return "", err
		}
	}
	body, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return "", err
	}
	metaPath := filepath.Join(hostDir, stamp+".json")
	if err := writeFileAtomic(metaPath, body); err != nil {
		return "", err
	}
	return metaPath, nil
}

func RestoreLatest(host, dataDir string) (string, error) {
	expectedPath, err := HostPath(host)
	if err != nil {
		return "", err
	}
	hostDir := filepath.Join(dataDir, "backups", host)
	entries, err := os.ReadDir(hostDir)
	if err != nil {
		return "", fmt.Errorf("no backup found for %s: %w", host, err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no backup found for %s", host)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, name := range names {
		body, err := os.ReadFile(filepath.Join(hostDir, name))
		if err != nil {
			continue
		}
		var meta backupMeta
		if err := json.Unmarshal(body, &meta); err != nil || meta.Path != expectedPath {
			continue
		}
		if meta.Existed {
			if meta.BackupFile == "" {
				continue
			}
			backup, err := os.ReadFile(filepath.Join(hostDir, meta.BackupFile))
			if err != nil {
				continue
			}
			if err := writeFileAtomic(meta.Path, backup); err != nil {
				return "", err
			}
			return meta.Path, nil
		}
		if err := os.Remove(meta.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		return meta.Path, nil
	}
	return "", fmt.Errorf("no usable backup found for %s", host)
}

func writeFileAtomic(path string, body []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".smr-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(body); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return fileperm.Restrict(path)
}

func stripComment(line string) string {
	inQuote := false
	for i, char := range line {
		if char == '"' {
			inQuote = !inQuote
		}
		if char == '#' && !inQuote {
			return line[:i]
		}
	}
	return line
}

func unquote(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
		return value[1 : len(value)-1]
	}
	return value
}
