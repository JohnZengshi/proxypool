package sub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseFixture(t *testing.T) {
	data := mustReadFile(t, "testdata/sample.yaml")
	result, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 7 {
		t.Fatalf("expected 7 vmess nodes, got %d", len(result.Nodes))
	}
	tlsCount := 0
	nonTLSCount := 0
	for _, n := range result.Nodes {
		if n.TLS {
			tlsCount++
		} else {
			nonTLSCount++
		}
		if n.Type != "vmess" {
			t.Fatalf("expected vmess, got %s", n.Type)
		}
		if n.Cipher != "auto" {
			t.Fatalf("expected cipher=auto, got %s", n.Cipher)
		}
	}
	if tlsCount != 3 || nonTLSCount != 4 {
		t.Fatalf("expected 3 tls / 4 non-tls, got %d tls / %d non-tls", tlsCount, nonTLSCount)
	}
	if result.Skipped != 0 {
		t.Fatalf("expected 0 skipped, got %d", result.Skipped)
	}
}

func TestParseSkipsNonVmess(t *testing.T) {
	data := []byte(`
proxies:
  - {name: ss1, server: a.com, port: 443, type: ss, cipher: aes-256-gcm, password: test}
  - {name: vm1, server: b.com, port: 443, type: vmess, uuid: test-uuid, alterId: 1, cipher: auto}
  - {name: trojan1, server: c.com, port: 443, type: trojan, password: test}
`)
	result, err := Parse(data)
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

func TestParseMalformed(t *testing.T) {
	_, err := Parse([]byte("proxies: [unclosed\n"))
	if err == nil {
		t.Fatal("expected error for malformed yaml")
	}
}

func TestNodeKey(t *testing.T) {
	n := Node{Server: "example.com", Port: 443}
	if n.Key() != "example.com:443" {
		t.Fatalf("expected example.com:443, got %s", n.Key())
	}
}
