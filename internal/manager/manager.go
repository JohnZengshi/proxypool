package manager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/john/proxypool/internal/alloc"
	"github.com/john/proxypool/internal/config"
	"github.com/john/proxypool/internal/core"
	"github.com/john/proxypool/internal/listen"
	"github.com/john/proxypool/internal/probe"
	"github.com/john/proxypool/internal/sub"
	"golang.org/x/sync/semaphore"
)

type NodeStatus struct {
	Port          int       `json:"port"`
	ExitIP        string    `json:"exit_ip"`
	Tag           string    `json:"tag"`
	Type          string    `json:"type"`
	NodeName      string    `json:"node_name"`
	LatencyMS     int64     `json:"latency_ms"`
	Healthy       bool      `json:"healthy"`
	LastCheck     time.Time `json:"last_check"`
	LastError     string    `json:"last_error"`
	FailCount     int       `json:"fail_count"`
	SlowLatencyMS int64     `json:"slow_latency_ms"`
}

type entry struct {
	node       sub.Node
	dialer     core.Dialer
	exitIP     string
	port       int
	tag        string
	ntype      string
	server     *listen.Server
	healthy    bool
	latency    time.Duration
	lastCheck  time.Time
	lastError  string
	hist       [180]Sample
	histLen    int
	histNext   int
	probing    atomic.Bool
	failCount  int
	generation int
}

type Sample struct {
	TS        time.Time `json:"ts"`
	LatencyMS int64     `json:"latency_ms"`
	Healthy   bool      `json:"healthy"`
}

func (e *entry) addSample(s Sample) {
	e.hist[e.histNext] = s
	e.histNext = (e.histNext + 1) % len(e.hist)
	if e.histLen < len(e.hist) {
		e.histLen++
	}
}

func (e *entry) history() []Sample {
	out := make([]Sample, 0, e.histLen)
	start := 0
	if e.histLen == len(e.hist) {
		start = e.histNext
	}
	for i := 0; i < e.histLen; i++ {
		out = append(out, e.hist[(start+i)%len(e.hist)])
	}
	return out
}

var ErrPortNotFound = errors.New("port not found")

const healthFailLimit = 3

type Manager struct {
	cfg       *config.Config
	alloc     *alloc.Allocator
	mu        sync.RWMutex
	entries   map[string]*entry
	logger    *slog.Logger
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	OnRefresh func()
	// fetchFunc fetches a source body; tests override it. Defaults to sub.Fetch.
	fetchFunc func(ctx context.Context, url string) ([]byte, error)
	// loadFunc loads a configured source body; tests override it. Defaults to sub.Load.
	loadFunc sub.SourceLoader
	// probeNode probes one node, returning exit IP/latency. Defaults to
	// building a sing-box dialer and probing; tests override it to skip network.
	probeNode func(ctx context.Context, node sub.Node) (probe.Result, error)
	// probeTraffic probes one existing node's dialer against the configured
	// traffic URL. probeExit refreshes only the node's exit IP metadata.
	probeTraffic func(ctx context.Context, node sub.Node, d core.Dialer) (probe.Result, error)
	probeExit    func(ctx context.Context, node sub.Node, d core.Dialer) (probe.Result, error)
	// directTraffic confirms the traffic probe target is reachable without a
	// proxy before a health round can mark nodes unhealthy.
	directTraffic func(ctx context.Context) (probe.Result, error)
}

func New(cfg *config.Config, logger *slog.Logger) *Manager {
	m := &Manager{
		cfg:       cfg,
		alloc:     alloc.New(cfg.BasePort),
		entries:   make(map[string]*entry),
		logger:    logger,
		fetchFunc: sub.Fetch,
		loadFunc:  sub.Load,
		probeTraffic: func(ctx context.Context, node sub.Node, d core.Dialer) (probe.Result, error) {
			return probe.HTTPSuccessAny(ctx, d, cfg.TrafficProbeTargets(), cfg.TrafficProbeTimeout)
		},
		probeExit: func(ctx context.Context, node sub.Node, d core.Dialer) (probe.Result, error) {
			return probe.Exit(ctx, d, cfg.ProbeURLs, cfg.ProbeTimeout)
		},
		directTraffic: func(ctx context.Context) (probe.Result, error) {
			return probe.DirectHTTPSuccessAny(ctx, cfg.TrafficProbeTargets(), cfg.TrafficProbeTimeout)
		},
	}
	m.probeNode = func(ctx context.Context, node sub.Node) (probe.Result, error) {
		return m.probeNodeDefault(ctx, node)
	}
	return m
}

