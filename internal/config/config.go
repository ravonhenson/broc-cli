// Package config loads drillbit's per-repository YAML configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level drillbit configuration for one backup repository.
type Config struct {
	Name    string        `yaml:"name"`
	Backend string        `yaml:"backend"`
	Restic  ResticConfig  `yaml:"restic,omitempty"`
	Scratch ScratchConfig `yaml:"scratch"`
	Sample  SampleConfig  `yaml:"sample"`
	State   StateConfig   `yaml:"state"`
	Notify  NotifyConfig  `yaml:"notify,omitempty"`
}

// ResticConfig configures the restic backend. All fields are optional:
// unset values fall back to restic's own ambient environment variables
// (RESTIC_REPOSITORY, RESTIC_PASSWORD_COMMAND, ...), so drillbit works
// out of the box in an environment already set up to run restic.
type ResticConfig struct {
	Binary          string            `yaml:"binary,omitempty"`           // default: "restic"
	Repository      string            `yaml:"repository,omitempty"`       // -> RESTIC_REPOSITORY
	PasswordCommand string            `yaml:"password_command,omitempty"` // -> RESTIC_PASSWORD_COMMAND
	PasswordFile    string            `yaml:"password_file,omitempty"`    // -> RESTIC_PASSWORD_FILE
	ExtraArgs       []string          `yaml:"extra_args,omitempty"`       // appended to every invocation
	Env             map[string]string `yaml:"env,omitempty"`              // additional env vars (e.g. AWS creds)
}

// ScratchConfig controls where files are restored to during a drill.
type ScratchConfig struct {
	Dir  string `yaml:"dir"`            // default: os.TempDir()/drillbit/<repo>
	Keep bool   `yaml:"keep,omitempty"` // keep restored files after the run (debugging)
}

// SampleConfig controls how many/which files are checked per run.
type SampleConfig struct {
	FilesPerRun       int   `yaml:"files_per_run"`                 // default: 15
	MaxFileSizeMB     int64 `yaml:"max_file_size_mb,omitempty"`    // skip files larger than this; 0 = no limit
	ReverifyAfterDays int   `yaml:"reverify_after_days,omitempty"` // 0 = use default (30)
}

// StateConfig controls where the verification ledger is stored.
type StateConfig struct {
	Path string `yaml:"path"` // default: ~/.local/state/drillbit/<repo>.db
}

// NotifyConfig configures how results are surfaced loudly.
type NotifyConfig struct {
	Webhook     *WebhookConfig     `yaml:"webhook,omitempty"`
	Healthcheck *HealthcheckConfig `yaml:"healthcheck,omitempty"`
}

// WebhookConfig posts a JSON run report to an arbitrary URL.
type WebhookConfig struct {
	URL       string `yaml:"url"`
	OnSuccess bool   `yaml:"on_success,omitempty"` // default true: post every run, not just failures
}

// HealthcheckConfig pings a healthchecks.io-compatible endpoint
// (healthchecks.io, cronitor, etc: GET base on success, GET base/fail on
// failure, GET base/start at run start).
type HealthcheckConfig struct {
	PingURL string `yaml:"ping_url"`
}

// ApplyDefaults fills in any unset fields after a Config has been
// constructed or loaded, deriving default paths from the repo name.
func (c *Config) ApplyDefaults() {
	if c.Backend == "" {
		c.Backend = "restic"
	}
	if c.Restic.Binary == "" {
		c.Restic.Binary = "restic"
	}
	if c.Sample.FilesPerRun <= 0 {
		c.Sample.FilesPerRun = 15
	}
	if c.Sample.ReverifyAfterDays <= 0 {
		c.Sample.ReverifyAfterDays = 30
	}
	if c.Scratch.Dir == "" {
		c.Scratch.Dir = filepath.Join(os.TempDir(), "drillbit", safeName(c.Name))
	}
	if c.State.Path == "" {
		home, err := os.UserHomeDir()
		base := "."
		if err == nil {
			base = filepath.Join(home, ".local", "state", "drillbit")
		}
		c.State.Path = filepath.Join(base, safeName(c.Name)+".db")
	}
}

func safeName(name string) string {
	if name == "" {
		return "default"
	}
	return name
}

// ReverifyAfter returns the configured reverification interval as a duration.
func (c *Config) ReverifyAfter() time.Duration {
	return time.Duration(c.Sample.ReverifyAfterDays) * 24 * time.Hour
}

// MaxFileSize returns the configured max file size in bytes, or 0 for no limit.
func (c *Config) MaxFileSize() int64 {
	return c.Sample.MaxFileSizeMB * 1024 * 1024
}

// Load reads and validates a drillbit config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	c.ApplyDefaults()
	return &c, nil
}

// Validate checks required fields before defaults are applied.
func (c *Config) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("name is required")
	}
	return nil
}

// Save writes the config to path as YAML, creating parent directories.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
