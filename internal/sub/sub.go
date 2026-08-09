package sub

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"

	"github.com/john/proxypool/internal/config"
	"gopkg.in/yaml.v3"
)

type Node struct {
	Tag      string
	Name     string
	Type     string
	Server   string
	Port     int
	Outbound option.Outbound
}

func (n Node) Key() string {
	return n.Tag + "|" + net.JoinHostPort(n.Server, strconv.Itoa(n.Port))
}

type FetchResult struct {
	Nodes        []Node
	Skipped      int
	SkippedTypes map[string]int
}

var dialableTypes = map[string]bool{
	"vmess":       true,
	"shadowsocks": true,
	"anytls":      true,
	"hysteria2":   true,
	"trojan":      true,
	"vless":       true,
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

func Parse(src config.Source, data []byte) (*FetchResult, error) {
	switch src.Type {
	case config.SourceClash:
		return parseClash(src.Tag, data)
	case config.SourceSingBox:
		return parseSingBox(src.Tag, data)
	default:
		return nil, fmt.Errorf("unknown source type %q", src.Type)
	}
}

type clashProxy struct {
	Name           string `yaml:"name"`
	Server         string `yaml:"server"`
	Port           int    `yaml:"port"`
	Type           string `yaml:"type"`
	UUID           string `yaml:"uuid"`
	AlterID        int    `yaml:"alterId"`
	Cipher         string `yaml:"cipher"`
	Network        string `yaml:"network"`
	TLS            bool   `yaml:"tls"`
	SkipCertVerify bool   `yaml:"skip-cert-verify"`
}

type clashConfig struct {
	Proxies []clashProxy `yaml:"proxies"`
}

func parseClash(tag string, data []byte) (*FetchResult, error) {
	var cc clashConfig
	if err := yaml.Unmarshal(data, &cc); err != nil {
		return nil, fmt.Errorf("parse clash yaml: %w", err)
	}
	result := &FetchResult{SkippedTypes: make(map[string]int)}
	for _, p := range cc.Proxies {
		if p.Type != "vmess" {
			result.Skipped++
			result.SkippedTypes[p.Type]++
			continue
		}
		result.Nodes = append(result.Nodes, Node{
			Tag:      tag,
			Name:     p.Name,
			Type:     "vmess",
			Server:   p.Server,
			Port:     p.Port,
			Outbound: vmessToOutbound(p),
		})
	}
	return result, nil
}

func vmessToOutbound(p clashProxy) option.Outbound {
	security := p.Cipher
	if security == "" {
		security = "auto"
	}
	var tlsOpts *option.OutboundTLSOptions
	if p.TLS {
		tlsOpts = &option.OutboundTLSOptions{Enabled: true, Insecure: p.SkipCertVerify}
	}
	vo := &option.VMessOutboundOptions{
		ServerOptions:               option.ServerOptions{Server: p.Server, ServerPort: uint16(p.Port)},
		UUID:                        p.UUID,
		Security:                    security,
		AlterId:                     p.AlterID,
		OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{TLS: tlsOpts},
	}
	return option.Outbound{Type: "vmess", Tag: p.Name, Options: vo}
}

type rawOutbounds struct {
	Outbounds []json.RawMessage `json:"outbounds"`
}

func parseSingBox(tag string, data []byte) (*FetchResult, error) {
	registry := include.OutboundRegistry()
	ctx := service.ContextWith[option.OutboundOptionsRegistry](context.Background(), registry)

	var raw rawOutbounds
	if err := json.UnmarshalContext(ctx, data, &raw); err != nil {
		return nil, fmt.Errorf("parse sing-box json: %w", err)
	}

	result := &FetchResult{SkippedTypes: make(map[string]int)}
	for _, b := range raw.Outbounds {
		var head struct {
			Type string `json:"type"`
		}
		_ = json.UnmarshalContext(ctx, b, &head)
		if !dialableTypes[head.Type] {
			result.Skipped++
			result.SkippedTypes[head.Type]++
			continue
		}
		var ob option.Outbound
		if err := json.UnmarshalContext(ctx, b, &ob); err != nil {
			result.Skipped++
			result.SkippedTypes[head.Type]++
			continue
		}
		server, port := serverFromOptions(ob.Options)
		if server == "" || port == 0 {
			result.Skipped++
			result.SkippedTypes[ob.Type]++
			continue
		}
		result.Nodes = append(result.Nodes, Node{
			Tag:      tag,
			Name:     ob.Tag,
			Type:     ob.Type,
			Server:   server,
			Port:     port,
			Outbound: ob,
		})
	}
	return result, nil
}

func serverFromOptions(opts any) (string, int) {
	sw, ok := opts.(option.ServerOptionsWrapper)
	if !ok {
		return "", 0
	}
	so := sw.TakeServerOptions()
	return so.Server, int(so.ServerPort)
}
