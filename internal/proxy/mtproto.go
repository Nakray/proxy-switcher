package proxy

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/Nakray/proxy-switcher/internal/config"
	"github.com/Nakray/proxy-switcher/internal/healthcheck"
	"github.com/Nakray/proxy-switcher/internal/metrics"
)

// MTProto constants
const (
	mtprotoTagInit      = 0x42544648 // "BTFH"
	mtprotoTagAbridged  = 0xefefefef
	mtprotoTagFull      = 0x00000000
	mtprotoMaxPacketLen = 16 * 1024
)

// MTProtoProxy represents an MTProto proxy server
type MTProtoProxy struct {
	config      *config.Config
	healthCheck *healthcheck.Checker
	metrics     *metrics.SafeCollector
	logger      *zap.Logger

	listener net.Listener
	wg       sync.WaitGroup
	mu       sync.Mutex
	running  bool
}

// NewMTProtoProxy creates a new MTProto proxy server
func NewMTProtoProxy(
	cfg *config.Config,
	hc *healthcheck.Checker,
	m *metrics.SafeCollector,
	logger *zap.Logger,
) *MTProtoProxy {
	return &MTProtoProxy{
		config:      cfg,
		healthCheck: hc,
		metrics:     m,
		logger:      logger,
	}
}

// Start starts the MTProto proxy server
func (p *MTProtoProxy) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return fmt.Errorf("MTProto proxy already running")
	}
	p.running = true
	p.mu.Unlock()

	addr := fmt.Sprintf(":%d", p.config.Proxy.MTProtoPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to start MTProto listener: %w", err)
	}

	p.listener = listener
	p.logger.Info("MTProto proxy started", zap.Int("port", p.config.Proxy.MTProtoPort))

	go func() {
		<-ctx.Done()
		p.Stop()
	}()

	p.acceptLoop()
	return nil
}

// Stop stops the MTProto proxy server
func (p *MTProtoProxy) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.running {
		return
	}

	p.logger.Info("Stopping MTProto proxy")
	if p.listener != nil {
		p.listener.Close()
	}
	p.running = false
	p.wg.Wait()
}

func (p *MTProtoProxy) acceptLoop() {
	for {
		p.mu.Lock()
		running := p.running
		p.mu.Unlock()

		if !running {
			return
		}

		conn, err := p.listener.Accept()
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			// Check for syscall errors that should be ignored
			var sysErr syscall.Errno
			if errors.As(err, &sysErr) {
				if sysErr == syscall.EINTR || sysErr == syscall.ECONNABORTED {
					continue
				}
			}
			p.logger.Debug("MTProto accept error", zap.Error(err))
			return
		}

		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.handleConnection(conn)
		}()
	}
}

func (p *MTProtoProxy) handleConnection(clientConn net.Conn) {
	defer clientConn.Close()

	p.logger.Debug("New MTProto connection", zap.String("remote", clientConn.RemoteAddr().String()))
	p.metrics.IncTotalConnections()
	p.metrics.IncActiveConnections()
	defer p.metrics.DecActiveConnections()

	startTime := time.Now()

	// Read client tag (4 bytes)
	tagBuf := make([]byte, 4)
	if _, err := io.ReadFull(clientConn, tagBuf); err != nil {
		p.logger.Debug("Failed to read MTProto tag", zap.Error(err))
		return
	}

	tag := binary.LittleEndian.Uint32(tagBuf)
	p.logger.Debug("MTProto tag received", zap.Uint32("tag", tag))

	// Get best MTProto upstream
	upstream := p.healthCheck.GetBestUpstream(config.UpstreamTypeMTProto)
	if upstream == nil {
		p.logger.Warn("No healthy MTProto upstream available")
		return
	}

	p.metrics.IncUpstreamRequests(upstream.Name, string(upstream.Type))

	// Connect to upstream with replay of the tag
	upstreamConn, err := p.connectToUpstream(upstream, tagBuf)
	if err != nil {
		p.logger.Debug("Failed to connect to MTProto upstream", zap.Error(err))
		p.metrics.IncUpstreamFailures(upstream.Name, string(upstream.Type))
		return
	}
	defer upstreamConn.Close()

	// Relay traffic
	p.relayTraffic(clientConn, upstreamConn, upstream, startTime)
}