func (m *Manager) probeNodeDefault(ctx context.Context, node sub.Node) (probe.Result, error) {
	d, err := core.NewOutbound(node, m.cfg.DialTimeout)
	if err != nil {
		return probe.Result{}, err
	}
	defer d.Close()

	traffic, err := m.probeTraffic(ctx, node, d)
	if err != nil {
		return traffic, err
	}
	exit, err := m.probeExit(ctx, node, d)
	if err != nil {
		return traffic, err
	}
	traffic.IP = exit.IP
	return traffic, nil
}

func (m *Manager) checkDirectTraffic(ctx context.Context) error {
	if m.directTraffic == nil || len(m.cfg.TrafficProbeTargets()) == 0 {
		return nil
	}
	if _, err := m.directTraffic(ctx); err != nil {
		return fmt.Errorf("direct traffic probe: %w", err)
	}
	return nil
}

func (m *Manager) Bootstrap(ctx context.Context) error {
	if err := m.alloc.Load(m.cfg.StateFile); err != nil {
		return err
	}
	if err := m.refreshOnce(ctx); err != nil {
		return err
	}

	subCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.wg.Add(2)
	go m.healthLoop(subCtx)
	go m.refreshLoop(subCtx)
	return nil
}

// ingest loads and parses every configured source, merging nodes into one
// slice. It also returns the set of source tags that refreshed successfully
// so refreshOnce can retain entries from failed sources. If a source fails it
// is logged and skipped; if all sources fail an error is returned.
func (m *Manager) ingest(ctx context.Context) ([]sub.Node, map[string]bool, error) {
	var all []sub.Node
	failed := 0
	successful := make(map[string]bool, len(m.cfg.Sources))
	for _, src := range m.cfg.Sources {
		data, err := m.loadSource(ctx, src)
		if err != nil {
			m.logger.Warn("source load failed", "tag", src.Tag, "err", err)
			failed++
			continue
		}
		result, err := sub.Parse(src, data)
		if err != nil {
			m.logger.Warn("source parse failed", "tag", src.Tag, "err", err)
			failed++
			continue
		}
		successful[src.Tag] = true
		if result.Skipped > 0 {
			m.logger.Info("skipped non-dialable nodes", "tag", src.Tag, "count", result.Skipped, "types", result.SkippedTypes)
		}
		all = append(all, result.Nodes...)
	}
	if len(m.cfg.Sources) > 0 && failed == len(m.cfg.Sources) {
		return nil, nil, fmt.Errorf("all %d sources failed", failed)
	}
	return all, successful, nil
}

func (m *Manager) loadSource(ctx context.Context, src config.Source) ([]byte, error) {
	if m.loadFunc != nil {
		return m.loadFunc(ctx, src)
	}
	if m.fetchFunc != nil && src.Type != config.SourceVPNCheap {
		return m.fetchFunc(ctx, src.URL)
	}
	return sub.Load(ctx, src)
}

