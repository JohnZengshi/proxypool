package sub

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"gopkg.in/yaml.v3"
)

type Node struct {
	Name           string `yaml:"name"`
	Server         string `yaml:"server"`
	Port           int    `yaml:"port"`
	Type           string `yaml:"type"`
	UUID           string `yaml:"uuid"`
	AlterID        int    `yaml:"alterId"`
	Cipher         string `yaml:"cipher"`
	TLS            bool   `yaml:"tls"`
	SkipCertVerify bool   `yaml:"skip-cert-verify"`
	UDP            bool   `yaml:"udp"`
}

func (n Node) Key() string {
	return fmt.Sprintf("%s:%d", n.Server, n.Port)
}

type clashConfig struct {
	Proxies []Node `yaml:"proxies"`
}

type FetchResult struct {
	Nodes        []Node
	Skipped      int
	SkippedTypes map[string]int
}

func Fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "clash-verge/v1")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch subscription: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("subscription returned HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func Parse(data []byte) (*FetchResult, error) {
	var cc clashConfig
	if err := yaml.Unmarshal(data, &cc); err != nil {
		return nil, fmt.Errorf("parse clash yaml: %w", err)
	}
	result := &FetchResult{
		SkippedTypes: make(map[string]int),
	}
	for _, n := range cc.Proxies {
		if n.Type != "vmess" {
			result.Skipped++
			result.SkippedTypes[n.Type]++
			continue
		}
		if n.Cipher == "" {
			n.Cipher = "auto"
		}
		result.Nodes = append(result.Nodes, n)
	}
	return result, nil
}
