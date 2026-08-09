package config

import (
	"os"
	"path/filepath"
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
	if cfg.ProbeTimeout != 10*time.Second {
		t.Fatalf("expected ProbeTimeout=10s, got %v", cfg.ProbeTimeout)
	}
	if cfg.MaxConcurrentProbe != 8 {
		t.Fatalf("expected MaxConcurrentProbe=8, got %d", cfg.MaxConcurrentProbe)
	}
	if cfg.HealthInterval != 20*time.Second {
		t.Fatalf("expected HealthInterval=20s, got %v", cfg.HealthInterval)
	}
	if !cfg.LogRequests {
		t.Fatal("expected LogRequests=true by default")
	}
	if cfg.LogFormat != "text" {
		t.Fatalf("expected LogFormat=text, got %s", cfg.LogFormat)
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
