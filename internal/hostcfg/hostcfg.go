// Package hostcfg detects Codex and Claude Code configuration and renders the
// exact blocks a user must add to point a host at SMR. It never writes those
// files: the user makes the edit and checks the result.
package hostcfg

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	Codex  = "codex"
	Claude = "claude"

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

// Part is one copyable piece of host configuration. Codex needs two: a
// top-level key that must sit above every table, and the provider block.
type Part struct {
	TitleKey string
	Code     string
}

// Instructions is the host configuration a user must add by hand.
type Instructions struct {
	Host   string
	Path   string
	Exists bool
	Parts  []Part
}

// InstructionsFor builds the blocks for one host. SMR never writes host
// configuration: a second model_provider or a leftover ANTHROPIC_API_KEY
// breaks the agent in ways the installer cannot see, and only the user knows
// which provider should stay the default. Showing exact blocks keeps every
// change visible and leaves the edit with the person who owns the file.
func InstructionsFor(host, listen string) (Instructions, error) {
	path, err := HostPath(host)
	if err != nil {
		return Instructions{}, err
	}
	listen = strings.TrimSpace(listen)
	if listen == "" {
		listen = defaultListen
	}
	base := "http://" + listen
	exists := false
	if _, err := os.Stat(path); err == nil {
		exists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return Instructions{}, err
	}
	result := Instructions{Host: host, Path: path, Exists: exists}
	switch host {
	case Codex:
		result.Parts = []Part{
			{
				TitleKey: "step3.codex.default",
				Code:     `model_provider = "smr"`,
			},
			{
				TitleKey: "step3.codex.block",
				Code: strings.Join([]string{
					"[model_providers.smr]",
					`name = "Smart Model Router"`,
					`base_url = "` + base + `/v1"`,
					`wire_api = "responses"`,
				}, "\n"),
			},
		}
	case Claude:
		// Only the two entries, not a whole document: settings.json usually
		// already has other settings, and pasting a complete {"env": {...}}
		// over it would delete them.
		result.Parts = []Part{{TitleKey: "step3.claude.block", Code: strings.Join([]string{
			`"ANTHROPIC_BASE_URL": "` + base + `",`,
			`"ANTHROPIC_AUTH_TOKEN": "` + claudePlaceholder + `"`,
		}, "\n")}}
	default:
		return Instructions{}, fmt.Errorf("unknown host %q", host)
	}
	return result, nil
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
