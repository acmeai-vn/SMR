// Package config defines the SMR YAML schema and its safe persistence rules.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

var Tiers = []string{"easy", "lower_medium", "upper_medium", "hard"}

type Config struct {
	Listen                 string             `yaml:"listen" json:"listen"`
	UIListen               string             `yaml:"ui_listen" json:"ui_listen"`
	ChatCompletionsProfile string             `yaml:"chat_completions_profile,omitempty" json:"chat_completions_profile"`
	Classifier             Classifier         `yaml:"classifier" json:"classifier"`
	Profiles               map[string]Profile `yaml:"profiles" json:"profiles"`
}

type Classifier struct {
	BaseURL          string `yaml:"base_url" json:"base_url"`
	APIKey           string `yaml:"api_key" json:"-"`
	TimeoutMS        int    `yaml:"timeout_ms" json:"timeout_ms"`
	MaxChars         int    `yaml:"max_chars" json:"max_chars"`
	Retry            int    `yaml:"retry" json:"retry"`
	OnTransientError string `yaml:"on_transient_error" json:"on_transient_error"`
	FallbackTier     string `yaml:"fallback_tier" json:"fallback_tier"`
}

type Profile struct {
	API              string          `yaml:"api" json:"api"`
	Auth             string          `yaml:"auth" json:"auth"`
	AnthropicVersion string          `yaml:"anthropic_version,omitempty" json:"anthropic_version,omitempty"`
	Tiers            map[string]Tier `yaml:"tiers" json:"tiers"`
}

type Tier struct {
	BaseURL string `yaml:"base_url" json:"base_url"`
	APIKey  string `yaml:"api_key" json:"-"`
	Model   string `yaml:"model" json:"model"`
}

func Default() Config {
	return Config{
		Listen:                 "127.0.0.1:8787",
		UIListen:               "127.0.0.1:8788",
		ChatCompletionsProfile: "codex",
		Classifier: Classifier{
			BaseURL:          "https://api.acmeai.vn",
			TimeoutMS:        15000,
			MaxChars:         12000,
			Retry:            1,
			OnTransientError: "fallback",
			FallbackTier:     "upper_medium",
		},
		Profiles: map[string]Profile{
			"codex": {
				API:   "responses",
				Auth:  "bearer",
				Tiers: emptyTiers(),
			},
			"claude": {
				API:              "messages",
				Auth:             "x-api-key",
				AnthropicVersion: "2023-06-01",
				Tiers:            emptyTiers(),
			},
		},
	}
}

func emptyTiers() map[string]Tier {
	tiers := make(map[string]Tier, len(Tiers))
	for _, tier := range Tiers {
		tiers[tier] = Tier{}
	}
	return tiers
}

// Load reads config from disk. A missing file yields defaults so the setup UI
// can run before the user has entered any provider credentials.
func Load(path string) (Config, error) {
	cfg := Default()
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		applyEnv(&cfg)
		return cfg, cfg.Validate()
	}
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	normalize(&cfg)
	applyEnv(&cfg)
	return cfg, cfg.Validate()
}

func normalize(cfg *Config) {
	defaults := Default()
	if cfg.Listen == "" {
		cfg.Listen = defaults.Listen
	}
	if cfg.UIListen == "" {
		cfg.UIListen = defaults.UIListen
	}
	if cfg.ChatCompletionsProfile == "" {
		cfg.ChatCompletionsProfile = defaults.ChatCompletionsProfile
	}
	if cfg.Classifier.BaseURL == "" {
		cfg.Classifier.BaseURL = defaults.Classifier.BaseURL
	}
	if cfg.Classifier.TimeoutMS == 0 {
		cfg.Classifier.TimeoutMS = defaults.Classifier.TimeoutMS
	}
	if cfg.Classifier.MaxChars == 0 {
		cfg.Classifier.MaxChars = defaults.Classifier.MaxChars
	}
	if cfg.Classifier.OnTransientError == "" {
		cfg.Classifier.OnTransientError = defaults.Classifier.OnTransientError
	}
	if cfg.Classifier.FallbackTier == "" {
		cfg.Classifier.FallbackTier = defaults.Classifier.FallbackTier
	}
	if cfg.Profiles == nil {
		cfg.Profiles = defaults.Profiles
	}
	for name, fallback := range defaults.Profiles {
		profile, ok := cfg.Profiles[name]
		if !ok {
			cfg.Profiles[name] = fallback
			continue
		}
		if profile.API == "" {
			profile.API = fallback.API
		}
		if profile.Auth == "" {
			profile.Auth = fallback.Auth
		}
		if profile.AnthropicVersion == "" {
			profile.AnthropicVersion = fallback.AnthropicVersion
		}
		if profile.Tiers == nil {
			profile.Tiers = emptyTiers()
		}
		for _, tier := range Tiers {
			if _, ok := profile.Tiers[tier]; !ok {
				profile.Tiers[tier] = Tier{}
			}
		}
		cfg.Profiles[name] = profile
	}
}

