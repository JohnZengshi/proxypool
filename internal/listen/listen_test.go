package listen

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/john/proxypool/internal/core"
)

type passthroughDialer struct{}

func (p *passthroughDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

func (p *passthroughDialer) Close() error { return nil }

func startServer(t *testing.T, d core.Dialer) (*Server, string) {
	t.Helper()
	s := New("127.0.0.1:0", 0, nil, false, 5*time.Second)
	if d != nil {
		s.SetDialer(d)
	}
	go s.Serve()
	time.Sleep(50 * time.Millisecond)
	addr := (*s.ln.Load()).Addr().String()
	return s, addr
}

func TestSOCKS5Connect(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello-socks")
	}))
	defer upstream.Close()

	s, addr := startServer(t, &passthroughDialer{})
	defer s.Close()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	conn.Write([]byte{0x05, 1, 0x00})
	resp := make([]byte, 2)
	io.ReadFull(conn, resp)
	if resp[0] != 0x05 || resp[1] != 0x00 {
		t.Fatalf("handshake failed: %v", resp)
	}

	host, portStr, _ := net.SplitHostPort(upstream.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	ip := net.ParseIP(host)
	req := []byte{0x05, 0x01, 0x00, 0x01}
	req = append(req, ip.To4()...)
	pb := make([]byte, 2)
	binary.BigEndian.PutUint16(pb, uint16(port))
	req = append(req, pb...)
	conn.Write(req)

	rep := make([]byte, 10)
	io.ReadFull(conn, rep)
	if rep[1] != 0x00 {
		t.Fatalf("connect failed: %v", rep[1])
	}

	conn.Write([]byte("GET / HTTP/1.0\r\n\r\n"))
	br := bufio.NewReader(conn)
	line, _ := br.ReadString('\n')
	if !strings.Contains(line, "200") {
		t.Fatalf("expected 200, got %s", line)
	}
	for {
		l, _ := br.ReadString('\n')
		if strings.TrimSpace(l) == "" {
			break
		}
	}
	body, _ := io.ReadAll(br)
	if !strings.Contains(string(body), "hello-socks") {
		t.Fatalf("expected hello-socks, got %s", string(body))
	}
}

func TestHTTPConnect(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello-http")
	}))
	defer upstream.Close()

	s, addr := startServer(t, &passthroughDialer{})
	defer s.Close()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\n\r\n", upstream.Listener.Addr().String())
	br := bufio.NewReader(conn)
	line, _ := br.ReadString('\n')
	if !strings.Contains(line, "200") {
		t.Fatalf("expected 200, got %s", line)
	}
	for {
		l, _ := br.ReadString('\n')
		if strings.TrimSpace(l) == "" {
			break
		}
	}

	conn.Write([]byte("GET / HTTP/1.0\r\n\r\n"))
	body, _ := io.ReadAll(br)
	if !strings.Contains(string(body), "hello-http") {
		t.Fatalf("expected hello-http, got %s", string(body))
	}
}

func TestHTTPForwardProxy(t *testing.T) {
	var gotHost, gotURI, gotHeader string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		gotURI = r.RequestURI
		gotHeader = r.Header.Get("X-Test")
		gotBody, _ = io.ReadAll(r.Body)
		fmt.Fprint(w, "hello-forward")
	}))
	defer upstream.Close()

	s, addr := startServer(t, &passthroughDialer{})
	defer s.Close()

	host, port, _ := net.SplitHostPort(upstream.Listener.Addr().String())
	upstreamHost := net.JoinHostPort(host, port)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "POST http://%s/echo HTTP/1.1\r\nHost: %s\r\nX-Test: yes\r\nProxy-Connection: keep-alive\r\nContent-Length: 4\r\nConnection: close\r\n\r\nbody", upstreamHost, upstreamHost)
	br := bufio.NewReader(conn)
	line, _ := br.ReadString('\n')
	if !strings.Contains(line, "200") {
		t.Fatalf("expected 200, got %s", line)
	}
	for {
		l, _ := br.ReadString('\n')
		if strings.TrimSpace(l) == "" {
			break
		}
	}
	body, _ := io.ReadAll(br)
	if !strings.Contains(string(body), "hello-forward") {
		t.Fatalf("expected hello-forward, got %s", string(body))
	}
	if gotHost != upstreamHost || gotURI != "/echo" || gotHeader != "yes" || string(gotBody) != "body" {
		t.Fatalf("upstream got host=%q uri=%q header=%q body=%q", gotHost, gotURI, gotHeader, gotBody)
	}
}

