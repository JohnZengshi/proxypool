package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	SubscriptionURL     string        `yaml:"subscription_url"`
	Sources             []Source      `yaml:"sources"`
	Bind                string        `yaml:"bind"`
	BasePort            int           `yaml:"base_port"`
	StatusPort          int           `yaml:"status_port"`
	StateFile           string        `yaml:"state_file"`
	ProbeURLs           []string      `yaml:"probe_urls"`
	ProbeTimeout        time.Duration `yaml:"probe_timeout"`
	HealthInterval      time.Duration `yaml:"health_interval"`
	RefreshInterval     time.Duration `yaml:"refresh_interval"`
	DialTimeout         time.Duration `yaml:"dial_timeout"`
	MaxConcurrentProbe  int           `yaml:"max_concurrent_probe"`
	TrafficProbeURLs    []string      `yaml:"traffic_probe_urls"`
	TrafficProbeURL     string        `yaml:"traffic_probe_url"`
	TrafficProbeTimeout time.Duration `yaml:"traffic_probe_timeout"`
	SlowLatency         time.Duration `yaml:"slow_latency"`
	LogRequests         bool          `yaml:"log_requests"`
	LogFormat           string        `yaml:"log_format"`

	logRequestsSet    bool `yaml:"-"`
	probeTimeoutSet   bool `yaml:"-"`
	trafficTimeoutSet bool `yaml:"-"`
	slowLatencySet    bool `yaml:"-"`
}

func defaults() Config {
	return Config{
		Bind:               "127.0.0.1",
		BasePort:           18081,
		StatusPort:         18080,
		StateFile:          "./state.json",
		ProbeURLs:          []string{"https://api.ipify.org", "https://ifconfig.me/ip"},
		ProbeTimeout:       6 * time.Second,
		HealthInterval:     60 * time.Second,
		RefreshInterval:    6 * time.Hour,
		DialTimeout:        10 * time.Second,
		MaxConcurrentProbe: 12,
		TrafficProbeURLs: []string{
			"https://www.cloudflare.com/cdn-cgi/trace",
			"https://api.ipify.org",
		},
		TrafficProbeURL:     "https://www.cloudflare.com/cdn-cgi/trace",
		TrafficProbeTimeout: 6 * time.Second,
		SlowLatency:         2 * time.Second,
		LogRequests:         true,
		LogFormat:           "text",
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
		if _, ok := probe["probe_timeout"]; ok {
			cfg.probeTimeoutSet = true
		}
		if _, ok := probe["traffic_probe_timeout"]; ok {
			cfg.trafficTimeoutSet = true
		}
		if _, ok := probe["traffic_probe_urls"]; !ok {
			if _, hasLegacy := probe["traffic_probe_url"]; hasLegacy {
				cfg.TrafficProbeURLs = nil
			}
		}
		if _, ok := probe["slow_latency"]; ok {
			cfg.slowLatencySet = true
		}
		for _, key := range []string{"probe_timeout", "traffic_probe_timeout", "slow_latency"} {
			if value, ok := probe[key]; ok {
				if duration, err := time.ParseDuration(strings.TrimSpace(fmt.Sprint(value))); err == nil && duration <= 0 {
					return nil, fmt.Errorf("%s must be greater than zero", key)
				}
			}
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
	if cfg.TrafficProbeURL == "" {
		cfg.TrafficProbeURL = d.TrafficProbeURL
	}
	if len(cfg.TrafficProbeURLs) == 0 && cfg.TrafficProbeURL != "" {
		cfg.TrafficProbeURLs = []string{cfg.TrafficProbeURL}
	}
	if cfg.TrafficProbeTimeout == 0 {
		cfg.TrafficProbeTimeout = d.TrafficProbeTimeout
	}
	if cfg.SlowLatency == 0 {
		cfg.SlowLatency = d.SlowLatency
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

// TrafficProbeTargets returns the ordered traffic probe URLs. The list field
// wins when configured; otherwise the legacy single URL is used.
func (c *Config) TrafficProbeTargets() []string {
	if len(c.TrafficProbeURLs) > 0 {
		return c.TrafficProbeURLs
	}
	if c.TrafficProbeURL != "" {
		return []string{c.TrafficProbeURL}
	}
	return nil
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
		if c.Sources[i].Type == SourceVPNCheap && goos == "darwin" && c.Sources[i].Path == "" {
			c.Sources[i].Path = vpncheapDarwinDefaultPath()
		}
	}
}

func vpncheapDarwinDefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	current := filepath.Join(home, "Library", "Containers", "com.vpncheap.macnative", "Data", "Library", "Caches", "com.vpncheap.macnative", "Cache.db")
	if _, err := os.Stat(current); err == nil {
		return current
	}
	legacy := filepath.Join(home, "Library", "Containers", "com.novamindllc.vpncheap", "Data", "Library", "Caches", "com.novamindllc.vpncheap", "Cache.db")
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return current
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
			if s.URL != "" {
				return fmt.Errorf("sources[%d] (tag %q): url must not be set for vpncheap on darwin", i, s.Tag)
			}
			if s.Path == "" {
				return fmt.Errorf("sources[%d] (tag %q): path must not be empty for vpncheap on darwin", i, s.Tag)
			}
			if hasScheme(s.Path, "http", "https") {
				return fmt.Errorf("sources[%d] (tag %q): path must be a filesystem path, not a URL", i, s.Tag)
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
	if !hasScheme(c.TrafficProbeURL, "http", "https") {
		return fmt.Errorf("traffic_probe_url must be http or https, got %q", c.TrafficProbeURL)
	}
	for i, u := range c.TrafficProbeTargets() {
		if u == "" {
			return fmt.Errorf("traffic_probe_urls[%d] must not be empty", i)
		}
		if !hasScheme(u, "http", "https") {
			return fmt.Errorf("traffic_probe_urls[%d] must be http or https, got %q", i, u)
		}
	}
	if (c.probeTimeoutSet && c.ProbeTimeout <= 0) ||
		(c.trafficTimeoutSet && c.TrafficProbeTimeout <= 0) ||
		(c.slowLatencySet && c.SlowLatency <= 0) {
		return fmt.Errorf("probe_timeout, traffic_probe_timeout, and slow_latency must be greater than zero")
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
