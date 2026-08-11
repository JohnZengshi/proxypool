package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/john/proxypool/internal/config"
	"github.com/john/proxypool/internal/manager"
)

type closeTrackingListener struct {
	net.Listener
	once   sync.Once
	closed chan struct{}
}

type acceptErrorListener struct {
	once   sync.Once
	closed chan struct{}
}

func (l *acceptErrorListener) Accept() (net.Conn, error) {
	return nil, errors.New("accept failed")
}

func (l *acceptErrorListener) Close() error {
	l.once.Do(func() {
		close(l.closed)
	})
	return nil
}

func (l *acceptErrorListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
}

func (l *closeTrackingListener) Close() error {
	l.once.Do(func() {
		close(l.closed)
	})
	return l.Listener.Close()
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestServeStatusPortConflictDoesNotBootstrap(t *testing.T) {
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()

	port := blocker.Addr().(*net.TCPAddr).Port
	bootstrapCalls := 0
	newPoolCalls := 0
	err = serve(context.Background(), serveDeps{
		cfg:     &config.Config{StatusPort: port},
		logger:  discardLogger(),
		listen:  net.Listen,
		newPool: func(*config.Config, *slog.Logger) *manager.Manager { newPoolCalls++; return nil },
		bootstrap: func(context.Context, *manager.Manager) error {
			bootstrapCalls++
			return nil
		},
		closePool: func(*manager.Manager) error { return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "status server listen") {
		t.Fatalf("expected status server listen error, got %v", err)
	}
	if newPoolCalls != 0 {
		t.Fatalf("newPool called %d times on port conflict, want 0", newPoolCalls)
	}
	if bootstrapCalls != 0 {
		t.Fatalf("bootstrap called %d times on port conflict, want 0", bootstrapCalls)
	}
}

func TestServeListensBeforeBootstrapAndServesStatus(t *testing.T) {
	events := make(chan string, 8)
	listeners := make(chan *closeTrackingListener, 1)
	bootstrapStarted := make(chan struct{})
	releaseBootstrap := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)

	go func() {
		errCh <- serve(ctx, serveDeps{
			cfg: &config.Config{
				BasePort:    18081,
				StatusPort:  0,
				SlowLatency: 2 * time.Second,
			},
			logger: discardLogger(),
			listen: func(network, address string) (net.Listener, error) {
				events <- "listen"
				raw, err := net.Listen(network, address)
				if err != nil {
					return nil, err
				}
				ln := &closeTrackingListener{
					Listener: raw,
					closed:   make(chan struct{}),
				}
				listeners <- ln
				return ln, nil
			},
			newPool: func(cfg *config.Config, logger *slog.Logger) *manager.Manager {
				events <- "pool"
				return manager.New(cfg, logger)
			},
			bootstrap: func(context.Context, *manager.Manager) error {
				events <- "bootstrap"
				close(bootstrapStarted)
				<-releaseBootstrap
				return nil
			},
			closePool: func(*manager.Manager) error {
				events <- "close"
				return nil
			},
		})
	}()

	var listener *closeTrackingListener
	select {
	case listener = <-listeners:
	case <-time.After(2 * time.Second):
		t.Fatal("listener was not created")
	}

	select {
	case <-bootstrapStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("bootstrap did not start")
	}

	url := "http://" + listener.Addr().String() + "/status"
	waitForStatus(t, url)
	close(releaseBootstrap)
	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("serve returned error after cancel: %v", err)
	}

	var order []string
	for len(order) < 4 {
		select {
		case event := <-events:
			order = append(order, event)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for lifecycle events, got %v", order)
		}
	}
	if got := strings.Join(order, ","); got != "listen,pool,bootstrap,close" {
		t.Fatalf("lifecycle order = %q, want listen,pool,bootstrap,close", got)
	}
	select {
	case <-listener.closed:
	default:
		t.Fatal("listener was not closed")
	}
}

func TestServeBootstrapErrorClosesListenerAndPool(t *testing.T) {
	listeners := make(chan *closeTrackingListener, 1)
	poolClosed := false
	err := serve(context.Background(), serveDeps{
		cfg: &config.Config{
			BasePort:    18081,
			StatusPort:  0,
			SlowLatency: 2 * time.Second,
		},
		logger: discardLogger(),
		listen: func(network, address string) (net.Listener, error) {
			raw, err := net.Listen(network, address)
			if err != nil {
				return nil, err
			}
			ln := &closeTrackingListener{
				Listener: raw,
				closed:   make(chan struct{}),
			}
			listeners <- ln
			return ln, nil
		},
		newPool: func(cfg *config.Config, logger *slog.Logger) *manager.Manager {
			return manager.New(cfg, logger)
		},
		bootstrap: func(context.Context, *manager.Manager) error {
			return errors.New("boom")
		},
		closePool: func(*manager.Manager) error {
			poolClosed = true
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "bootstrap pool") {
		t.Fatalf("expected bootstrap pool error, got %v", err)
	}
	var listener *closeTrackingListener
	select {
	case listener = <-listeners:
	case <-time.After(2 * time.Second):
		t.Fatal("listener was not created")
	}
	select {
	case <-listener.closed:
	default:
		t.Fatal("listener was not closed")
	}
	if !poolClosed {
		t.Fatal("pool was not closed")
	}
}

func TestServeHTTPErrorAbortsBootstrap(t *testing.T) {
	ln := &acceptErrorListener{closed: make(chan struct{})}
	poolClosed := false
	err := serve(context.Background(), serveDeps{
		cfg: &config.Config{
			StatusPort:  0,
			SlowLatency: 2 * time.Second,
		},
		logger: discardLogger(),
		listen: func(string, string) (net.Listener, error) {
			return ln, nil
		},
		newPool: func(cfg *config.Config, logger *slog.Logger) *manager.Manager {
			return manager.New(cfg, logger)
		},
		bootstrap: func(ctx context.Context, _ *manager.Manager) error {
			<-ctx.Done()
			return ctx.Err()
		},
		closePool: func(*manager.Manager) error {
			poolClosed = true
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "status server") {
		t.Fatalf("expected status server error, got %v", err)
	}
	select {
	case <-ln.closed:
	default:
		t.Fatal("listener was not closed")
	}
	if !poolClosed {
		t.Fatal("pool was not closed")
	}
}

func waitForStatus(t *testing.T, url string) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("status endpoint %s never became ready: %v", url, err)
	}
	resp.Body.Close()
	t.Fatalf("status endpoint %s never became ready: status %d", url, resp.StatusCode)
}