func TestNilDialerSocks5(t *testing.T) {
	s, addr := startServer(t, nil)
	defer s.Close()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	conn.Write([]byte{0x05, 1, 0x00})
	resp := make([]byte, 2)
	io.ReadFull(conn, resp)
	if resp[0] != 0x05 || resp[1] != 0x01 {
		t.Fatalf("expected failure 0x01, got %v", resp)
	}
}

func TestNilDialerHTTP(t *testing.T) {
	s, addr := startServer(t, nil)
	defer s.Close()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT example.com:443 HTTP/1.1\r\n\r\n")
	br := bufio.NewReader(conn)
	line, _ := br.ReadString('\n')
	if !strings.Contains(line, "502") {
		t.Fatalf("expected 502, got %s", line)
	}
}

func TestSetDialerNilAfterDialerServesFailure(t *testing.T) {
	s, addr := startServer(t, &passthroughDialer{})
	defer s.Close()

	s.SetDialer(nil)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT example.com:443 HTTP/1.1\r\n\r\n")
	br := bufio.NewReader(conn)
	line, _ := br.ReadString('\n')
	if !strings.Contains(line, "502") {
		t.Fatalf("expected 502, got %s", line)
	}
}

func TestSetDialerNilAfterDialerSocksFailure(t *testing.T) {
	s, addr := startServer(t, &passthroughDialer{})
	defer s.Close()

	s.SetDialer(nil)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	conn.Write([]byte{0x05, 1, 0x00})
	resp := make([]byte, 2)
	io.ReadFull(conn, resp)
	if resp[0] != 0x05 || resp[1] != 0x01 {
		t.Fatalf("expected handshake failure 0x01, got %v", resp)
	}
}

func TestConcurrentConnections(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer upstream.Close()

	s, addr := startServer(t, &passthroughDialer{})
	defer s.Close()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := net.Dial("tcp", addr)
			if err != nil {
				t.Errorf("dial: %v", err)
				return
			}
			defer conn.Close()
			fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\n\r\n", upstream.Listener.Addr().String())
			br := bufio.NewReader(conn)
			line, _ := br.ReadString('\n')
			if !strings.Contains(line, "200") {
				t.Errorf("expected 200, got %s", line)
			}
		}()
	}
	wg.Wait()
}

func TestSOCKS5UDPReject(t *testing.T) {
	s, addr := startServer(t, &passthroughDialer{})
	defer s.Close()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	conn.Write([]byte{0x05, 1, 0x00})
	resp := make([]byte, 2)
	io.ReadFull(conn, resp)

	req := []byte{0x05, 0x03, 0x00, 0x01, 127, 0, 0, 1, 0, 80}
	conn.Write(req)
	rep := make([]byte, 10)
	io.ReadFull(conn, rep)
	if rep[1] != 0x07 {
		t.Fatalf("expected command-not-supported 0x07, got %v", rep[1])
	}
}

func TestRequestLogHTTPConnect(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(h)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello-log")
	}))
	defer upstream.Close()

	s := New("127.0.0.1:0", 19999, logger, true, 5*time.Second)
	s.SetDialer(&passthroughDialer{})
	s.SetExitIP("1.2.3.4")
	go s.Serve()
	time.Sleep(50 * time.Millisecond)
	defer s.Close()

	addr := (*s.ln.Load()).Addr().String()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\n\r\n", upstream.Listener.Addr().String())
	br := bufio.NewReader(conn)
	line, _ := br.ReadString('\n')
	if !strings.Contains(line, "200") {
		t.Fatalf("expected 200, got %s", line)
	}
	for {
		l, _ := br.ReadString('\n')
		if strings.TrimSpace(l) == "" {
			break
		}
	}
	conn.Write([]byte("GET / HTTP/1.0\r\n\r\n"))
	io.ReadAll(br)
	conn.Close()
	s.Close()

	output := buf.String()
	if !strings.Contains(output, "level=INFO") {
		t.Fatalf("expected INFO log, got: %s", output)
	}
	for _, key := range []string{"port=19999", "exit_ip=1.2.3.4", "proto=http", "method=CONNECT", "up_bytes", "down_bytes", "dur_ms"} {
		if !strings.Contains(output, key) {
			t.Fatalf("log missing %s: %s", key, output)
		}
	}
}

