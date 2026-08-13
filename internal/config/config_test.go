package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	os.WriteFile(p, []byte("subscription_url: https://example.com/sub.yaml\n"), 0644)

	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Bind != "127.0.0.1" {
		t.Fatalf("expected Bind=127.0.0.1, got %s", cfg.Bind)
	}
	if cfg.BasePort != 18081 {
		t.Fatalf("expected BasePort=18081, got %d", cfg.BasePort)
	}
	if cfg.ProbeTimeout != 6*time.Second {
		t.Fatalf("expected ProbeTimeout=6s, got %v", cfg.ProbeTimeout)
	}
	if cfg.MaxConcurrentProbe != 12 {
		t.Fatalf("expected MaxConcurrentProbe=12, got %d", cfg.MaxConcurrentProbe)
	}
	if cfg.HealthInterval != 60*time.Second {
		t.Fatalf("expected HealthInterval=60s, got %v", cfg.HealthInterval)
	}
	if cfg.TrafficProbeURL != "https://www.cloudflare.com/cdn-cgi/trace" {
		t.Fatalf("unexpected TrafficProbeURL %q", cfg.TrafficProbeURL)
	}
	targets := cfg.TrafficProbeTargets()
	if len(targets) != 2 || targets[0] != "https://www.cloudflare.com/cdn-cgi/trace" || targets[1] != "https://api.ipify.org" {
		t.Fatalf("unexpected default traffic probe targets %v", targets)
	}
	if cfg.TrafficProbeTimeout != 6*time.Second {
		t.Fatalf("expected TrafficProbeTimeout=6s, got %v", cfg.TrafficProbeTimeout)
	}
	if cfg.SlowLatency != 2*time.Second {
		t.Fatalf("expected SlowLatency=2s, got %v", cfg.SlowLatency)
	}
	if !cfg.LogRequests {
		t.Fatal("expected LogRequests=true by default")
	}
	if cfg.LogFormat != "text" {
		t.Fatalf("expected LogFormat=text, got %s", cfg.LogFormat)
	}
}

func TestLoadLegacyTrafficProbeOnly(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	os.WriteFile(p, []byte("subscription_url: https://example.com/sub.yaml\ntraffic_probe_url: https://legacy.example.com/check\n"), 0644)

	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	targets := cfg.TrafficProbeTargets()
	if len(targets) != 1 || targets[0] != "https://legacy.example.com/check" {
		t.Fatalf("expected legacy target to win, got %v", targets)
	}
}

func TestLoadTrafficProbeURLsWins(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	os.WriteFile(p, []byte("subscription_url: https://example.com/sub.yaml\ntraffic_probe_urls:\n  - https://first.example.com/check\n  - https://second.example.com/check\n"), 0644)

	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	targets := cfg.TrafficProbeTargets()
	if len(targets) != 2 || targets[0] != "https://first.example.com/check" || targets[1] != "https://second.example.com/check" {
		t.Fatalf("unexpected traffic probe targets %v", targets)
	}
}

func TestLoadLogRequestsFalse(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	os.WriteFile(p, []byte("subscription_url: https://example.com/sub.yaml\nlog_requests: false\n"), 0644)

	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogRequests {
		t.Fatal("expected LogRequests=false when explicitly set")
	}
}

func TestLoadLogFormatJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	os.WriteFile(p, []byte("subscription_url: https://example.com/sub.yaml\nlog_format: json\n"), 0644)

	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogFormat != "json" {
		t.Fatalf("expected LogFormat=json, got %s", cfg.LogFormat)
	}
}

func TestLoadLogFormatInvalid(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	os.WriteFile(p, []byte("subscription_url: https://example.com/sub.yaml\nlog_format: xml\n"), 0644)

	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for invalid log_format")
	}
	if !strings.Contains(err.Error(), "text") || !strings.Contains(err.Error(), "json") {
		t.Fatalf("error should mention text and json, got: %v", err)
	}
}

