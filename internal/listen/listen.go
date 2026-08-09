package listen

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/john/proxypool/internal/core"
)

type Server struct {
	addr   string
	dialer atomic.Pointer[core.Dialer]
	ln     atomic.Pointer[net.Listener]
	wg     sync.WaitGroup

	logger     *slog.Logger
	port       int
	logEnabled bool
	exitIP     atomic.Pointer[string]
}

func New(addr string, port int, logger *slog.Logger, logEnabled bool) *Server {
	return &Server{addr: addr, port: port, logger: logger, logEnabled: logEnabled}
}

func (s *Server) SetDialer(d core.Dialer) {
	s.dialer.Store(&d)
}

func (s *Server) SetExitIP(ip string) {
	s.exitIP.Store(&ip)
}

func (s *Server) log() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
}

func (s *Server) reqLog(proto, method, target, status string, durMs, up, down int64) {
	if !s.logEnabled {
		return
	}
	ip := ""
	if p := s.exitIP.Load(); p != nil {
		ip = *p
	}
	s.log().Info("proxy request",
		"port", s.port,
		"exit_ip", ip,
		"proto", proto,
		"method", method,
		"target", target,
		"status", status,
		"dur_ms", durMs,
		"up_bytes", up,
		"down_bytes", down,
	)
}

func (s *Server) warnLog(proto, target, errMsg string) {
	if !s.logEnabled {
		return
	}
	ip := ""
	if p := s.exitIP.Load(); p != nil {
		ip = *p
	}
	s.log().Warn("proxy error",
		"port", s.port,
		"exit_ip", ip,
		"proto", proto,
		"target", target,
		"err", errMsg,
	)
}

func (s *Server) Serve() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.addr, err)
	}
	s.ln.Store(&ln)
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		s.wg.Add(1)
		go s.handle(conn)
	}
}

func (s *Server) Close() error {
	if p := s.ln.Load(); p != nil {
		(*p).Close()
	}
	s.wg.Wait()
	return nil
}

func relay(a, b net.Conn) (up, down int64) {
	done := make(chan struct{}, 2)
	var n1, n2 int64
	go func() {
		n1, _ = io.Copy(a, b)
		done <- struct{}{}
	}()
	go func() {
		n2, _ = io.Copy(b, a)
		done <- struct{}{}
	}()
	<-done
	a.Close()
	b.Close()
	<-done
	return n1, n2
}

func (s *Server) handle(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()

	br := bufio.NewReader(conn)
	firstByte, err := br.ReadByte()
	if err != nil {
		return
	}
	br.UnreadByte()

	dp := s.dialer.Load()
	if dp == nil {
		if firstByte == 0x05 {
			conn.Write([]byte{0x05, 0x01})
		} else {
			conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		}
		return
	}

	if firstByte == 0x05 {
		s.handleSocks5(conn, br)
	} else {
		s.handleHTTP(conn, br)
	}
}

func (s *Server) handleSocks5(conn net.Conn, br *bufio.Reader) {
	start := time.Now()
	ver, err := br.ReadByte()
	if err != nil {
		return
	}
	if ver != 0x05 {
		return
	}
	nmethods, err := br.ReadByte()
	if err != nil {
		return
	}
	methods := make([]byte, nmethods)
	if _, err := io.ReadFull(br, methods); err != nil {
		return
	}
	conn.Write([]byte{0x05, 0x00})

	hdr := make([]byte, 4)
	if _, err := io.ReadFull(br, hdr); err != nil {
		return
	}
	if hdr[0] != 0x05 {
		return
	}
	if hdr[1] != 0x01 {
		conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		s.warnLog("socks5", "", "command not supported")
		return
	}

	var host string
	switch hdr[3] {
	case 0x01:
		buf := make([]byte, 4)
		io.ReadFull(br, buf)
		host = net.IP(buf).String()
	case 0x03:
		lenByte, err := br.ReadByte()
		if err != nil {
			return
		}
		buf := make([]byte, lenByte)
		io.ReadFull(br, buf)
		host = string(buf)
	case 0x04:
		buf := make([]byte, 16)
		io.ReadFull(br, buf)
		host = net.IP(buf).String()
	default:
		conn.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		s.warnLog("socks5", "", "address type not supported")
		return
	}

	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(br, portBuf); err != nil {
		return
	}
	port := binary.BigEndian.Uint16(portBuf)
	dest := net.JoinHostPort(host, strconv.Itoa(int(port)))

	dp := s.dialer.Load()
	if dp == nil {
		conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		s.warnLog("socks5", dest, "no dialer")
		return
	}

	remote, err := (*dp).DialContext(context.Background(), "tcp", dest)
	if err != nil {
		conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		s.warnLog("socks5", dest, err.Error())
		return
	}
	defer remote.Close()

	conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	up, down := relay(conn, remote)
	s.reqLog("socks5", "CONNECT", dest, "ok", time.Since(start).Milliseconds(), up, down)
}

func (s *Server) handleHTTP(conn net.Conn, br *bufio.Reader) {
	start := time.Now()
	line, err := br.ReadString('\n')
	if err != nil {
		return
	}
	line = strings.TrimSpace(line)
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 3 {
		conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
		s.warnLog("http", "", "malformed request line")
		return
	}
	method := parts[0]
	target := parts[1]

	for {
		hdrLine, err := br.ReadString('\n')
		if err != nil {
			return
		}
		if strings.TrimSpace(hdrLine) == "" {
			break
		}
	}

	dp := s.dialer.Load()
	if dp == nil {
		conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		s.warnLog("http", target, "no dialer")
		return
	}

	if method == "CONNECT" {
		remote, err := (*dp).DialContext(context.Background(), "tcp", target)
		if err != nil {
			conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
			s.warnLog("http", target, err.Error())
			return
		}
		defer remote.Close()
		conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		up, down := relay(conn, remote)
		s.reqLog("http", method, target, "ok", time.Since(start).Milliseconds(), up, down)
	} else {
		remote, err := (*dp).DialContext(context.Background(), "tcp", target)
		if err != nil {
			conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
			s.warnLog("http", target, err.Error())
			return
		}
		defer remote.Close()
		fmt.Fprintf(remote, "%s %s %s\r\n", method, target, parts[2])
		fmt.Fprintf(remote, "Connection: close\r\n\r\n")
		up, _ := io.Copy(remote, br)
		if c, ok := remote.(*net.TCPConn); ok {
			c.CloseWrite()
		}
		down, _ := io.Copy(conn, remote)
		s.reqLog("http", method, target, "ok", time.Since(start).Milliseconds(), up, down)
	}
}