func (p *MTProtoProxy) connectToUpstream(upstream *config.Upstream, clientTag []byte) (net.Conn, error) {
	upstreamAddr := fmt.Sprintf("%s:%d", upstream.Host, upstream.Port)
	dialer := &net.Dialer{Timeout: 5 * time.Second}

	conn, err := dialer.Dial("tcp", upstreamAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to dial upstream: %w", err)
	}

	// Replay client tag to upstream
	if _, err := conn.Write(clientTag); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to write tag: %w", err)
	}

	// If secret is provided, perform MTProto handshake
	if upstream.Secret != "" {
		if err := p.mtprotoHandshake(conn, upstream.Secret); err != nil {
			conn.Close()
			return nil, fmt.Errorf("MTProto handshake failed: %w", err)
		}
	}

	return conn, nil
}

func (p *MTProtoProxy) mtprotoHandshake(conn net.Conn, secret string) error {
	// MTProto simplified handshake
	// In production, implement full MTProto 2.0 protocol with proper encryption

	// Generate random nonce
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}

	// Create handshake packet
	packet := make([]byte, 0, 4+16)
	packet = append(packet, 0x00, 0x00, 0x00, 0x00) // Placeholder for length
	packet = append(packet, nonce...)

	// Write packet
	if _, err := conn.Write(packet); err != nil {
		return err
	}

	// Read server response
	resp := make([]byte, 4)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return err
	}

	// Check response tag
	respTag := binary.LittleEndian.Uint32(resp)
	if respTag != 0x00000000 && respTag != 0xffffffff {
		// Read remaining response
		buf := make([]byte, 60)
		io.ReadFull(conn, buf)
	}

	return nil
}

func (p *MTProtoProxy) relayTraffic(clientConn, upstreamConn net.Conn, upstream *config.Upstream, startTime time.Time) {
	var wg sync.WaitGroup
	bytesTransferred := int64(0)
	done := make(chan struct{}, 2)

	// Client -> Upstream
	wg.Add(1)
	go func() {
		defer wg.Done()
		n, _ := io.Copy(upstreamConn, clientConn)
		clientConn.SetReadDeadline(time.Now())
		upstreamConn.SetWriteDeadline(time.Now())
		bytesTransferred += n
		done <- struct{}{}
	}()

	// Upstream -> Client
	wg.Add(1)
	go func() {
		defer wg.Done()
		n, _ := io.Copy(clientConn, upstreamConn)
		clientConn.SetWriteDeadline(time.Now())
		upstreamConn.SetReadDeadline(time.Now())
		bytesTransferred += n
		done <- struct{}{}
	}()

	// Wait for either direction to complete
	<-done

	// Give the other direction a moment to finish
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
	}

	// Close connections
	clientConn.Close()
	upstreamConn.Close()

	// Wait for goroutines
	wg.Wait()

	duration := time.Since(startTime)
	p.metrics.AddBytesTransferred(bytesTransferred)
	p.metrics.ObserveConnectionDuration(duration)

	p.logger.Debug("MTProto connection closed",
		zap.String("upstream", upstream.Name),
		zap.Int64("bytes", bytesTransferred),
		zap.Duration("duration", duration))
}

// MTProtoCipher handles MTProto encryption/decryption
type MTProtoCipher struct {
	encrypt cipher.Block
	decrypt cipher.Block
	ivEnc   []byte
	ivDec   []byte
}

// NewMTProtoCipher creates a new MTProto cipher from secret
// Note: This is a simplified implementation. For production use,
// implement full MTProto 2.0 protocol with proper key derivation.
func NewMTProtoCipher(secret string) (*MTProtoCipher, error) {
	// Hash secret to get key material
	hash := sha256.Sum256([]byte(secret))

	// Use first 16 bytes for AES key (AES-128)
	key := hash[:16]

	// Generate IVs from remaining hash bytes
	ivEnc := make([]byte, 16)
	ivDec := make([]byte, 16)
	copy(ivEnc, hash[0:16])
	copy(ivDec, hash[16:32])

	encrypt, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	decrypt, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	return &MTProtoCipher{
		encrypt: encrypt,
		decrypt: decrypt,
		ivEnc:   ivEnc,
		ivDec:   ivDec,
	}, nil
}

// Encrypt encrypts data using CFB mode
func (c *MTProtoCipher) Encrypt(dst, src []byte) {
	stream := cipher.NewCFBEncrypter(c.encrypt, c.ivEnc)
	stream.XORKeyStream(dst, src)
}

// Decrypt decrypts data using CFB mode
func (c *MTProtoCipher) Decrypt(dst, src []byte) {
	stream := cipher.NewCFBDecrypter(c.decrypt, c.ivDec)
	stream.XORKeyStream(dst, src)
}
