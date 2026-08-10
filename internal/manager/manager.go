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
	Port      int       `json:"port"`
	ExitIP    string    `json:"exit_ip"`
	Tag       string    `json:"tag"`
	Type      string    `json:"type"`
	NodeName  string    `json:"node_name"`
	LatencyMS int64     `json:"latency_ms"`
	Healthy   bool      `json:"healthy"`
	LastCheck time.Time `json:"last_check"`
}

type entry struct {
	node      sub.Node
	dialer    core.Dialer
	exitIP    string
	port      int
	tag       string
	ntype     string
	server    *listen.Server
	healthy   bool
	latency   time.Duration
	lastCheck time.Time
	hist      [180]Sample
	histLen   int
	histNext  int
	probing   atomic.Bool
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
}

func New(cfg *config.Config, logger *slog.Logger) *Manager {
	return &Manager{
		cfg:       cfg,
		alloc:     alloc.New(cfg.BasePort),
		entries:   make(map[string]*entry),
		logger:    logger,
		fetchFunc: sub.Fetch,
		loadFunc:  sub.Load,
		probeNode: func(ctx context.Context, node sub.Node) (probe.Result, error) {
			d, err := core.NewOutbound(node, cfg.DialTimeout)
			if err != nil {
				return probe.Result{}, err
			}
			defer d.Close()
			return probe.Exit(ctx, d, cfg.ProbeURLs, cfg.ProbeTimeout)
		},
	}
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

		port, err := m.alloc.Port(ip)
		if err != nil {
			m.logger.Error("port alloc failed", "ip", ip, "err", err)
			continue
		}

		if e, ok := m.entries[key]; ok {
			e.healthy = true
			e.latency = r.latency
			e.lastCheck = time.Now()
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

		srv := listen.New(net.JoinHostPort(m.cfg.Bind, itoa(port)), port, m.logger, m.cfg.LogRequests)
		srv.SetDialer(d)
		srv.SetExitIP(ip)
		srv.SetTag(r.node.Tag)
		go srv.Serve()

		m.entries[key] = &entry{
			node:      r.node,
			dialer:    d,
			exitIP:    ip,
			port:      port,
			tag:       r.node.Tag,
			ntype:     r.node.Type,
			server:    srv,
			healthy:   true,
			latency:   r.latency,
			lastCheck: time.Now(),
		}
		m.logger.Info("proxy started", "port", port, "exit_ip", ip, "tag", r.node.Tag, "node", maskUUID(r.node.Name), "latency", r.latency)
	}

	for key, e := range m.entries {
		if !successful[e.tag] {
			continue
		}
		if !activeKeys[key] {
			e.healthy = false
			if e.server != nil {
				e.server.SetDialer(nil)
			}
			m.logger.Info("node marked unhealthy", "port", e.port, "exit_ip", e.exitIP, "tag", e.tag, "node", maskUUID(e.node.Name))
		}
	}

	m.alloc.Save(m.cfg.StateFile)
	m.mu.Unlock()

	if m.OnRefresh != nil {
		m.OnRefresh()
	}
	return nil
}

func (m *Manager) healthLoop(ctx context.Context) {
	defer m.wg.Done()
	ticker := time.NewTicker(m.cfg.HealthInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkHealth(ctx)
		}
	}
}

func (m *Manager) checkHealth(ctx context.Context) {
	m.mu.RLock()
	entries := make([]*entry, 0, len(m.entries))
	for _, e := range m.entries {
		entries = append(entries, e)
	}
	m.mu.RUnlock()

	var wg sync.WaitGroup
	for _, e := range entries {
		wg.Add(1)
		go func(e *entry) {
			defer wg.Done()
			m.probeEntry(ctx, e)
		}(e)
	}
	wg.Wait()
	m.mu.Lock()
	m.alloc.Save(m.cfg.StateFile)
	m.mu.Unlock()
}

func (m *Manager) probeEntry(ctx context.Context, e *entry) {
	if !e.probing.CompareAndSwap(false, true) {
		return
	}
	defer e.probing.Store(false)

	if m.probeNode == nil || e.dialer == nil {
		return
	}
	res, err := probe.Exit(ctx, e.dialer, m.cfg.ProbeURLs, m.cfg.ProbeTimeout)
	m.mu.Lock()
	defer m.mu.Unlock()
	e.lastCheck = time.Now()
	if err != nil {
		e.healthy = false
		e.addSample(Sample{TS: time.Now(), LatencyMS: 0, Healthy: false})
		if e.server != nil {
			e.server.SetDialer(nil)
		}
		m.logger.Warn("probe failed", "port", e.port, "err", err)
		return
	}
	if res.IP != e.exitIP {
		newPort, err := m.alloc.Port(res.IP)
		if err != nil {
			m.logger.Error("port realloc failed", "ip", res.IP, "err", err)
			return
		}
		e.exitIP = res.IP
		e.port = newPort
		if e.server != nil {
			e.server.Close()
			e.server = listen.New(net.JoinHostPort(m.cfg.Bind, itoa(newPort)), newPort, m.logger, m.cfg.LogRequests)
			e.server.SetDialer(e.dialer)
			e.server.SetExitIP(res.IP)
			e.server.SetTag(e.tag)
			go e.server.Serve()
		}
	}
	e.healthy = true
	e.latency = res.Latency
	e.addSample(Sample{TS: time.Now(), LatencyMS: res.Latency.Milliseconds(), Healthy: true})
	if e.server != nil {
		e.server.SetDialer(e.dialer)
	}
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

	var wg sync.WaitGroup
	for _, e := range targets {
		wg.Add(1)
		go func(e *entry) {
			defer wg.Done()
			m.probeEntry(ctx, e)
		}(e)
	}
	wg.Wait()
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
			Port:      e.port,
			ExitIP:    e.exitIP,
			Tag:       e.tag,
			Type:      e.ntype,
			NodeName:  maskUUID(e.node.Name),
			LatencyMS: e.latency.Milliseconds(),
			Healthy:   e.healthy,
			LastCheck: e.lastCheck,
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
	return nil
}

func maskUUID(s string) string {
	return s
}

func itoa(i int) string {
	return strconv.Itoa(i)
}
