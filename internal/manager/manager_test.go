package manager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/john/proxypool/internal/alloc"
	"github.com/john/proxypool/internal/config"
	"github.com/john/proxypool/internal/core"
	"github.com/john/proxypool/internal/listen"
	"github.com/john/proxypool/internal/probe"
	"github.com/john/proxypool/internal/sub"
	"github.com/sagernet/sing-box/option"
)

type fakeDialer struct {
	id string
}

type trackingDialer struct {
	active atomic.Int64
	max    atomic.Int64
	delay  time.Duration
}

func (d *trackingDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	active := d.active.Add(1)
	for {
		cur := d.max.Load()
		if active <= cur || d.max.CompareAndSwap(cur, active) {
			break
		}
	}
	defer d.active.Add(-1)
	if d.delay > 0 {
		time.Sleep(d.delay)
	}
	var raw net.Dialer
	return raw.DialContext(ctx, network, addr)
}

func (d *trackingDialer) Close() error { return nil }

func (f *fakeDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

func (f *fakeDialer) Close() error { return nil }

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		SubscriptionURL:     "fake://test",
		Bind:                "127.0.0.1",
		BasePort:            28081,
		StatusPort:          28080,
		StateFile:           t.TempDir() + "/state.json",
		ProbeURLs:           []string{"http://127.0.0.1:9999"},
		ProbeTimeout:        2 * time.Second,
		HealthInterval:      1 * time.Hour,
		RefreshInterval:     1 * time.Hour,
		DialTimeout:         2 * time.Second,
		MaxConcurrentProbe:  4,
		TrafficProbeURL:     "http://127.0.0.1:9999",
		TrafficProbeTimeout: 2 * time.Second,
		SlowLatency:         2 * time.Second,
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
	ln, err := net.Listen("tcp", srv.Addr())
	if err != nil {
		t.Fatalf("proxy port still bound after Close: %v", err)
	}
	ln.Close()
}

func allocNew(base int) *alloc.Allocator {
	return alloc.New(base)
}

