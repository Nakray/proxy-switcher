package proxy

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/Nakray/proxy-switcher/internal/config"
	"github.com/Nakray/proxy-switcher/internal/healthcheck"
	"github.com/Nakray/proxy-switcher/internal/metrics"
)

const (
	socks5Version     = 0x05
	socks5AuthNone    = 0x00
	socks5AuthPass    = 0x02
	socks5CmdConnect  = 0x01
	socks5AtypIPv4    = 0x01
	socks5AtypDomain  = 0x03
	socks5AtypIPv6    = 0x04
	socks5RespSuccess = 0x00
)

// SOCKS5Proxy represents a SOCKS5 proxy server
type SOCKS5Proxy struct {
	config      *config.Config
	healthCheck *healthcheck.Checker
	metrics     *metrics.SafeCollector
	logger      *zap.Logger

	listener net.Listener
	wg       sync.WaitGroup
	mu       sync.Mutex
	running  bool
}

// NewSOCKS5Proxy creates a new SOCKS5 proxy server
func NewSOCKS5Proxy(
	cfg *config.Config,
	hc *healthcheck.Checker,
	m *metrics.SafeCollector,
	logger *zap.Logger,
) *SOCKS5Proxy {
	return &SOCKS5Proxy{
		config:      cfg,
		healthCheck: hc,
		metrics:     m,
		logger:      logger,
	}
}

// Start starts the SOCKS5 proxy server
func (p *SOCKS5Proxy) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return fmt.Errorf("SOCKS5 proxy already running")
	}
	p.running = true
	p.mu.Unlock()

	addr := fmt.Sprintf(":%d", p.config.Proxy.SOCKS5Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to start SOCKS5 listener: %w", err)
	}

	p.listener = listener
	p.logger.Info("SOCKS5 proxy started", zap.Int("port", p.config.Proxy.SOCKS5Port))

	go func() {
		<-ctx.Done()
		p.Stop()
	}()

	p.acceptLoop()
	return nil
}

// Stop stops the SOCKS5 proxy server
func (p *SOCKS5Proxy) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.running {
		return
	}

	p.logger.Info("Stopping SOCKS5 proxy")
	if p.listener != nil {
		p.listener.Close()
	}
	p.running = false
	p.wg.Wait()
}

func (p *SOCKS5Proxy) acceptLoop() {
	for {
		p.mu.Lock()
		running := p.running
		p.mu.Unlock()

		if !running {
			return
		}

		conn, err := p.listener.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				continue
			}
			p.logger.Debug("SOCKS5 accept error", zap.Error(err))
			return
		}

		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.handleConnection(conn)
		}()
	}
}

func (p *SOCKS5Proxy) handleConnection(clientConn net.Conn) {
	defer clientConn.Close()

	p.logger.Debug("New SOCKS5 connection", zap.String("remote", clientConn.RemoteAddr().String()))
	p.metrics.IncTotalConnections()
	p.metrics.IncActiveConnections()
	defer p.metrics.DecActiveConnections()

	startTime := time.Now()

	// Handshake
	if err := p.handshake(clientConn); err != nil {
		p.logger.Debug("SOCKS5 handshake failed", zap.Error(err))
		return
	}

	// Process request
	if err := p.processRequest(clientConn, startTime); err != nil {
		p.logger.Debug("SOCKS5 request failed", zap.Error(err))
	}
}

func (p *SOCKS5Proxy) handshake(conn net.Conn) error {
	// Read version and number of auth methods
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return fmt.Errorf("failed to read version: %w", err)
	}

	if buf[0] != socks5Version {
		return fmt.Errorf("unsupported SOCKS version: %d", buf[0])
	}

	numMethods := int(buf[1])
	methods := make([]byte, numMethods)
	if _, err := io.ReadFull(conn, methods); err != nil {
		return fmt.Errorf("failed to read methods: %w", err)
	}

	// Select authentication method (no auth for now)
	if _, err := conn.Write([]byte{socks5Version, socks5AuthNone}); err != nil {
		return fmt.Errorf("failed to write auth response: %w", err)
	}

	return nil
}

