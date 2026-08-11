package manager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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
		out += fmt.Sprintf("  - {name: %s, server: %s, port: 443, type: vmess, uuid: u, alterId: 1, cipher: auto}\n", p, testServer(p))
	}
	return []byte(out)
}

func singboxSub(tags ...string) []byte {
	out := `{"outbounds":[`
	for i, tg := range tags {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprintf(`{"type":"vmess","tag":"%s","server":"%s","server_port":443,"uuid":"u","security":"auto"}`, tg, testServer(tg))
	}
	out += `]}`
	return []byte(out)
}

func testServer(name string) string {
	return "203.0.113." + strings.TrimPrefix(name, "srv")
}

func multiSourceConfig() *config.Config {
	return &config.Config{
		Bind:                "127.0.0.1",
		BasePort:            28081,
		StatusPort:          28080,
		ProbeURLs:           []string{"http://127.0.0.1:9999"},
		ProbeTimeout:        2 * time.Second,
		HealthInterval:      1 * time.Hour,
		RefreshInterval:     1 * time.Hour,
		DialTimeout:         2 * time.Second,
		MaxConcurrentProbe:  4,
		TrafficProbeURL:     "http://127.0.0.1:9999",
		TrafficProbeTimeout: 2 * time.Second,
		SlowLatency:         2 * time.Second,
		Sources: []config.Source{
			{Tag: "legacy", Type: config.SourceClash, URL: "http://legacy"},
			{Tag: "vpncheap", Type: config.SourceSingBox, URL: "http://vpncheap"},
		},
	}
}

func multiSourceCacheConfig() *config.Config {
	cfg := multiSourceConfig()
	cfg.Sources = []config.Source{
		{Tag: "legacy", Type: config.SourceClash, URL: "http://legacy"},
		{Tag: "vpncheap", Type: config.SourceVPNCheap, Path: `C:\fake\app_state.json`},
	}
	return cfg
}

func newTestManager(t *testing.T, cfg *config.Config) *Manager {
	t.Helper()
	cfg.StateFile = t.TempDir() + "/state.json"
	m := &Manager{
		cfg:       cfg,
		alloc:     allocNew(cfg.BasePort),
		entries:   make(map[string]*entry),
		logger:    slog.Default(),
		fetchFunc: sub.Fetch,
	}
	m.directTraffic = func(ctx context.Context) (probe.Result, error) {
		return probe.Result{Latency: time.Millisecond}, nil
	}
	return m
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
	// srv1 and srv3 share an exit IP while remaining separate nodes.
	ipByServer := map[string]string{
		testServer("srv1"): "10.0.0.1", testServer("srv2"): "10.0.0.2",
		testServer("srv3"): "10.0.0.1", testServer("srv4"): "10.0.0.3",
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
	if len(snap) != 4 {
		t.Fatalf("expected all 4 parsed nodes, got %d: %+v", len(snap), snap)
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

func TestRefreshContinuesWhenDirectTrafficFails(t *testing.T) {
	m := newTestManager(t, multiSourceConfig())
	m.fetchFunc = func(ctx context.Context, url string) ([]byte, error) {
		return clashSub("srv1"), nil
	}
	m.probeNode = func(ctx context.Context, node sub.Node) (probe.Result, error) {
		return probe.Result{IP: "10.0.0.1", Latency: 10 * time.Millisecond}, nil
	}
	m.directTraffic = func(ctx context.Context) (probe.Result, error) {
		return probe.Result{}, errors.New("direct target unreachable")
	}

	if err := m.refreshOnce(context.Background()); err != nil {
		t.Fatalf("refreshOnce should continue when direct traffic probe fails: %v", err)
	}
	if len(m.Snapshot()) != 1 {
		t.Fatalf("expected 1 entry from node probe, got %d", len(m.Snapshot()))
	}
}

func TestRefreshFailedVPNCheapRetainsPriorEntries(t *testing.T) {
	m := newTestManager(t, multiSourceCacheConfig())
	failVPNCheap := false
	m.loadFunc = func(ctx context.Context, src config.Source) ([]byte, error) {
		switch src.Tag {
		case "legacy":
			if failVPNCheap {
				return clashSub("srv2"), nil
			}
			return clashSub("srv1"), nil
		case "vpncheap":
			if failVPNCheap {
				return nil, errors.New("cache down")
			}
			return singboxSub("srv3"), nil
		}
		return nil, fmt.Errorf("unknown source %s", src.Tag)
	}
	m.probeNode = func(ctx context.Context, node sub.Node) (probe.Result, error) {
		ip := map[string]string{
			testServer("srv1"): "10.0.0.1",
			testServer("srv2"): "10.0.0.2",
			testServer("srv3"): "10.0.0.3",
		}[node.Server]
		if ip == "" {
			return probe.Result{}, fmt.Errorf("no ip for %s", node.Server)
		}
		return probe.Result{IP: ip, Latency: 10 * time.Millisecond}, nil
	}

	if err := m.refreshOnce(context.Background()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	initial := m.Snapshot()
	var cheapPort int
	for _, status := range initial {
		if status.Tag == "vpncheap" {
			cheapPort = status.Port
		}
	}
	if cheapPort == 0 {
		t.Fatalf("expected vpncheap entry, got %+v", initial)
	}

	failVPNCheap = true
	if err := m.refreshOnce(context.Background()); err != nil {
		t.Fatalf("refresh with one failed source: %v", err)
	}
	snap := m.Snapshot()
	var cheap *NodeStatus
	for i := range snap {
		if snap[i].Tag == "vpncheap" {
			cheap = &snap[i]
		}
	}
	if cheap == nil {
		t.Fatalf("expected retained vpncheap entry, got %+v", snap)
	}
	if !cheap.Healthy {
		t.Fatalf("expected vpncheap entry to stay healthy after its source failed, got %+v", cheap)
	}
	if cheap.Port != cheapPort {
		t.Fatalf("expected vpncheap port %d to be retained, got %d", cheapPort, cheap.Port)
	}
}

func TestRefreshAllSourcesFailRetainsPriorEntries(t *testing.T) {
	m := newTestManager(t, multiSourceCacheConfig())
	m.loadFunc = func(ctx context.Context, src config.Source) ([]byte, error) {
		switch src.Tag {
		case "legacy":
			return clashSub("srv1"), nil
		case "vpncheap":
			return singboxSub("srv3"), nil
		}
		return nil, fmt.Errorf("unknown source %s", src.Tag)
	}
	m.probeNode = func(ctx context.Context, node sub.Node) (probe.Result, error) {
		ip := map[string]string{testServer("srv1"): "10.0.0.1", testServer("srv3"): "10.0.0.3"}[node.Server]
		return probe.Result{IP: ip, Latency: 10 * time.Millisecond}, nil
	}
	if err := m.refreshOnce(context.Background()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	if len(m.Snapshot()) != 2 {
		t.Fatalf("expected 2 initial entries, got %+v", m.Snapshot())
	}

	m.loadFunc = func(context.Context, config.Source) ([]byte, error) {
		return nil, errors.New("all down")
	}
	if err := m.refreshOnce(context.Background()); err == nil {
		t.Fatal("expected error when all sources fail")
	}
	snap := m.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected prior entries to remain after all-source refresh failure, got %+v", snap)
	}
	for _, status := range snap {
		if !status.Healthy {
			t.Fatalf("expected prior entry %+v to remain healthy after all-source refresh failure", status)
		}
	}
}
