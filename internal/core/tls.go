package core

import (
	"crypto/tls"
	"net"
)

func tlsClient(raw net.Conn, serverName string, insecure bool) *tls.Conn {
	conf := &tls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: insecure,
	}
	return tls.Client(raw, conf)
}
