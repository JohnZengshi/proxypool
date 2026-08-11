package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/john/proxypool/internal/config"
	"github.com/john/proxypool/internal/manager"
)

func TestHandlerStatusAndHealthWithoutNodes(t *testing.T) {
	// Given: a manager with no reachable nodes.
	cfg := &config.Config{BasePort: 18081}
	m := manager.New(cfg, nil)
	handler := New(m)

	// When: requesting status.
	status := httptest.NewRecorder()
	handler.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/status", nil))

	// Then: status returns an empty JSON list.
	if status.Code != http.StatusOK || status.Body.String() != "[]\n" {
		t.Fatalf("status = %d %q, want 200 \"[]\\n\"", status.Code, status.Body.String())
	}

	// When: requesting health.
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	// Then: health reports no healthy nodes.
	if health.Code != http.StatusServiceUnavailable {
		t.Fatalf("health = %d, want %d", health.Code, http.StatusServiceUnavailable)
	}
}

func TestHandlerProbeRejectsInvalidPort(t *testing.T) {
	// Given: a manager with no nodes.
	m := manager.New(&config.Config{BasePort: 18081}, nil)
	handler := New(m)

	// When: probing a non-numeric port.
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/probe?port=abc", nil))

	// Then: the API rejects invalid input.
	if response.Code != http.StatusBadRequest {
		t.Fatalf("probe = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestHandlerReconnectRejectsInvalidPort(t *testing.T) {
	// Given: a manager with no nodes.
	m := manager.New(&config.Config{BasePort: 18081}, nil)
	handler := New(m)

	// When: reconnecting a non-numeric port.
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/reconnect?port=abc", nil))

	// Then: the API rejects invalid input.
	if response.Code != http.StatusBadRequest {
		t.Fatalf("reconnect = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestHandlerReconnectPortNotFound(t *testing.T) {
	// Given: a manager with no nodes.
	m := manager.New(&config.Config{BasePort: 18081}, nil)
	handler := New(m)

	// When: reconnecting an unknown port.
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/reconnect?port=28099", nil))

	// Then: the API returns not found.
	if response.Code != http.StatusNotFound {
		t.Fatalf("reconnect = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestHandlerDashboard(t *testing.T) {
	m := manager.New(&config.Config{BasePort: 18081}, nil)
	handler := New(m)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("dashboard = %d, want 200", response.Code)
	}
	if ct := response.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content type = %q, want text/html", ct)
	}
	for _, want := range []string{`fetch("/status")`, `fetch("/history")`, `"/probe"`, `"/reconnect"`, "probe-all", "reconnect-all", "data-reconnect", "slow_latency_ms", "last_error"} {
		if !strings.Contains(response.Body.String(), want) {
			t.Fatalf("dashboard missing %q", want)
		}
	}
}