func (p *SOCKS5Proxy) processRequest(conn net.Conn, startTime time.Time) error {
	// Read request header
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return fmt.Errorf("failed to read request header: %w", err)
	}

	version := header[0]
	cmd := header[1]
	atyp := header[3]

	if version != socks5Version {
		return fmt.Errorf("unsupported SOCKS version: %d", version)
	}

	if cmd != socks5CmdConnect {
		p.sendResponse(conn, 0x07) // Command not supported
		return fmt.Errorf("unsupported command: %d", cmd)
	}

	// Read destination address
	var destAddr string
	switch atyp {
	case socks5AtypIPv4:
		addr := make([]byte, 4)
		if _, err := io.ReadFull(conn, addr); err != nil {
			return fmt.Errorf("failed to read IPv4: %w", err)
		}
		destAddr = net.IP(addr).String()
	case socks5AtypDomain:
		var domainLen byte
		if _, err := io.ReadFull(conn, []byte{domainLen}); err != nil {
			return fmt.Errorf("failed to read domain length: %w", err)
		}
		domain := make([]byte, domainLen)
		if _, err := io.ReadFull(conn, domain); err != nil {
			return fmt.Errorf("failed to read domain: %w", err)
		}
		destAddr = string(domain)
	case socks5AtypIPv6:
		addr := make([]byte, 16)
		if _, err := io.ReadFull(conn, addr); err != nil {
			return fmt.Errorf("failed to read IPv6: %w", err)
		}
		destAddr = net.IP(addr).String()
	default:
		return fmt.Errorf("unsupported address type: %d", atyp)
	}

	// Read destination port
	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBuf); err != nil {
		return fmt.Errorf("failed to read port: %w", err)
	}
	destPort := binary.BigEndian.Uint16(portBuf)

	dest := fmt.Sprintf("%s:%d", destAddr, destPort)
	p.logger.Debug("SOCKS5 request", zap.String("destination", dest))

	// Get best upstream
	upstream := p.healthCheck.GetBestUpstream(config.UpstreamTypeSOCKS5)
	if upstream == nil {
		p.logger.Warn("No healthy upstream available")
		p.sendResponse(conn, 0x06) // Host unreachable
		return fmt.Errorf("no healthy upstream")
	}

	p.metrics.IncUpstreamRequests(upstream.Name, string(upstream.Type))

	// Connect to upstream
	upstreamConn, err := p.connectToUpstream(upstream, dest)
	if err != nil {
		p.logger.Debug("Failed to connect to upstream", zap.Error(err))
		p.metrics.IncUpstreamFailures(upstream.Name, string(upstream.Type))
		p.sendResponse(conn, 0x05) // Connection refused
		return fmt.Errorf("failed to connect to upstream: %w", err)
	}
	defer upstreamConn.Close()

	// Send success response
	p.sendResponse(conn, socks5RespSuccess)

	// Start bidirectional copy
	p.relayTraffic(conn, upstreamConn, upstream, startTime)

	return nil
}

func (p *SOCKS5Proxy) connectToUpstream(upstream *config.Upstream, dest string) (net.Conn, error) {
	// Connect to upstream proxy
	upstreamAddr := fmt.Sprintf("%s:%d", upstream.Host, upstream.Port)
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.Dial("tcp", upstreamAddr)
	if err != nil {
		return nil, err
	}

	// Perform SOCKS5 handshake with upstream
	if err := p.upstreamHandshake(conn, upstream); err != nil {
		conn.Close()
		return nil, err
	}

	// Send CONNECT request to upstream
	if err := p.upstreamConnect(conn, dest); err != nil {
		conn.Close()
		return nil, err
	}

	return conn, nil
}

func (p *SOCKS5Proxy) upstreamHandshake(conn net.Conn, upstream *config.Upstream) error {
	// Send version and auth methods
	authMethods := []byte{socks5AuthNone}
	if upstream.Username != "" && upstream.Password != "" {
		authMethods = append(authMethods, socks5AuthPass)
	}

	buf := make([]byte, 0, 2+len(authMethods))
	buf = append(buf, socks5Version, byte(len(authMethods)))
	buf = append(buf, authMethods...)

	if _, err := conn.Write(buf); err != nil {
		return fmt.Errorf("failed to send handshake: %w", err)
	}

	// Read response
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return fmt.Errorf("failed to read handshake response: %w", err)
	}

	if resp[0] != socks5Version {
		return fmt.Errorf("unsupported upstream SOCKS version: %d", resp[0])
	}

	if resp[1] == socks5AuthPass && upstream.Username != "" {
		// Perform username/password authentication
		if err := p.authenticateUpstream(conn, upstream.Username, upstream.Password); err != nil {
			return err
		}
	} else if resp[1] != socks5AuthNone {
		return fmt.Errorf("unsupported auth method: %d", resp[1])
	}

	return nil
}

