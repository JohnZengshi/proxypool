package probe

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeDialer struct{}

func (f *fakeDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

func (f *fakeDialer) Close() error { return nil }

func TestExitSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "1.2.3.4")
	}))
	defer srv.Close()

	d := &fakeDialer{}
	res, err := Exit(context.Background(), d, []string{srv.URL}, 4*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.IP != "1.2.3.4" {
		t.Fatalf("expected 1.2.3.4, got %s", res.IP)
	}
	if res.Latency <= 0 {
		t.Fatal("expected positive latency")
	}
}

func TestExitSkipsNonIP(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "not-an-ip")
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "5.6.7.8")
	}))
	defer good.Close()

	d := &fakeDialer{}
	res, err := Exit(context.Background(), d, []string{bad.URL, good.URL}, 4*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.IP != "5.6.7.8" {
		t.Fatalf("expected 5.6.7.8, got %s", res.IP)
	}
}

func TestExitAllFail(t *testing.T) {
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv1.Close()
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv2.Close()

	d := &fakeDialer{}
	_, err := Exit(context.Background(), d, []string{srv1.URL, srv2.URL}, 4*time.Second)
	if err == nil {
		t.Fatal("expected error when all probes fail")
	}
}

func TestExitTimeout(t *testing.T) {
	start := time.Now()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
	}))
	defer srv.Close()

	d := &fakeDialer{}
	_, err := Exit(context.Background(), d, []string{srv.URL}, 1500*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed > 4*time.Second {
		t.Fatalf("took too long: %v", elapsed)
	}
}

func TestHTTPSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	res, err := HTTPSuccess(context.Background(), &fakeDialer{}, srv.URL, 4*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Latency <= 0 {
		t.Fatal("expected positive latency")
	}
}

func TestHTTPSuccessRejectsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad", http.StatusBadGateway)
	}))
	defer srv.Close()

	_, err := HTTPSuccess(context.Background(), &fakeDialer{}, srv.URL, 4*time.Second)
	if err == nil {
		t.Fatal("expected non-2xx error")
	}
}

func TestHTTPSuccessAnyFallsBack(t *testing.T) {
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

	d := &fakeDialer{}
	if _, err := HTTPSuccessAny(context.Background(), d, []string{bad.URL, good.URL}, 4*time.Second); err != nil {
		t.Fatal(err)
	}
	if badHits != 1 || goodHits != 1 {
		t.Fatalf("badHits=%d goodHits=%d, want 1 and 1", badHits, goodHits)
	}
}

func TestHTTPSuccessAnyAllFailIncludesTargets(t *testing.T) {
	bad1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad", http.StatusBadGateway)
	}))
	defer bad1.Close()
	bad2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad", http.StatusServiceUnavailable)
	}))
	defer bad2.Close()

	d := &fakeDialer{}
	_, err := HTTPSuccessAny(context.Background(), d, []string{bad1.URL, bad2.URL}, 4*time.Second)
	if err == nil {
		t.Fatal("expected error when all targets fail")
	}
	if !strings.Contains(err.Error(), bad1.URL) || !strings.Contains(err.Error(), bad2.URL) {
		t.Fatalf("error should include all targets, got %v", err)
	}
}

func TestDirectHTTPSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	res, err := DirectHTTPSuccess(context.Background(), srv.URL, 4*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Latency <= 0 {
		t.Fatal("expected positive latency")
	}
}

func TestDirectHTTPSuccessRejectsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad", http.StatusBadGateway)
	}))
	defer srv.Close()

	_, err := DirectHTTPSuccess(context.Background(), srv.URL, 4*time.Second)
	if err == nil {
		t.Fatal("expected non-2xx error")
	}
}

func TestDirectHTTPSuccessIgnoresEnvironmentProxy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")

	if _, err := DirectHTTPSuccess(context.Background(), srv.URL, 4*time.Second); err != nil {
		t.Fatalf("direct probe should bypass environment proxy: %v", err)
	}
}

func TestDirectHTTPSuccessAnyFallsBack(t *testing.T) {
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

	if _, err := DirectHTTPSuccessAny(context.Background(), []string{bad.URL, good.URL}, 4*time.Second); err != nil {
		t.Fatal(err)
	}
	if badHits != 1 || goodHits != 1 {
		t.Fatalf("badHits=%d goodHits=%d, want 1 and 1", badHits, goodHits)
	}
}