func subNode(name, server string, port int) sub.Node {
	return sub.Node{
		Tag:    "default",
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

func newListen(addr string) *listen.Server {
	return listen.New(addr, 0, nil, false, 2*time.Second)
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

func TestSnapshotIncludesProbeDiagnostics(t *testing.T) {
	cfg := testConfig(t)
	cfg.SlowLatency = 3 * time.Second
	m := &Manager{
		cfg:     cfg,
		entries: make(map[string]*entry),
	}
	m.entries["test"] = &entry{
		port:      28081,
		exitIP:    "1.2.3.4",
		healthy:   false,
		failCount: 2,
		lastError: "traffic probe failed",
	}

	snap := m.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 status, got %d", len(snap))
	}
	if snap[0].LastError != "traffic probe failed" || snap[0].FailCount != 2 || snap[0].SlowLatencyMS != 3000 {
		t.Fatalf("unexpected snapshot: %+v", snap[0])
	}
}

func TestCheckHealthSkipsWhenDirectTrafficFails(t *testing.T) {
	cfg := testConfig(t)
	m := &Manager{
		cfg:     cfg,
		alloc:   allocNew(28081),
		entries: make(map[string]*entry),
		logger:  slog.Default(),
	}
	m.directTraffic = func(ctx context.Context) (probe.Result, error) {
		return probe.Result{}, errors.New("direct down")
	}
	e := &entry{port: 28081, dialer: &fakeDialer{id: "test"}, healthy: true}
	m.entries["test"] = e

	m.checkHealth(context.Background())
	if !e.healthy || e.failCount != 0 {
		t.Fatalf("health round changed node after direct failure: healthy=%v failCount=%d", e.healthy, e.failCount)
	}
}

func TestProxyAddressesHealthySorted(t *testing.T) {
	cfg := testConfig(t)
	m := &Manager{
		cfg:     cfg,
		entries: make(map[string]*entry),
	}
	m.entries["unhealthy"] = &entry{port: 28082, healthy: false, server: newListen("127.0.0.1:0")}
	m.entries["slow"] = &entry{port: 28083, healthy: true, latency: 200 * time.Millisecond, server: newListen("127.0.0.1:0")}
	m.entries["fast"] = &entry{port: 28081, healthy: true, latency: 50 * time.Millisecond, server: newListen("127.0.0.1:0")}
	m.entries["fastest"] = &entry{port: 28084, healthy: true, latency: 10 * time.Millisecond, server: newListen("127.0.0.1:0")}
	m.entries["no-server"] = &entry{port: 28084, healthy: true}

	got := m.proxyAddresses()
	want := []string{"http://127.0.0.1:28084", "http://127.0.0.1:28081", "http://127.0.0.1:28083"}
	if len(got) != len(want) {
		t.Fatalf("addresses = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("addresses = %v, want %v", got, want)
		}
	}
}

func TestPrintProxyAddressesNoExtraOutput(t *testing.T) {
	cfg := testConfig(t)
	m := &Manager{
		cfg:     cfg,
		entries: make(map[string]*entry),
	}
	m.entries["slow"] = &entry{port: 28083, healthy: true, latency: 200 * time.Millisecond, server: newListen("127.0.0.1:0")}
	m.entries["fast"] = &entry{port: 28081, healthy: true, latency: 50 * time.Millisecond, server: newListen("127.0.0.1:0")}
	m.entries["down"] = &entry{port: 28082, healthy: false, server: newListen("127.0.0.1:0")}

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() {
		w.Close()
		os.Stdout = old
	}()

	m.printProxyAddresses()
	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "http://127.0.0.1:28081\nhttp://127.0.0.1:28083\n" {
		t.Fatalf("stdout = %q", string(out))
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

func TestProbeFailureThreshold(t *testing.T) {
	cfg := testConfig(t)
	m := &Manager{
		cfg:     cfg,
		alloc:   allocNew(28081),
		entries: make(map[string]*entry),
		logger:  slog.Default(),
	}
	m.probeTraffic = func(ctx context.Context, node sub.Node, d core.Dialer) (probe.Result, error) {
		return probe.Result{}, errors.New("probe down")
	}
	e := &entry{port: 28081, dialer: &fakeDialer{id: "test"}, server: newListen("127.0.0.1:0"), healthy: true}
	m.entries["test"] = e

	for i := 1; i <= 2; i++ {
		m.probeEntry(context.Background(), e)
		if !e.healthy || e.failCount != i {
			t.Fatalf("after %d failures: healthy=%v failCount=%d", i, e.healthy, e.failCount)
		}
	}

	m.probeEntry(context.Background(), e)
	if e.healthy {
		t.Fatal("expected unhealthy after third failure")
	}
	if e.failCount != 3 {
		t.Fatalf("expected failCount=3, got %d", e.failCount)
	}
}

func TestProbeFailureRecoversOnSuccess(t *testing.T) {
	cfg := testConfig(t)
	m := &Manager{
		cfg:     cfg,
		alloc:   allocNew(28081),
		entries: make(map[string]*entry),
		logger:  slog.Default(),
	}
	m.probeTraffic = func(ctx context.Context, node sub.Node, d core.Dialer) (probe.Result, error) {
		return probe.Result{Latency: 50 * time.Millisecond}, nil
	}
	e := &entry{
		port:      28081,
		exitIP:    "1.2.3.4",
		dialer:    &fakeDialer{id: "test"},
		server:    newListen("127.0.0.1:0"),
		healthy:   false,
		failCount: 3,
	}
	m.entries["test"] = e

	m.probeEntry(context.Background(), e)
	if !e.healthy {
		t.Fatal("expected healthy after successful probe")
	}
	if e.failCount != 0 {
		t.Fatalf("expected failCount=0, got %d", e.failCount)
	}
	if e.lastError != "" {
		t.Fatalf("expected lastError cleared, got %q", e.lastError)
	}
}

func TestProbeOfflineRetainsServerDialer(t *testing.T) {
	cfg := testConfig(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "still-served")
	}))
	defer upstream.Close()

	srv := newListen("127.0.0.1:0")
	srv.SetDialer(&fakeDialer{id: "test"})
	go srv.Serve()
	time.Sleep(50 * time.Millisecond)
	defer srv.Close()

	m := &Manager{
		cfg:     cfg,
		alloc:   allocNew(28081),
		entries: make(map[string]*entry),
		logger:  slog.Default(),
	}
	m.probeTraffic = func(ctx context.Context, node sub.Node, d core.Dialer) (probe.Result, error) {
		return probe.Result{}, errors.New("probe down")
	}
	e := &entry{
		port:    28081,
		dialer:  &fakeDialer{id: "test"},
		server:  srv,
		healthy: true,
	}
	m.entries["test"] = e
	for i := 0; i < 3; i++ {
		m.probeEntry(context.Background(), e)
	}
	if e.healthy {
		t.Fatal("expected unhealthy after three failures")
	}

	proxyURL, err := url.Parse("http://" + srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	resp, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatalf("proxy stopped serving after offline: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "still-served") {
		t.Fatalf("proxy returned %s, want still-served", body)
	}
}

func TestProbeEntryKeepsPortOnExitIPChange(t *testing.T) {
	cfg := testConfig(t)
	m := &Manager{
		cfg:     cfg,
		alloc:   allocNew(28081),
		entries: make(map[string]*entry),
		logger:  slog.Default(),
	}
	m.probeTraffic = func(ctx context.Context, node sub.Node, d core.Dialer) (probe.Result, error) {
		return probe.Result{Latency: 30 * time.Millisecond}, nil
	}
	m.probeExit = func(ctx context.Context, node sub.Node, d core.Dialer) (probe.Result, error) {
		return probe.Result{IP: "5.6.7.8"}, nil
	}
	e := &entry{
		port:    28081,
		exitIP:  "1.2.3.4",
		node:    subNode("test", "srv1", 443),
		dialer:  &fakeDialer{id: "test"},
		server:  newListen("127.0.0.1:0"),
		healthy: true,
	}
	m.entries["test"] = e

	m.probeEntry(context.Background(), e)
	if e.port != 28081 {
		t.Fatalf("port changed to %d, want 28081", e.port)
	}
	if e.exitIP != "5.6.7.8" {
		t.Fatalf("exit IP = %s, want 5.6.7.8", e.exitIP)
	}
	if got, _ := m.alloc.Port(e.node.Key()); got != 28081 {
		t.Fatalf("port for node key = %d, want 28081", got)
	}
}

func TestProbeEntriesRespectsConcurrencyLimit(t *testing.T) {
	cfg := testConfig(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "1.2.3.4")
	}))
	defer srv.Close()
	cfg.ProbeURLs = []string{srv.URL}
	cfg.TrafficProbeURL = srv.URL
	cfg.MaxConcurrentProbe = 1

	d := &trackingDialer{delay: 50 * time.Millisecond}
	m := &Manager{
		cfg:     cfg,
		alloc:   allocNew(28081),
		entries: make(map[string]*entry),
		logger:  slog.Default(),
	}
	m.probeTraffic = func(ctx context.Context, node sub.Node, d core.Dialer) (probe.Result, error) {
		return probe.HTTPSuccess(ctx, d, srv.URL, cfg.TrafficProbeTimeout)
	}
	for i := 0; i < 3; i++ {
		m.entries[fmt.Sprintf("n%d", i)] = &entry{
			port:    28081 + i,
			exitIP:  "1.2.3.4",
			dialer:  d,
			server:  newListen("127.0.0.1:0"),
			healthy: true,
		}
	}

	m.probeEntries(context.Background(), []*entry{
		m.entries["n0"], m.entries["n1"], m.entries["n2"],
	})
	if got := d.max.Load(); got != 1 {
		t.Fatalf("max concurrent dials = %d, want 1", got)
	}
}

