package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// AccessKeyPrefix marks SMR access keys. The 24 random bytes encode to exactly
// 32 base64url characters, which is easy to recognise in a config file without
// making the key long enough to be painful to paste on a phone.
const AccessKeyPrefix = "smr_lan_"

// AccessKey authenticates one device that uses this SMR proxy. The key is
// stored in plaintext for the same reason provider keys are: the config file is
// already 0600 and readable only by the account that owns them. Hashing would
// not protect anything a reader of this file cannot already take.
type AccessKey struct {
	Name      string `yaml:"name" json:"name"`
	Key       string `yaml:"key" json:"key"`
	CreatedAt string `yaml:"created_at,omitempty" json:"created_at,omitempty"`
}

// GenerateAccessKey returns a new random key and its creation time.
func GenerateAccessKey() (AccessKey, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return AccessKey{}, fmt.Errorf("generate access key: %w", err)
	}
	return AccessKey{
		Key:       AccessKeyPrefix + base64.RawURLEncoding.EncodeToString(raw),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// ValidateAccessKeyName rejects names that cannot be typed or copied reliably.
func ValidateAccessKeyName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("config: access key name is required")
	}
	if utf8.RuneCountInString(name) > 64 {
		return errors.New("config: access key name must be at most 64 characters")
	}
	for _, char := range name {
		if char < 0x20 || char == 0x7f {
			return errors.New("config: access key name must not contain control characters")
		}
	}
	return nil
}

func validateAccessKeyKey(key string) error {
	body := strings.TrimPrefix(key, AccessKeyPrefix)
	if !strings.HasPrefix(key, AccessKeyPrefix) || len(body) != 32 {
		return fmt.Errorf("config: access key %q is not in the smr_lan_<32 base64url> format", key)
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil || len(raw) != 24 {
		return fmt.Errorf("config: access key %q is not valid base64url", key)
	}
	return nil
}

// AccessKey returns the key with the given name.
func (c Config) AccessKey(name string) (AccessKey, bool) {
	name = strings.TrimSpace(name)
	for _, key := range c.AccessKeys {
		if key.Name == name {
			return key, true
		}
	}
	return AccessKey{}, false
}

// LocalAccessKey is the key shown to the machine that runs the daemon. It is
// the one named "local" when present, otherwise the first configured key.
func (c Config) LocalAccessKey() (AccessKey, bool) {
	if key, ok := c.AccessKey("local"); ok {
		return key, true
	}
	if len(c.AccessKeys) > 0 {
		return c.AccessKeys[0], true
	}
	return AccessKey{}, false
}

// AddAccessKey appends a key and rejects duplicate names.
func (c *Config) AddAccessKey(key AccessKey) error {
	key.Name = strings.TrimSpace(key.Name)
	if err := ValidateAccessKeyName(key.Name); err != nil {
		return err
	}
	if err := validateAccessKeyKey(key.Key); err != nil {
		return err
	}
	if _, exists := c.AccessKey(key.Name); exists {
		return fmt.Errorf("config: access key %q already exists", key.Name)
	}
	c.AccessKeys = append(c.AccessKeys, key)
	return nil
}

// RemoveAccessKey deletes one key by name.
func (c *Config) RemoveAccessKey(name string) bool {
	name = strings.TrimSpace(name)
	for index, key := range c.AccessKeys {
		if key.Name == name {
			c.AccessKeys = append(c.AccessKeys[:index], c.AccessKeys[index+1:]...)
			return true
		}
	}
	return false
}

// DescribeAccessKey is the short form shown in listings. It is not a secret:
// the config file remains the only place that stores the whole key.
func DescribeAccessKey(key string) string {
	if len(key) <= 16 {
		return key
	}
	return key[:12] + "..." + key[len(key)-4:]
}
