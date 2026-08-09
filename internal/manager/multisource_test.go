package manager

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/john/proxypool/internal/config"
	"github.com/john/proxypool/internal/probe"
	"github.com/john/proxypool/internal/sub"
	"github.com/sagernet/sing-box/option"
)

func vmessNode(tag, name, server string, port int) sub.Node {
	return sub.Node{
		Tag:    tag,
		Name:   name,
		Type:   "vmess",
		Server: server,
		Port:   port,
		Outbound: option.Outbound{
			Type: "vmess",
			Tag:  name,
			Options: &option.VMessOutboundOptions{
				ServerOptions: option.ServerOptions{Server: server, ServerPort: uint16(port)},
				UUID:          "test-uuid",
				Security:      "auto",
			},
		},
	}
}

func clashSub(proxies ...string) []byte {
	out := "proxies:\n"
	for _, p := range proxies {
		out += fmt.Sprintf("  - {name: %s, server: %s, port: 443, type: vmess, uuid: u, alterId: 1, cipher: auto}\n", p, p)
	}
	return []byte(out)
}

func singboxSub(tags ...string) []byte {
	out := `{"outbounds":[`
	for i, tg := range tags {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprintf(`{"type":"vmess","tag":"%s","server":"%s","server_port":443,"uuid":"u","security":"auto"}`, tg, tg)
	}
	out += `]}`
	return []byte(out)
}

func multiSourceConfig() *config.Config {
	return &config.Config{
		Bind:               "127.0.0.1",
		BasePort:           28081,
		StatusPort:         28080,
		ProbeURLs:          []string{"http://127.0.0.1:9999"},
		ProbeTimeout:       2 * time.Second,
		HealthInterval:     1 * time.Hour,
		RefreshInterval:    1 * time.Hour,
		DialTimeout:        2 * time.Second,
		MaxConcurrentProbe: 4,
		Sources: []config.Source{
			{Tag: "legacy", Type: config.SourceClash, URL: "http://legacy"},
			{Tag: "vpncheap", Type: config.SourceSingBox, URL: "http://vpncheap"},
		},
	}
}

func newTestManager(t *testing.T, cfg *config.Config) *Manager {
	t.Helper()
	cfg.StateFile = t.TempDir() + "/state.json"
	return &Manager{
		cfg:       cfg,
		alloc:     allocNew(cfg.BasePort),
		entries:   make(map[string]*entry),
		logger:    slog.Default(),
		fetchFunc: sub.Fetch,
	}
}

func TestRefreshMultiSource(t *testing.T) {
	m := newTestManager(t, multiSourceConfig())
	m.fetchFunc = func(ctx context.Context, url string) ([]byte, error) {
		switch url {
		case "http://legacy":
			return clashSub("srv1", "srv2"), nil
		case "http://vpncheap":
			return singboxSub("srv3", "srv4"), nil
		}
		return nil, fmt.Errorf("unknown url %s", url)
	}
	// srv1 and srv3 share exit IP 10.0.0.1 to prove global dedupe collapses them.
	ipByServer := map[string]string{
		"srv1": "10.0.0.1", "srv2": "10.0.0.2",
		"srv3": "10.0.0.1", "srv4": "10.0.0.3",
	}
	m.probeNode = func(ctx context.Context, node sub.Node) (probe.Result, error) {
		ip := ipByServer[node.Server]
		if ip == "" {
			return probe.Result{}, fmt.Errorf("no ip for %s", node.Server)
		}
		return probe.Result{IP: ip, Latency: 10 * time.Millisecond}, nil
	}

	if err := m.refreshOnce(context.Background()); err != nil {
		t.Fatalf("refreshOnce: %v", err)
	}
	snap := m.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("expected 3 ports (shared IP collapses), got %d: %+v", len(snap), snap)
	}
	tags := map[string]bool{}
	for _, s := range snap {
		tags[s.Tag] = true
	}
	if !tags["legacy"] || !tags["vpncheap"] {
		t.Fatalf("expected both source tags, got %v", tags)
	}
}

func TestRefreshOneSourceFails(t *testing.T) {
	m := newTestManager(t, multiSourceConfig())
	m.fetchFunc = func(ctx context.Context, url string) ([]byte, error) {
		if url == "http://legacy" {
			return nil, fmt.Errorf("legacy down")
		}
		return singboxSub("srv3"), nil
	}
	m.probeNode = func(ctx context.Context, node sub.Node) (probe.Result, error) {
		return probe.Result{IP: "10.0.0.3", Latency: 10 * time.Millisecond}, nil
	}

	if err := m.refreshOnce(context.Background()); err != nil {
		t.Fatalf("refreshOnce should succeed when one source fails: %v", err)
	}
	snap := m.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 port from surviving source, got %d", len(snap))
	}
	if snap[0].Tag != "vpncheap" {
		t.Fatalf("expected vpncheap tag, got %q", snap[0].Tag)
	}
}

func TestRefreshAllSourcesFail(t *testing.T) {
	m := newTestManager(t, multiSourceConfig())
	m.fetchFunc = func(ctx context.Context, url string) ([]byte, error) {
		return nil, fmt.Errorf("all down")
	}
	m.probeNode = func(ctx context.Context, node sub.Node) (probe.Result, error) {
		return probe.Result{}, nil
	}

	err := m.refreshOnce(context.Background())
	if err == nil {
		t.Fatal("expected error when all sources fail")
	}
	if len(m.Snapshot()) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(m.Snapshot()))
	}
}
