package core

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/sagernet/sing-vmess"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/john/proxypool/internal/sub"
)

type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
	Close() error
}

type vmessDialer struct {
	client   *vmess.Client
	server   string
	port     int
	tls      bool
	skipCert bool
	nd       *net.Dialer
}

func NewOutbound(node sub.Node, dialTimeout time.Duration) (Dialer, error) {
	security := node.Cipher
	if security == "" {
		security = "auto"
	}
	if security == "auto" && node.TLS {
		security = "zero"
	}
	client, err := vmess.NewClient(node.UUID, security, node.AlterID)
	if err != nil {
		return nil, fmt.Errorf("vmess client for %s: %w", node.Name, err)
	}
	return &vmessDialer{
		client:   client,
		server:   node.Server,
		port:     node.Port,
		tls:      node.TLS,
		skipCert: node.SkipCertVerify,
		nd: &net.Dialer{
			Timeout: dialTimeout,
		},
	}, nil
}

func (d *vmessDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("split addr %s: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("parse port %s: %w", portStr, err)
	}
	destination := M.ParseSocksaddrHostPort(host, uint16(port))

	serverAddr := net.JoinHostPort(d.server, strconv.Itoa(d.port))
	rawConn, err := d.nd.DialContext(ctx, "tcp", serverAddr)
	if err != nil {
		return nil, fmt.Errorf("dial server %s: %w", serverAddr, err)
	}

	var upstream net.Conn = rawConn
	if d.tls {
		tlsConn := tlsClient(rawConn, d.server, d.skipCert)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			rawConn.Close()
			return nil, fmt.Errorf("tls handshake to %s: %w", serverAddr, err)
		}
		upstream = tlsConn
	}

	vconn := d.client.DialEarlyConn(upstream, destination)
	_ = N.NetworkTCP
	return vconn, nil
}

func (d *vmessDialer) Close() error {
	return nil
}
