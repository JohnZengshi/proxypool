package sub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/john/proxypool/internal/config"
	"github.com/sagernet/sing-box/option"
)

func TestParseClashFixture(t *testing.T) {
	data := mustReadFile(t, "testdata/sample.yaml")
	result, err := Parse(config.Source{Tag: "legacy", Type: config.SourceClash}, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 7 {
		t.Fatalf("expected 7 vmess nodes, got %d", len(result.Nodes))
	}
	tlsCount := 0
	for _, n := range result.Nodes {
		if n.Type != "vmess" {
			t.Fatalf("expected vmess, got %s", n.Type)
		}
		if n.Tag != "legacy" {
			t.Fatalf("expected tag legacy, got %s", n.Tag)
		}
		vo, ok := n.Outbound.Options.(*option.VMessOutboundOptions)
		if !ok {
			t.Fatalf("expected *VMessOutboundOptions, got %T", n.Outbound.Options)
		}
		if vo.UUID != "00000000-0000-0000-0000-000000000001" {
			t.Fatalf("unexpected uuid %s", vo.UUID)
		}
		if vo.Security != "auto" {
			t.Fatalf("expected security auto, got %s", vo.Security)
		}
		if vo.ServerOptions.ServerPort == 0 {
			t.Fatalf("expected non-zero port")
		}
		if vo.TLS != nil && vo.TLS.Enabled {
			tlsCount++
		}
	}
	if tlsCount != 3 {
		t.Fatalf("expected 3 tls nodes, got %d", tlsCount)
	}
	if result.Skipped != 0 {
		t.Fatalf("expected 0 skipped, got %d", result.Skipped)
	}
}

func TestParseClashSkipsNonVmess(t *testing.T) {
	data := []byte(`
proxies:
  - {name: ss1, server: a.com, port: 443, type: ss, cipher: aes-256-gcm, password: test}
  - {name: vm1, server: b.com, port: 443, type: vmess, uuid: test-uuid, alterId: 1, cipher: auto}
  - {name: trojan1, server: c.com, port: 443, type: trojan, password: test}
`)
	result, err := Parse(config.Source{Tag: "legacy", Type: config.SourceClash}, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 1 {
		t.Fatalf("expected 1 vmess node, got %d", len(result.Nodes))
	}
	if result.Skipped != 2 {
		t.Fatalf("expected 2 skipped, got %d", result.Skipped)
	}
	if result.Nodes[0].Name != "vm1" {
		t.Fatalf("expected vm1, got %s", result.Nodes[0].Name)
	}
}

func TestParseSingBox(t *testing.T) {
	data := mustReadFile(t, "testdata/vpncheap.json")
	result, err := Parse(config.Source{Tag: "vpncheap", Type: config.SourceSingBox}, data)
	if err != nil {
		t.Fatal(err)
	}
	// 5 dialable (2 hysteria2, 1 shadowsocks, 1 anytls, 1 vmess); selector/direct/block/dns skipped.
	if len(result.Nodes) != 5 {
		t.Fatalf("expected 5 dialable nodes, got %d (skipped=%d types=%v)",
			len(result.Nodes), result.Skipped, result.SkippedTypes)
	}
	if result.Skipped != 4 {
		t.Fatalf("expected 4 skipped group/utility, got %d", result.Skipped)
	}
	seen := map[string]int{}
	for _, n := range result.Nodes {
		if n.Tag != "vpncheap" {
			t.Fatalf("expected tag vpncheap, got %s", n.Tag)
		}
		seen[n.Type]++
		if n.Server == "" || n.Port == 0 {
			t.Fatalf("node %s missing server/port", n.Name)
		}
	}
	if seen["hysteria2"] != 2 || seen["shadowsocks"] != 1 || seen["anytls"] != 1 || seen["vmess"] != 1 {
		t.Fatalf("unexpected type distribution: %v", seen)
	}
}

func TestParseSingBoxMalformed(t *testing.T) {
	_, err := Parse(config.Source{Tag: "vpncheap", Type: config.SourceSingBox}, []byte("{not json"))
	if err == nil {
		t.Fatal("expected error for malformed json")
	}
}

func TestParseUnknownType(t *testing.T) {
	_, err := Parse(config.Source{Tag: "x", Type: "bogus"}, []byte("{}"))
	if err == nil {
		t.Fatal("expected error for unknown source type")
	}
}

func TestFetchSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "clash-verge/v1" {
			t.Errorf("expected UA clash-verge/v1, got %s", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte("proxies: []\n"))
	}))
	defer srv.Close()

	data, err := Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty data")
	}
}

func TestFetchServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := Fetch(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestParseClashMalformed(t *testing.T) {
	_, err := Parse(config.Source{Tag: "x", Type: config.SourceClash}, []byte("proxies: [unclosed\n"))
	if err == nil {
		t.Fatal("expected error for malformed yaml")
	}
}

func TestNodeKey(t *testing.T) {
	n := Node{Tag: "vpncheap", Server: "example.com", Port: 443}
	if n.Key() != "vpncheap|example.com:443" {
		t.Fatalf("expected vpncheap|example.com:443, got %s", n.Key())
	}
}
