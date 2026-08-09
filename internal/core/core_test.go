package core

import (
	"testing"
	"time"

	"github.com/john/proxypool/internal/sub"
	"github.com/sagernet/sing-box/option"
)

func outboundFor(t *testing.T, typ, tag, server string, port uint16, opts any) sub.Node {
	t.Helper()
	return sub.Node{
		Tag:      tag,
		Name:     tag,
		Type:     typ,
		Server:   server,
		Port:     int(port),
		Outbound: option.Outbound{Type: typ, Tag: tag, Options: opts},
	}
}

func TestNewOutboundAllProtocols(t *testing.T) {
	// 127.0.0.1 avoids real DNS/network during construction; the outbound is
	// built but not dialed here.
	cases := []struct {
		typ  string
		opts any
	}{
		{"vmess", &option.VMessOutboundOptions{
			ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: 443},
			UUID:          "00000000-0000-0000-0000-000000000001",
			Security:      "auto",
		}},
		{"shadowsocks", &option.ShadowsocksOutboundOptions{
			ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: 8388},
			Method:        "aes-256-gcm",
			Password:      "test-password",
		}},
		{"anytls", &option.AnyTLSOutboundOptions{
			ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: 443},
			Password:      "test-password",
			OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
				TLS: &option.OutboundTLSOptions{Enabled: true, Insecure: true},
			},
		}},
		{"hysteria2", &option.Hysteria2OutboundOptions{
			ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: 443},
			Password:      "test-password",
			OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
				TLS: &option.OutboundTLSOptions{Enabled: true, Insecure: true},
			},
		}},
	}
	for _, c := range cases {
		t.Run(c.typ, func(t *testing.T) {
			node := outboundFor(t, c.typ, "test-"+c.typ, "127.0.0.1", 443, c.opts)
			d, err := NewOutbound(node, 5*time.Second)
			if err != nil {
				t.Fatalf("NewOutbound(%s): %v", c.typ, err)
			}
			if d == nil {
				t.Fatalf("NewOutbound(%s) returned nil", c.typ)
			}
			if err := d.Close(); err != nil {
				t.Fatalf("Close(%s): %v", c.typ, err)
			}
		})
	}
}

func TestNewOutboundHysteria2(t *testing.T) {
	node := outboundFor(t, "hysteria2", "hy2-1", "127.0.0.1", 8443, &option.Hysteria2OutboundOptions{
		ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: 8443},
		Password:      "test-password",
		OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
			TLS: &option.OutboundTLSOptions{Enabled: true, Insecure: true},
		},
	})
	d, err := NewOutbound(node, 5*time.Second)
	if err != nil {
		t.Fatalf("NewOutbound hysteria2: %v", err)
	}
	if d == nil {
		t.Fatal("nil dialer")
	}
	d.Close()
}

func TestNewOutboundDomainResolved(t *testing.T) {
	// A domain server must be resolved to an IP at construction; localhost is
	// used so the lookup succeeds without external network.
	node := outboundFor(t, "vmess", "dom-1", "localhost", 443, &option.VMessOutboundOptions{
		ServerOptions: option.ServerOptions{Server: "localhost", ServerPort: 443},
		UUID:          "00000000-0000-0000-0000-000000000001",
		Security:      "auto",
		OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
			TLS: &option.OutboundTLSOptions{Enabled: true},
		},
	})
	d, err := NewOutbound(node, 5*time.Second)
	if err != nil {
		t.Fatalf("NewOutbound domain: %v", err)
	}
	d.Close()
}