func (p *SOCKS5Proxy) authenticateUpstream(conn net.Conn, username, password string) error {
	buf := make([]byte, 0, 2+len(username)+len(password))
	buf = append(buf, 0x01, byte(len(username)))
	buf = append(buf, []byte(username)...)
	buf = append(buf, byte(len(password)))
	buf = append(buf, []byte(password)...)

	if _, err := conn.Write(buf); err != nil {
		return fmt.Errorf("failed to send auth: %w", err)
	}

	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return fmt.Errorf("failed to read auth response: %w", err)
	}

	if resp[1] != 0x00 {
		return fmt.Errorf("authentication failed")
	}

	return nil
}

func (p *SOCKS5Proxy) upstreamConnect(conn net.Conn, dest string) error {
	// Parse destination
	host, portStr, err := net.SplitHostPort(dest)
	if err != nil {
		return err
	}
	var port uint16
	fmt.Sscanf(portStr, "%d", &port)

	// Build CONNECT request
	var req []byte
	req = append(req, socks5Version, socks5CmdConnect, 0x00)

	// Check if host is IP or domain
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			req = append(req, socks5AtypIPv4)
			req = append(req, ip4...)
		} else {
			req = append(req, socks5AtypIPv6)
			req = append(req, ip.To16()...)
		}
	} else {
		req = append(req, socks5AtypDomain, byte(len(host)))
		req = append(req, []byte(host)...)
	}

	req = append(req, byte(port>>8), byte(port&0xff))

	if _, err := conn.Write(req); err != nil {
		return fmt.Errorf("failed to send connect request: %w", err)
	}

	// Read response
	resp := make([]byte, 4)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return fmt.Errorf("failed to read connect response: %w", err)
	}

	if resp[1] != socks5RespSuccess {
		return fmt.Errorf("upstream connect failed with status: %d", resp[1])
	}

	// Skip bound address
	switch resp[3] {
	case socks5AtypIPv4:
		io.CopyN(io.Discard, conn, 4+2)
	case socks5AtypDomain:
		var domainLen byte
		io.ReadFull(conn, []byte{domainLen})
		io.CopyN(io.Discard, conn, int64(domainLen)+2)
	case socks5AtypIPv6:
		io.CopyN(io.Discard, conn, 16+2)
	}

	return nil
}

func (p *SOCKS5Proxy) sendResponse(conn net.Conn, reply byte) {
	resp := make([]byte, 4)
	resp[0] = socks5Version
	resp[1] = reply
	resp[2] = 0x00
	resp[3] = socks5AtypIPv4
	// Bind address 0.0.0.0:0
	resp = append(resp, 0, 0, 0, 0, 0, 0)
	conn.Write(resp)
}

func (p *SOCKS5Proxy) relayTraffic(clientConn, upstreamConn net.Conn, upstream *config.Upstream, startTime time.Time) {
	var wg sync.WaitGroup
	bytesTransferred := int64(0)

	// Client -> Upstream
	wg.Add(1)
	go func() {
		defer wg.Done()
		n, _ := io.Copy(upstreamConn, clientConn)
		clientConn.SetReadDeadline(time.Now())
		upstreamConn.SetWriteDeadline(time.Now())
		bytesTransferred += n
	}()

	// Upstream -> Client
	wg.Add(1)
	go func() {
		defer wg.Done()
		n, _ := io.Copy(clientConn, upstreamConn)
		clientConn.SetWriteDeadline(time.Now())
		upstreamConn.SetReadDeadline(time.Now())
		bytesTransferred += n
	}()

	wg.Wait()

	duration := time.Since(startTime)
	p.metrics.AddBytesTransferred(bytesTransferred)
	p.metrics.ObserveConnectionDuration(duration)

	p.logger.Debug("Connection closed",
		zap.String("upstream", upstream.Name),
		zap.Int64("bytes", bytesTransferred),
		zap.Duration("duration", duration))
}