func TestDefaultTrafficProbeFallsBack(t *testing.T) {
	badHits := 0
	goodHits := 0
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badHits++
		http.Error(w, "bad", http.StatusBadGateway)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodHits++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer good.Close()

	cfg := testConfig(t)
	cfg.TrafficProbeURL = bad.URL
	cfg.TrafficProbeURLs = []string{bad.URL, good.URL}
	m := New(cfg, slog.Default())

	if _, err := m.probeTraffic(context.Background(), subNode("test", "srv1", 443), &fakeDialer{}); err != nil {
		t.Fatal(err)
	}
	if badHits != 1 || goodHits != 1 {
		t.Fatalf("badHits=%d goodHits=%d, want 1 and 1", badHits, goodHits)
	}
}

func TestDefaultDirectTrafficFallsBack(t *testing.T) {
	badHits := 0
	goodHits := 0
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badHits++
		http.Error(w, "bad", http.StatusBadGateway)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodHits++
		fmt.Fprint(w, "ok")
	}))
	defer good.Close()

	cfg := testConfig(t)
	cfg.TrafficProbeURL = bad.URL
	cfg.TrafficProbeURLs = []string{bad.URL, good.URL}
	m := New(cfg, slog.Default())

	if _, err := m.directTraffic(context.Background()); err != nil {
		t.Fatal(err)
	}
	if badHits != 1 || goodHits != 1 {
		t.Fatalf("badHits=%d goodHits=%d, want 1 and 1", badHits, goodHits)
	}
}

func TestDefaultTrafficProbeAllFailIncludesTargets(t *testing.T) {
	bad1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad", http.StatusBadGateway)
	}))
	defer bad1.Close()
	bad2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad", http.StatusServiceUnavailable)
	}))
	defer bad2.Close()

	cfg := testConfig(t)
	cfg.TrafficProbeURL = bad1.URL
	cfg.TrafficProbeURLs = []string{bad1.URL, bad2.URL}
	m := New(cfg, slog.Default())

	_, err := m.probeTraffic(context.Background(), subNode("test", "srv1", 443), &fakeDialer{})
	if err == nil {
		t.Fatal("expected error when all traffic targets fail")
	}
	if !strings.Contains(err.Error(), bad1.URL) || !strings.Contains(err.Error(), bad2.URL) {
		t.Fatalf("error should include all targets, got %v", err)
	}
}