func TestLoadValidationErrors(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		yaml string
	}{
		{"empty_url", "bind: 127.0.0.1\nbase_port: 18081\n"},
		{"bad_port", "subscription_url: https://x.com\nbind: 127.0.0.1\nbase_port: 80\n"},
		{"bad_bind", "subscription_url: https://x.com\nbind: not-an-ip\nbase_port: 18081\n"},
		{"bad_scheme", "subscription_url: ftp://x.com\nbind: 127.0.0.1\nbase_port: 18081\n"},
		{"bad_traffic_probe", "subscription_url: https://x.com\nbind: 127.0.0.1\nbase_port: 18081\ntraffic_probe_url: ftp://x.com\n"},
		{"bad_traffic_probe_urls", "subscription_url: https://x.com\nbind: 127.0.0.1\nbase_port: 18081\ntraffic_probe_urls:\n  - ftp://x.com\n"},
		{"empty_traffic_probe_urls", "subscription_url: https://x.com\nbind: 127.0.0.1\nbase_port: 18081\ntraffic_probe_urls:\n  - \"\"\n"},
		{"bad_traffic_timeout", "subscription_url: https://x.com\nbind: 127.0.0.1\nbase_port: 18081\ntraffic_probe_timeout: 0s\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(dir, tt.name+".yaml")
			os.WriteFile(p, []byte(tt.yaml), 0644)
			_, err := Load(p)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadMalformedYAML(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.yaml")
	os.WriteFile(p, []byte("subscription_url: [unclosed\n"), 0644)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for malformed YAML")
	}
}

func TestSourcesLegacyPromotion(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	os.WriteFile(p, []byte("subscription_url: https://example.com/sub.yaml\n"), 0644)

	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sources) != 1 {
		t.Fatalf("expected 1 synthesized source, got %d", len(cfg.Sources))
	}
	if cfg.Sources[0].Tag != "default" {
		t.Fatalf("expected tag=default, got %q", cfg.Sources[0].Tag)
	}
	if cfg.Sources[0].Type != SourceClash {
		t.Fatalf("expected type=clash, got %q", cfg.Sources[0].Type)
	}
	if cfg.Sources[0].URL != "https://example.com/sub.yaml" {
		t.Fatalf("unexpected url %q", cfg.Sources[0].URL)
	}
}

func TestSourcesMulti(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	os.WriteFile(p, []byte(`sources:
  - tag: legacy
    type: clash
    url: https://example.com/sub.yaml
  - tag: vpncheap
    type: singbox
    url: https://example.com/cheap/token
`), 0644)

	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sources) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(cfg.Sources))
	}
	if cfg.Sources[0].Tag != "legacy" || cfg.Sources[0].Type != SourceClash {
		t.Fatalf("source 0 wrong: %+v", cfg.Sources[0])
	}
	if cfg.Sources[1].Tag != "vpncheap" || cfg.Sources[1].Type != SourceSingBox {
		t.Fatalf("source 1 wrong: %+v", cfg.Sources[1])
	}
}

func TestSourcesTypeDefaultsToClash(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	os.WriteFile(p, []byte("sources:\n  - tag: a\n    url: https://example.com/a\n"), 0644)

	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sources[0].Type != SourceClash {
		t.Fatalf("expected clash default, got %q", cfg.Sources[0].Type)
	}
}

func TestSourcesInvalid(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			"duplicate_tags",
			"sources:\n  - tag: a\n    type: clash\n    url: https://example.com/1\n  - tag: a\n    type: singbox\n    url: https://example.com/2\n",
			"duplicate tag",
		},
		{
			"bad_type",
			"sources:\n  - tag: a\n    type: bogus\n    url: https://example.com/1\n",
			"type must be",
		},
		{
			"empty_tag",
			"sources:\n  - tag: \"\"\n    type: clash\n    url: https://example.com/1\n",
			"tag must not be empty",
		},
		{
			"empty_url",
			"sources:\n  - tag: a\n    type: clash\n    url: \"\"\n",
			"url must not be empty",
		},
		{
			"bad_scheme",
			"sources:\n  - tag: a\n    type: clash\n    url: ftp://example.com/1\n",
			"must be http or https",
		},
		{
			"no_sources",
			"bind: 127.0.0.1\nbase_port: 18081\n",
			"at least one source",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(dir, tt.name+".yaml")
			os.WriteFile(p, []byte(tt.yaml), 0644)
			_, err := Load(p)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q should contain %q", err.Error(), tt.want)
			}
		})
	}
}

func TestLoadVPNCheapWindowsDefaultPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only config path behavior")
	}
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	p := filepath.Join(dir, "config.yaml")
	os.WriteFile(p, []byte(`sources:
  - tag: vpncheap
    type: vpncheap
`), 0644)

	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "vpncheap", "app_state.json")
	if cfg.Sources[0].Path != want {
		t.Fatalf("expected Windows default path %q, got %q", want, cfg.Sources[0].Path)
	}
	if cfg.Sources[0].URL != "" {
		t.Fatalf("expected optional URL to stay empty, got %q", cfg.Sources[0].URL)
	}
}

