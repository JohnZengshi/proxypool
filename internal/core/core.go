package core

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"

	"github.com/john/proxypool/internal/sub"
)

type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
	Close() error
}

type singDialer struct {
	outbound adapter.Outbound
}

func NewOutbound(node sub.Node, dialTimeout time.Duration) (Dialer, error) {
	ob := node.Outbound

	// sing-box's dialer.New resolves domain servers through its own
	// DNSTransportManager, which we do not run. Resolve up front to an IP and
	// carry the original domain as TLS SNI so ServerIsDomain() is false.
	if err := resolveServerIP(context.Background(), &ob, dialTimeout); err != nil {
		return nil, fmt.Errorf("resolve server for %s: %w", node.Name, err)
	}
	applyDialTimeout(&ob, dialTimeout)

	registry := include.OutboundRegistry()
	ctx := service.ContextWith[option.OutboundOptionsRegistry](context.Background(), registry)
	out, err := registry.CreateOutbound(ctx, nil, log.NewNOPFactory().Logger(), node.Tag, ob.Type, ob.Options)
	if err != nil {
		return nil, fmt.Errorf("create %s outbound for %s: %w", ob.Type, node.Name, err)
	}
	return &singDialer{outbound: out}, nil
}

func (d *singDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("split addr %s: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("parse port %s: %w", portStr, err)
	}
	destination := M.ParseSocksaddrHostPort(host, uint16(port))
	return d.outbound.DialContext(ctx, network, destination)
}

func (d *singDialer) Close() error {
	if c, ok := d.outbound.(interface{ Close() error }); ok {
		return c.Close()
	}
	return nil
}

func resolveServerIP(ctx context.Context, ob *option.Outbound, timeout time.Duration) error {
	sw, ok := ob.Options.(option.ServerOptionsWrapper)
	if !ok {
		return nil
	}
	so := sw.TakeServerOptions()
	if so.Server == "" {
		return fmt.Errorf("empty server")
	}
	if net.ParseIP(so.Server) != nil {
		return nil
	}

	domain := so.Server
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(rctx, domain)
	if err != nil {
		return err
	}
	resolved := pickIPv4(ips)
	if resolved == "" {
		return fmt.Errorf("no IP for %s", domain)
	}

	if tw, ok := ob.Options.(option.OutboundTLSOptionsWrapper); ok {
		tls := tw.TakeOutboundTLSOptions()
		if tls != nil && tls.Enabled && tls.ServerName == "" {
			tls.ServerName = domain
			tw.ReplaceOutboundTLSOptions(tls)
		}
	}

	so.Server = resolved
	sw.ReplaceServerOptions(so)
	return nil
}

func pickIPv4(ips []net.IPAddr) string {
	for _, ip := range ips {
		if v4 := ip.IP.To4(); v4 != nil {
			return v4.String()
		}
	}
	if len(ips) > 0 {
		return ips[0].IP.String()
	}
	return ""
}

func applyDialTimeout(ob *option.Outbound, dialTimeout time.Duration) {
	dw, ok := ob.Options.(option.DialerOptionsWrapper)
	if !ok {
		return
	}
	do := dw.TakeDialerOptions()
	do.ConnectTimeout = badoption.Duration(dialTimeout)
	dw.ReplaceDialerOptions(do)
}