func (m *Manager) refreshOnce(ctx context.Context) error {
	nodes, successful, err := m.ingest(ctx)
	if err != nil {
		return err
	}
	if err := m.checkDirectTraffic(ctx); err != nil {
		m.logger.Warn("direct traffic probe unavailable; continuing with node probes", "err", err)
	}
	result := &sub.FetchResult{Nodes: nodes}

	type probeResult struct {
		node    sub.Node
		exitIP  string
		latency time.Duration
		ok      bool
	}

	sem := semaphore.NewWeighted(int64(m.cfg.MaxConcurrentProbe))
	results := make([]probeResult, len(result.Nodes))
	var wg sync.WaitGroup

	for i, node := range result.Nodes {
		wg.Add(1)
		go func(idx int, n sub.Node) {
			defer wg.Done()
			sem.Acquire(ctx, 1)
			defer sem.Release(1)

			res, err := m.probeNode(ctx, n)
			if err != nil {
				m.logger.Warn("probe failed", "node", maskUUID(n.Name), "err", err)
				return
			}
			results[idx] = probeResult{node: n, exitIP: res.IP, latency: res.Latency, ok: true}
		}(i, node)
	}
	wg.Wait()

	bestByIP := make(map[string]probeResult)
	for _, r := range results {
		if !r.ok {
			continue
		}
		existing, found := bestByIP[r.exitIP]
		if !found || r.latency < existing.latency {
			bestByIP[r.exitIP] = r
		}
	}

	var ips []string
	for ip := range bestByIP {
		ips = append(ips, ip)
	}
	sort.Strings(ips)

	m.mu.Lock()

	activeKeys := make(map[string]bool)
	for _, ip := range ips {
		r := bestByIP[ip]
		key := r.node.Key()
		activeKeys[key] = true

		port, err := m.alloc.PortFor(key, ip)
		if err != nil {
			m.logger.Error("port alloc failed", "ip", ip, "err", err)
			continue
		}

		if e, ok := m.entries[key]; ok {
			e.generation++
			e.healthy = true
			e.latency = r.latency
			e.lastCheck = time.Now()
			e.failCount = 0
			e.lastError = ""
			if e.server != nil {
				e.server.SetDialer(e.dialer)
			}
			continue
		}

		d, err := core.NewOutbound(r.node, m.cfg.DialTimeout)
		if err != nil {
			m.logger.Error("recreate outbound failed", "node", maskUUID(r.node.Name), "err", err)
			continue
		}

		srv := listen.New(net.JoinHostPort(m.cfg.Bind, itoa(port)), port, m.logger, m.cfg.LogRequests, m.cfg.DialTimeout)
		srv.SetDialer(d)
		srv.SetExitIP(ip)
		srv.SetTag(r.node.Tag)
		go srv.Serve()

		m.entries[key] = &entry{
			node:       r.node,
			dialer:     d,
			exitIP:     ip,
			port:       port,
			tag:        r.node.Tag,
			ntype:      r.node.Type,
			server:     srv,
			healthy:    true,
			latency:    r.latency,
			lastCheck:  time.Now(),
			generation: 1,
		}
		m.logger.Info("proxy started", "port", port, "exit_ip", ip, "tag", r.node.Tag, "node", maskUUID(r.node.Name), "latency", r.latency)
	}

	for key, e := range m.entries {
		if !successful[e.tag] {
			continue
		}
		if !activeKeys[key] {
			e.generation++
			e.healthy = false
			e.lastError = "node removed from active subscription"
			m.logger.Info("node marked unhealthy", "port", e.port, "exit_ip", e.exitIP, "tag", e.tag, "node", maskUUID(e.node.Name))
		}
	}

	m.alloc.Save(m.cfg.StateFile)
	m.mu.Unlock()

	if m.OnRefresh != nil {
		m.OnRefresh()
	}
	m.printProxyAddresses()
	return nil
}

func (m *Manager) proxyAddresses() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	type addressEntry struct {
		addr    string
		latency time.Duration
		port    int
	}
	entries := make([]addressEntry, 0, len(m.entries))
	for _, e := range m.entries {
		if e.healthy && e.server != nil {
			entries = append(entries, addressEntry{
				addr:    "http://" + net.JoinHostPort(m.cfg.Bind, itoa(e.port)),
				latency: e.latency,
				port:    e.port,
			})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].latency != entries[j].latency {
			return entries[i].latency < entries[j].latency
		}
		return entries[i].port < entries[j].port
	})
	addrs := make([]string, len(entries))
	for i := range entries {
		addrs[i] = entries[i].addr
	}
	return addrs
}

func (m *Manager) printProxyAddresses() {
	for _, addr := range m.proxyAddresses() {
		fmt.Println(addr)
	}
}

func (m *Manager) healthLoop(ctx context.Context) {
	defer m.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(m.cfg.HealthInterval):
			m.checkHealth(ctx)
		}
	}
}

func (m *Manager) checkHealth(ctx context.Context) {
	if err := m.checkDirectTraffic(ctx); err != nil {
		m.logger.Warn("health round skipped", "err", err)
		return
	}
	m.mu.RLock()
	entries := make([]*entry, 0, len(m.entries))
	for _, e := range m.entries {
		entries = append(entries, e)
	}
	m.mu.RUnlock()
	m.probeEntries(ctx, entries)
	m.mu.Lock()
	m.alloc.Save(m.cfg.StateFile)
	m.mu.Unlock()
}

func (m *Manager) probeEntries(ctx context.Context, entries []*entry) {
	limit := m.cfg.MaxConcurrentProbe
	if limit < 1 {
		limit = 1
	}
	sem := semaphore.NewWeighted(int64(limit))
	var wg sync.WaitGroup
	for _, e := range entries {
		wg.Add(1)
		go func(e *entry) {
			defer wg.Done()
			if err := sem.Acquire(ctx, 1); err != nil {
				return
			}
			defer sem.Release(1)
			m.probeEntry(ctx, e)
		}(e)
	}
	wg.Wait()
}

