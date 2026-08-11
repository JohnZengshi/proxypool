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

func HTTPSuccess(ctx context.Context, d core.Dialer, url string, timeout time.Duration) (Result, error) {
	transport := &http.Transport{
		DialContext: d.DialContext,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return Result{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, resp.Body)
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("traffic probe %s returned HTTP %d", url, resp.StatusCode)
	}
	return Result{Latency: time.Since(start)}, nil
}

func HTTPSuccessAny(ctx context.Context, d core.Dialer, urls []string, timeout time.Duration) (Result, error) {
	var errs []string
	for _, url := range urls {
		res, err := HTTPSuccess(ctx, d, url, timeout)
		if err == nil {
			return res, nil
		}
		errs = append(errs, fmt.Sprintf("%s: %v", url, err))
	}
	if len(errs) == 0 {
		return Result{}, fmt.Errorf("no traffic probe URLs configured")
	}
	return Result{}, fmt.Errorf("traffic probe targets unavailable: %s", strings.Join(errs, "; "))
}

func DirectHTTPSuccess(ctx context.Context, url string, timeout time.Duration) (Result, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return Result{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, resp.Body)
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("traffic probe %s returned HTTP %d", url, resp.StatusCode)
	}
	return Result{Latency: time.Since(start)}, nil
}

func DirectHTTPSuccessAny(ctx context.Context, urls []string, timeout time.Duration) (Result, error) {
	var errs []string
	for _, url := range urls {
		res, err := DirectHTTPSuccess(ctx, url, timeout)
		if err == nil {
			return res, nil
		}
		errs = append(errs, fmt.Sprintf("%s: %v", url, err))
	}
	if len(errs) == 0 {
		return Result{}, fmt.Errorf("no traffic probe URLs configured")
	}
	return Result{}, fmt.Errorf("traffic probe targets unavailable: %s", strings.Join(errs, "; "))
}