func TestLoadVPNCheapDarwinDefaultPath(t *testing.T) {
	tests := []struct {
		name   string
		create string
		want   string
	}{
		{
			name:   "current_cache_wins",
			create: "current",
			want:   "current",
		},
		{
			name:   "legacy_fallback",
			create: "legacy",
			want:   "legacy",
		},
		{
			name:   "missing_cache_defaults_to_current",
			create: "",
			want:   "current",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			if runtime.GOOS == "windows" {
				t.Setenv("USERPROFILE", home)
			} else {
				t.Setenv("HOME", home)
			}
			current := filepath.Join(home, "Library", "Containers", "com.vpncheap.macnative", "Data", "Library", "Caches", "com.vpncheap.macnative", "Cache.db")
			legacy := filepath.Join(home, "Library", "Containers", "com.novamindllc.vpncheap", "Data", "Library", "Caches", "com.novamindllc.vpncheap", "Cache.db")
			var create string
			switch tt.create {
			case "current":
				create = current
			case "legacy":
				create = legacy
			}
			if tt.create != "" {
				if err := os.MkdirAll(filepath.Dir(create), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(create, []byte("cache"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			want := current
			if tt.create == "legacy" {
				want = legacy
			}

			cfg := defaults()
			cfg.Sources = []Source{{Tag: "vpncheap", Type: SourceVPNCheap}}
			cfg.normalizeOn("darwin")
			if cfg.Sources[0].Path != want {
				t.Fatalf("expected Darwin default path %q, got %q", want, cfg.Sources[0].Path)
			}
			if cfg.Sources[0].URL != "" {
				t.Fatalf("expected optional URL to stay empty, got %q", cfg.Sources[0].URL)
			}
		})
	}
}

func TestSourcesVPNCheapPlatformValidation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", dir)
	} else {
		t.Setenv("HOME", dir)
	}
	tests := []struct {
		name   string
		goos   string
		source Source
		want   string
	}{
		{
			name: "windows_default_path_optional_url",
			goos: "windows",
			source: Source{
				Tag:  "vpncheap",
				Type: SourceVPNCheap,
			},
		},
		{
			name: "macos_default_path",
			goos: "darwin",
			source: Source{
				Tag:  "vpncheap",
				Type: SourceVPNCheap,
			},
		},
		{
			name: "macos_explicit_path",
			goos: "darwin",
			source: Source{
				Tag:  "vpncheap",
				Type: SourceVPNCheap,
				Path: filepath.Join(dir, "Cache.db"),
			},
		},
		{
			name: "macos_url_set",
			goos: "darwin",
			source: Source{
				Tag:  "vpncheap",
				Type: SourceVPNCheap,
				URL:  "https://example.com/cheap/secret-token",
			},
			want: "url must not be set for vpncheap on darwin",
		},
		{
			name: "macos_url_with_path",
			goos: "darwin",
			source: Source{
				Tag:  "vpncheap",
				Type: SourceVPNCheap,
				URL:  "https://example.com/cheap/secret-token",
				Path: filepath.Join(dir, "Cache.db"),
			},
			want: "url must not be set for vpncheap on darwin",
		},
		{
			name: "macos_path_url",
			goos: "darwin",
			source: Source{
				Tag:  "vpncheap",
				Type: SourceVPNCheap,
				Path: "https://example.com/Cache.db",
			},
			want: "path must be a filesystem path",
		},
		{
			name: "windows_path_url",
			goos: "windows",
			source: Source{
				Tag:  "vpncheap",
				Type: SourceVPNCheap,
				Path: "https://example.com/app_state.json",
			},
			want: "path must be a filesystem path",
		},
		{
			name: "linux_accepts_config",
			goos: "linux",
			source: Source{
				Tag:  "vpncheap",
				Type: SourceVPNCheap,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaults()
			cfg.Sources = []Source{tt.source}
			cfg.normalizeOn(tt.goos)
			err := cfg.validateOn(tt.goos)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("expected valid source, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q should contain %q", err.Error(), tt.want)
			}
			if tt.source.URL != "" && strings.Contains(err.Error(), tt.source.URL) {
				t.Fatalf("error leaked URL %q: %v", tt.source.URL, err)
			}
		})
	}
}

func TestLoadConfigExamplePortable(t *testing.T) {
	home := t.TempDir()
	appData := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	} else {
		t.Setenv("HOME", home)
	}
	t.Setenv("APPDATA", appData)
	cfg, err := Load(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, goos := range []string{"windows", "darwin", "linux"} {
		portable := *cfg
		portable.Sources = append([]Source(nil), cfg.Sources...)
		for i := range portable.Sources {
			if portable.Sources[i].Type == SourceVPNCheap {
				portable.Sources[i].URL = ""
				portable.Sources[i].Path = ""
			}
		}
		portable.normalizeOn(goos)
		if err := portable.validateOn(goos); err != nil {
			t.Fatalf("config example failed validation on %s: %v", goos, err)
		}
	}
}
