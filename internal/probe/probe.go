package probe

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/john/proxypool/internal/core"
)

type Result struct {
	IP      string
	Latency time.Duration
}

func Exit(ctx context.Context, d core.Dialer, urls []string, timeout time.Duration) (Result, error) {
	transport := &http.Transport{
		DialContext: d.DialContext,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}

	var lastErr error
	for _, url := range urls {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		start := time.Now()
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		ip := strings.TrimSpace(string(body))
		if net.ParseIP(ip) == nil {
			lastErr = fmt.Errorf("probe %s returned non-IP: %s", url, ip)
			continue
		}
		return Result{IP: ip, Latency: time.Since(start)}, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no probe URLs configured")
	}
	return Result{}, lastErr
}
