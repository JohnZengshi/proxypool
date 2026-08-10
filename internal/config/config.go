package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"gopkg.in/yaml.v3"
)

// Source types.
const (
	SourceClash    = "clash"
	SourceSingBox  = "singbox"
	SourceVPNCheap = "vpncheap"
)

// Source is one subscription feeding the pool. Every node parsed from a
// source carries its Tag so the pool can show where a node came from.
type Source struct {
	Tag  string `yaml:"tag"`
	Type string `yaml:"type"`
	URL  string `yaml:"url"`
	Path string `yaml:"path"`
}

// Config holds all runtime configuration.
type Config struct {
	// SubscriptionURL is the legacy single-source field. When set and
	// Sources is empty it is promoted to one clash source tagged "default".
	SubscriptionURL    string        `yaml:"subscription_url"`
	Sources            []Source      `yaml:"sources"`
	Bind               string        `yaml:"bind"`
	BasePort           int           `yaml:"base_port"`
	StatusPort         int           `yaml:"status_port"`
	StateFile          string        `yaml:"state_file"`
	ProbeURLs          []string      `yaml:"probe_urls"`
	ProbeTimeout       time.Duration `yaml:"probe_timeout"`
	HealthInterval     time.Duration `yaml:"health_interval"`
	RefreshInterval    time.Duration `yaml:"refresh_interval"`
	DialTimeout        time.Duration `yaml:"dial_timeout"`
	MaxConcurrentProbe int           `yaml:"max_concurrent_probe"`
	LogRequests        bool          `yaml:"log_requests"`
	LogFormat          string        `yaml:"log_format"`

	logRequestsSet bool `yaml:"-"`
}

func defaults() Config {
	return Config{
		Bind:               "127.0.0.1",
		BasePort:           18081,
		StatusPort:         18080,
		StateFile:          "./state.json",
		ProbeURLs:          []string{"https://api.ipify.org", "https://ifconfig.me/ip"},
		ProbeTimeout:       10 * time.Second,
		HealthInterval:     20 * time.Second,
		RefreshInterval:    6 * time.Hour,
		DialTimeout:        10 * time.Second,
		MaxConcurrentProbe: 8,
		LogRequests:        true,
		LogFormat:          "text",
	}
}

// Load reads a YAML config file, merges defaults, and validates.
func Load(path string) (*Config, error) {
	cfg := defaults()

	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config %s: %w", path, err)
		}
		var probe map[string]any
		if err := yaml.Unmarshal(raw, &probe); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
		if _, ok := probe["log_requests"]; ok {
			cfg.logRequestsSet = true
		}
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	// Re-apply defaults for zero values.
	d := defaults()
	if cfg.Bind == "" {
		cfg.Bind = d.Bind
	}
	if cfg.BasePort == 0 {
		cfg.BasePort = d.BasePort
	}
	if cfg.StatusPort == 0 {
		cfg.StatusPort = d.StatusPort
	}
	if cfg.StateFile == "" {
		cfg.StateFile = d.StateFile
	}
	if len(cfg.ProbeURLs) == 0 {
		cfg.ProbeURLs = d.ProbeURLs
	}
	if cfg.ProbeTimeout == 0 {
		cfg.ProbeTimeout = d.ProbeTimeout
	}
	if cfg.HealthInterval == 0 {
		cfg.HealthInterval = d.HealthInterval
	}
	if cfg.RefreshInterval == 0 {
		cfg.RefreshInterval = d.RefreshInterval
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = d.DialTimeout
	}
	if cfg.MaxConcurrentProbe == 0 {
		cfg.MaxConcurrentProbe = d.MaxConcurrentProbe
	}
	if !cfg.logRequestsSet {
		cfg.LogRequests = d.LogRequests
	}
	if cfg.LogFormat == "" {
		cfg.LogFormat = d.LogFormat
	}

	cfg.Normalize()

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Normalize promotes the legacy subscription_url into Sources and fills
// per-source defaults. Safe to call more than once.
func (c *Config) Normalize() {
	c.normalizeOn(runtime.GOOS)
}

func (c *Config) normalizeOn(goos string) {
	if len(c.Sources) == 0 && c.SubscriptionURL != "" {
		c.Sources = []Source{{Tag: "default", Type: SourceClash, URL: c.SubscriptionURL}}
	}
	for i := range c.Sources {
		if c.Sources[i].Type == "" {
			c.Sources[i].Type = SourceClash
		}
		if c.Sources[i].Type == SourceVPNCheap && goos == "windows" && c.Sources[i].Path == "" {
			if appData := os.Getenv("APPDATA"); appData != "" {
				c.Sources[i].Path = filepath.Join(appData, "vpncheap", "app_state.json")
			}
		}
	}
}

// Validate checks required fields and constraints.
func (c *Config) Validate() error {
	return c.validateOn(runtime.GOOS)
}

func (c *Config) validateOn(goos string) error {
	if len(c.Sources) == 0 {
		return fmt.Errorf("at least one source is required: set sources or subscription_url")
	}
	seen := make(map[string]bool, len(c.Sources))
	for i, s := range c.Sources {
		if s.Tag == "" {
			return fmt.Errorf("sources[%d]: tag must not be empty", i)
		}
		if seen[s.Tag] {
			return fmt.Errorf("sources[%d]: duplicate tag %q", i, s.Tag)
		}
		seen[s.Tag] = true
		if s.Type != SourceClash && s.Type != SourceSingBox && s.Type != SourceVPNCheap {
			return fmt.Errorf("sources[%d] (tag %q): type must be %s, %s, or %s, got %q", i, s.Tag, SourceClash, SourceSingBox, SourceVPNCheap, s.Type)
		}
		switch {
		case s.Type == SourceVPNCheap && goos == "windows":
			if s.Path == "" {
				return fmt.Errorf("sources[%d] (tag %q): path must not be empty for vpncheap on windows", i, s.Tag)
			}
			if hasScheme(s.Path, "http", "https") {
				return fmt.Errorf("sources[%d] (tag %q): path must be a filesystem path, not a URL", i, s.Tag)
			}
		case s.Type == SourceVPNCheap && goos == "darwin":
			if s.URL == "" {
				return fmt.Errorf("sources[%d] (tag %q): url must not be empty for vpncheap on darwin", i, s.Tag)
			}
			if !hasScheme(s.URL, "http", "https") {
				return fmt.Errorf("sources[%d] (tag %q): url must be http or https", i, s.Tag)
			}
		case s.Type != SourceVPNCheap:
			if s.URL == "" {
				return fmt.Errorf("sources[%d] (tag %q): url must not be empty", i, s.Tag)
			}
			if !hasScheme(s.URL, "http", "https") {
				return fmt.Errorf("sources[%d] (tag %q): url must be http or https", i, s.Tag)
			}
		}
	}
	if c.BasePort < 1024 || c.BasePort > 65000 {
		return fmt.Errorf("base_port must be between 1024 and 65000, got %d", c.BasePort)
	}
	if net.ParseIP(c.Bind) == nil {
		return fmt.Errorf("bind must be a valid IP address, got %q", c.Bind)
	}
	if c.LogFormat != "text" && c.LogFormat != "json" {
		return fmt.Errorf("log_format must be text or json, got %q", c.LogFormat)
	}
	return nil
}

func hasScheme(url string, schemes ...string) bool {
	for _, s := range schemes {
		if len(url) > len(s)+3 && url[:len(s)] == s && url[len(s):len(s)+3] == "://" {
			return true
		}
	}
	return false
}
