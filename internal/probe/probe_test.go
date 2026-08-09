package probe

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
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