func (m *Manager) probeEntry(ctx context.Context, e *entry) {
	if !e.probing.CompareAndSwap(false, true) {
		return
	}
	defer e.probing.Store(false)

	m.mu.RLock()
	d := e.dialer
	gen := e.generation
	m.mu.RUnlock()
	if d == nil {
		return
	}
	var (
		res probe.Result
		err error
	)
	if m.probeTraffic != nil {
		res, err = m.probeTraffic(ctx, e.node, d)
	} else if m.probeNode != nil {
		res, err = m.probeNode(ctx, e.node)
	} else {
		err = fmt.Errorf("no traffic probe configured")
	}
	m.mu.Lock()
	if gen != e.generation || d != e.dialer {
		m.mu.Unlock()
		return
	}
	e.lastCheck = time.Now()
	if err != nil {
		e.lastError = err.Error()
		e.failCount++
		if e.failCount >= healthFailLimit {
			e.healthy = false
		}
		e.addSample(Sample{TS: time.Now(), LatencyMS: 0, Healthy: false})
		m.logger.Warn("probe failed", "port", e.port, "err", err)
		m.mu.Unlock()
		return
	}
	e.failCount = 0
	e.healthy = true
	e.latency = res.Latency
	e.lastError = ""
	e.addSample(Sample{TS: time.Now(), LatencyMS: res.Latency.Milliseconds(), Healthy: true})
	if e.dialer != d || e.server == nil {
		m.mu.Unlock()
		return
	}
	if e.server != nil {
		e.server.SetDialer(d)
	}
	if m.probeExit == nil {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	exitRes, exitErr := m.probeExit(ctx, e.node, d)
	if exitErr != nil {
		m.logger.Warn("exit identify failed", "port", e.port, "err", exitErr)
		return
	}
	m.mu.Lock()
	if gen != e.generation || d != e.dialer {
		m.mu.Unlock()
		return
	}
	if exitRes.IP != "" && exitRes.IP != e.exitIP {
		m.alloc.RetainPort(e.node.Key(), e.port)
		e.exitIP = exitRes.IP
		if e.server != nil {
			e.server.SetExitIP(exitRes.IP)
		}
	}
	m.mu.Unlock()
}

func (m *Manager) ProbeNow(ctx context.Context, port int) error {
	m.mu.RLock()
	var targets []*entry
	if port == 0 {
		for _, e := range m.entries {
			targets = append(targets, e)
		}
	} else {
		for _, e := range m.entries {
			if e.port == port {
				targets = append(targets, e)
				break
			}
		}
	}
	m.mu.RUnlock()

	if len(targets) == 0 {
		return ErrPortNotFound
	}
	if err := m.checkDirectTraffic(ctx); err != nil {
		return err
	}

	m.probeEntries(ctx, targets)
	return nil
}

func (m *Manager) refreshLoop(ctx context.Context) {
	defer m.wg.Done()
	ticker := time.NewTicker(m.cfg.RefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.refreshOnce(ctx); err != nil {
				m.logger.Error("refresh failed", "err", err)
			}
		}
	}
}

func (m *Manager) Snapshot() []NodeStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	statuses := make([]NodeStatus, 0, len(m.entries))
	for _, e := range m.entries {
		statuses = append(statuses, NodeStatus{
			Port:          e.port,
			ExitIP:        e.exitIP,
			Tag:           e.tag,
			Type:          e.ntype,
			NodeName:      maskUUID(e.node.Name),
			LatencyMS:     e.latency.Milliseconds(),
			Healthy:       e.healthy,
			LastCheck:     e.lastCheck,
			LastError:     e.lastError,
			FailCount:     e.failCount,
			SlowLatencyMS: m.cfg.SlowLatency.Milliseconds(),
		})
	}
	sort.Slice(statuses, func(i, j int) bool {
		return statuses[i].Port < statuses[j].Port
	})
	return statuses
}

func (m *Manager) History() map[int][]Sample {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[int][]Sample, len(m.entries))
	for _, e := range m.entries {
		out[e.port] = e.history()
	}
	return out
}

func (m *Manager) Close() error {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.entries {
		if e.server != nil {
			e.server.Close()
		}
		if e.dialer != nil {
			e.dialer.Close()
		}
	}
	if m.cfg != nil && m.cfg.StateFile != "" && m.alloc != nil {
		if err := m.alloc.Save(m.cfg.StateFile); err != nil && m.logger != nil {
			m.logger.Warn("state save failed", "err", err)
		}
	}
	return nil
}

func maskUUID(s string) string {
	return s
}

func itoa(i int) string {
	return strconv.Itoa(i)
}