func applyEnv(cfg *Config) {
	if key := strings.TrimSpace(os.Getenv("ACMEAI_API_KEY")); key != "" {
		cfg.Classifier.APIKey = key
	}
}

func (c Config) Validate() error {
	if err := validateLoopback(c.Listen, "listen"); err != nil {
		return err
	}
	if err := validateLoopback(c.UIListen, "ui_listen"); err != nil {
		return err
	}
	if c.Listen == c.UIListen {
		return fmt.Errorf("config: listen and ui_listen must differ")
	}
	if c.ChatCompletionsProfile == "" {
		return fmt.Errorf("config: chat_completions_profile is required")
	}
	if _, ok := c.Profiles[c.ChatCompletionsProfile]; !ok {
		return fmt.Errorf("config: chat_completions_profile %q is not configured", c.ChatCompletionsProfile)
	}
	if _, err := parseBaseURL(c.Classifier.BaseURL, "classifier.base_url"); err != nil {
		return err
	}
	if c.Classifier.TimeoutMS < 1000 || c.Classifier.TimeoutMS > 120000 {
		return fmt.Errorf("config: classifier.timeout_ms must be between 1000 and 120000")
	}
	if c.Classifier.MaxChars < 1000 || c.Classifier.MaxChars > 100000 {
		return fmt.Errorf("config: classifier.max_chars must be between 1000 and 100000")
	}
	if c.Classifier.Retry < 0 || c.Classifier.Retry > 3 {
		return fmt.Errorf("config: classifier.retry must be between 0 and 3")
	}
	switch c.Classifier.OnTransientError {
	case "fallback", "fail":
	default:
		return fmt.Errorf("config: classifier.on_transient_error must be fallback or fail")
	}
	if !IsTier(c.Classifier.FallbackTier) {
		return fmt.Errorf("config: classifier.fallback_tier must be one of %s", strings.Join(Tiers, ", "))
	}
	for name, profile := range c.Profiles {
		switch profile.Auth {
		case "bearer", "x-api-key":
		default:
			return fmt.Errorf("config: profiles.%s.auth must be bearer or x-api-key", name)
		}
		for _, tier := range Tiers {
			entry := profile.Tiers[tier]
			if entry.BaseURL != "" {
				if _, err := parseBaseURL(entry.BaseURL, "profiles."+name+".tiers."+tier+".base_url"); err != nil {
					return err
				}
			}
			if (entry.BaseURL == "") != (entry.Model == "") {
				return fmt.Errorf("config: profiles.%s.tiers.%s needs both base_url and model", name, tier)
			}
		}
	}
	return nil
}

func validateLoopback(addr, field string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("config: %s must be host:port: %w", field, err)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("config: %s must bind to loopback, got %q", field, host)
	}
	return nil
}

func parseBaseURL(value, field string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if !strings.HasPrefix(value, "https://") && !strings.HasPrefix(value, "http://") {
		return "", fmt.Errorf("config: %s must start with http:// or https://", field)
	}
	return value, nil
}

func IsTier(value string) bool {
	for _, tier := range Tiers {
		if tier == value {
			return true
		}
	}
	return false
}

// Save writes atomically with user-only permissions.
func Save(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".config-*.yaml")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
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
	if err := os.Rename(tempName, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// Manager serializes config reads and writes between the proxy and local UI.
type Manager struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

func LoadManager(path string) (*Manager, error) {
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}
	return &Manager{path: path, cfg: cfg}, nil
}

func (m *Manager) Path() string { return m.path }

func (m *Manager) Snapshot() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return clone(m.cfg)
}

func (m *Manager) Update(cfg Config) error {
	normalize(&cfg)
	persist := clone(cfg)
	// ACMEAI_API_KEY is intentionally an environment override. Never copy the
	// environment secret into a file just because the user saved another field.
	if strings.TrimSpace(os.Getenv("ACMEAI_API_KEY")) != "" {
		persist.Classifier.APIKey = ""
	}
	if err := Save(m.path, persist); err != nil {
		return err
	}
	m.mu.Lock()
	m.cfg = clone(cfg)
	m.mu.Unlock()
	return nil
}

func clone(cfg Config) Config {
	out := cfg
	out.Profiles = make(map[string]Profile, len(cfg.Profiles))
	for name, profile := range cfg.Profiles {
		copyProfile := profile
		copyProfile.Tiers = make(map[string]Tier, len(profile.Tiers))
		for tier, entry := range profile.Tiers {
			copyProfile.Tiers[tier] = entry
		}
		out.Profiles[name] = copyProfile
	}
	return out
}