func TestRequestLogDisabled(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(h)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	}))
	defer upstream.Close()

	s := New("127.0.0.1:0", 19998, logger, false, 5*time.Second)
	s.SetDialer(&passthroughDialer{})
	go s.Serve()
	time.Sleep(50 * time.Millisecond)
	defer s.Close()

	addr := (*s.ln.Load()).Addr().String()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\n\r\n", upstream.Listener.Addr().String())
	br := bufio.NewReader(conn)
	br.ReadString('\n')
	conn.Write([]byte("GET / HTTP/1.0\r\n\r\n"))
	io.ReadAll(br)
	conn.Close()
	s.Close()

	if buf.String() != "" {
		t.Fatalf("expected no logs when disabled, got: %s", buf.String())
	}
}

func TestRequestLogDialError(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(h)

	s := New("127.0.0.1:0", 19997, logger, true, 5*time.Second)
	s.SetDialer(&errDialer{})
	go s.Serve()
	time.Sleep(50 * time.Millisecond)
	defer s.Close()

	addr := (*s.ln.Load()).Addr().String()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT 127.0.0.1:1 HTTP/1.1\r\n\r\n")
	br := bufio.NewReader(conn)
	io.ReadAll(br)
	conn.Close()
	s.Close()

	output := buf.String()
	if !strings.Contains(output, "level=WARN") {
		t.Fatalf("expected WARN log, got: %s", output)
	}
	if strings.Contains(output, "level=INFO") {
		t.Fatalf("expected no INFO log on error, got: %s", output)
	}
}

type blockingDialer struct{}

func (b *blockingDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (b *blockingDialer) Close() error { return nil }

func TestDialTimeoutReturnsProtocolFailure(t *testing.T) {
	s := New("127.0.0.1:0", 0, nil, false, 100*time.Millisecond)
	s.SetDialer(&blockingDialer{})
	go s.Serve()
	time.Sleep(50 * time.Millisecond)
	defer s.Close()

	addr := (*s.ln.Load()).Addr().String()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	start := time.Now()
	fmt.Fprintf(conn, "CONNECT example.com:443 HTTP/1.1\r\n\r\n")
	br := bufio.NewReader(conn)
	line, _ := br.ReadString('\n')
	if !strings.Contains(line, "502") {
		t.Fatalf("expected 502, got %s", line)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout response took %v", elapsed)
	}
}

func TestSOCKSDialTimeoutReturnsFailure(t *testing.T) {
	s := New("127.0.0.1:0", 0, nil, false, 100*time.Millisecond)
	s.SetDialer(&blockingDialer{})
	go s.Serve()
	time.Sleep(50 * time.Millisecond)
	defer s.Close()

	addr := (*s.ln.Load()).Addr().String()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	conn.Write([]byte{0x05, 1, 0x00})
	resp := make([]byte, 2)
	io.ReadFull(conn, resp)

	start := time.Now()
	req := []byte{0x05, 0x01, 0x00, 0x01, 127, 0, 0, 1, 0, 80}
	conn.Write(req)
	rep := make([]byte, 10)
	io.ReadFull(conn, rep)
	if rep[1] != 0x01 {
		t.Fatalf("expected general failure 0x01, got %v", rep[1])
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout response took %v", elapsed)
	}
}

type errDialer struct{}

func (e *errDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return nil, fmt.Errorf("connection refused")
}

func (e *errDialer) Close() error { return nil }
