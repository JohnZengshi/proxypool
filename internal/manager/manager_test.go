package manager

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/john/proxypool/internal/alloc"
	"github.com/john/proxypool/internal/config"
	"github.com/john/proxypool/internal/listen"
	"github.com/john/proxypool/internal/sub"
)

type fakeDialer struct {
	id string
}

func (f *fakeDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

func (f *fakeDialer) Close() error { return nil }

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		SubscriptionURL:    "fake://test",
		Bind:               "127.0.0.1",
		BasePort:           28081,
		StatusPort:         28080,
		StateFile:          t.TempDir() + "/state.json",
		ProbeURLs:          []string{"http://127.0.0.1:9999"},
		ProbeTimeout:       2 * time.Second,
		HealthInterval:     1 * time.Hour,
		RefreshInterval:    1 * time.Hour,
		DialTimeout:        2 * time.Second,
		MaxConcurrentProbe: 4,
	}
}

func TestSnapshotSorted(t *testing.T) {
	m := &Manager{
		cfg:     testConfig(t),
		alloc:   allocNew(28081),
		entries: make(map[string]*entry),
		logger:  slog.Default(),
	}

	m.entries["a"] = &entry{port: 28083, exitIP: "3.3.3.3", node: subNode("a", "srv1", 443)}
	m.entries["b"] = &entry{port: 28081, exitIP: "1.1.1.1", node: subNode("b", "srv2", 443)}
	m.entries["c"] = &entry{port: 28082, exitIP: "2.2.2.2", node: subNode("c", "srv3", 443)}

	snap := m.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("expected 3, got %d", len(snap))
	}
	if snap[0].Port != 28081 || snap[1].Port != 28082 || snap[2].Port != 28083 {
		t.Fatalf("ports not sorted: %d %d %d", snap[0].Port, snap[1].Port, snap[2].Port)
	}
}

func TestDedupeByExitIP(t *testing.T) {
	n1 := subNode("fast", "srv1", 443)
	n2 := subNode("slow", "srv2", 443)
	n1.UUID = "test-uuid"
	n2.UUID = "test-uuid"

	if n1.Key() == n2.Key() {
		t.Fatal("different servers should have different keys")
	}
}

func TestCloseExitsCleanly(t *testing.T) {
	m := &Manager{
		cfg:     testConfig(t),
		alloc:   allocNew(28081),
		entries: make(map[string]*entry),
		logger:  slog.Default(),
	}

	n := subNode("test", "srv1", 443)
	srv := newListen("127.0.0.1:0")
	go srv.Serve()
	time.Sleep(50 * time.Millisecond)

	m.entries["test"] = &entry{
		node:   n,
		dialer: &fakeDialer{id: "test"},
		port:   28081,
		server: srv,
	}

	done := make(chan error, 1)
	go func() {
		done <- m.Close()
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close timed out")
	}
}

func allocNew(base int) *alloc.Allocator {
	return alloc.New(base)
}

func subNode(name, server string, port int) sub.Node {
	return sub.Node{Name: name, Server: server, Port: port, Type: "vmess", UUID: "test-uuid", AlterID: 1, Cipher: "auto"}
}

func newListen(addr string) *listen.Server {
	return listen.New(addr, 0, nil, false)
}

func TestRingBuffer(t *testing.T) {
	e := &entry{}
	for i := int64(1); i <= 200; i++ {
		e.addSample(Sample{TS: time.Unix(i, 0), LatencyMS: i, Healthy: true})
	}
	if e.histLen != 180 {
		t.Fatalf("expected 180, got %d", e.histLen)
	}
	hist := e.history()
	if len(hist) != 180 {
		t.Fatalf("expected 180, got %d", len(hist))
	}
	if hist[0].LatencyMS != 21 {
		t.Fatalf("expected first=21, got %d", hist[0].LatencyMS)
	}
	if hist[179].LatencyMS != 200 {
		t.Fatalf("expected last=200, got %d", hist[179].LatencyMS)
	}
}

func TestRingBufferWrap(t *testing.T) {
	e := &entry{}
	e.addSample(Sample{TS: time.Unix(1, 0), LatencyMS: 1, Healthy: true})
	e.addSample(Sample{TS: time.Unix(2, 0), LatencyMS: 2, Healthy: false})
	hist := e.history()
	if len(hist) != 2 {
		t.Fatalf("expected 2, got %d", len(hist))
	}
	if hist[0].LatencyMS != 1 || !hist[0].Healthy {
		t.Fatalf("expected first=1/healthy, got %v", hist[0])
	}
	if hist[1].LatencyMS != 2 || hist[1].Healthy {
		t.Fatalf("expected second=2/unhealthy, got %v", hist[1])
	}
}

func TestHistoryReturnsCopy(t *testing.T) {
	m := &Manager{
		cfg:     testConfig(t),
		alloc:   allocNew(28081),
		entries: make(map[string]*entry),
		logger:  slog.Default(),
	}
	e := &entry{port: 28081}
	e.addSample(Sample{TS: time.Now(), LatencyMS: 50, Healthy: true})
	m.entries["test"] = e

	h1 := m.History()
	h1[28081][0].LatencyMS = 999

	h2 := m.History()
	if h2[28081][0].LatencyMS != 50 {
		t.Fatalf("internal data was mutated: expected 50, got %d", h2[28081][0].LatencyMS)
	}
}

func TestProbeNowPortNotFound(t *testing.T) {
	m := &Manager{
		cfg:     testConfig(t),
		alloc:   allocNew(28081),
		entries: make(map[string]*entry),
		logger:  slog.Default(),
	}
	m.entries["test"] = &entry{port: 28081, dialer: &fakeDialer{id: "test"}}

	err := m.ProbeNow(context.Background(), 65535)
	if !errors.Is(err, ErrPortNotFound) {
		t.Fatalf("expected ErrPortNotFound, got %v", err)
	}
}

func TestProbeNowAll(t *testing.T) {
	m := &Manager{
		cfg:     testConfig(t),
		alloc:   allocNew(28081),
		entries: make(map[string]*entry),
		logger:  slog.Default(),
	}
	m.entries["a"] = &entry{port: 28081, dialer: &fakeDialer{id: "a"}}
	m.entries["b"] = &entry{port: 28082, dialer: &fakeDialer{id: "b"}}

	err := m.ProbeNow(context.Background(), 0)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}
