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
