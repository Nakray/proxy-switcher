package router

import (
	"context"
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

// Connection represents an active proxy connection
type Connection struct {
	ID           string
	ClientConn   net.Conn
	UpstreamConn net.Conn
	Upstream     *config.Upstream
	StartTime    time.Time
	BytesSent    int64
	BytesRecv    int64
	mu           sync.RWMutex
}

// Router manages traffic routing between clients and upstreams
type Router struct {
	config      *config.Config
	healthCheck *healthcheck.Checker
	metrics     *metrics.SafeCollector
	logger      *zap.Logger

	mu           sync.RWMutex
	connections  map[string]*Connection
	connectionID uint64

	reconnectAttempts map[string]int
	maxReconnects     int
	reconnectDelay    time.Duration
}

// NewRouter creates a new traffic router
func NewRouter(
	cfg *config.Config,
	hc *healthcheck.Checker,
	m *metrics.SafeCollector,
	logger *zap.Logger,
) *Router {
	return &Router{
		config:            cfg,
		healthCheck:       hc,
		metrics:           m,
		logger:            logger,
		connections:       make(map[string]*Connection),
		reconnectAttempts: make(map[string]int),
		maxReconnects:     3,
		reconnectDelay:    1 * time.Second,
	}
}

// Route establishes a connection through the best available upstream
func (r *Router) Route(ctx context.Context, clientConn net.Conn, upstreamType config.UpstreamType) error {
	connID := r.generateConnectionID()
	logger := r.logger.With(zap.String("conn_id", connID))

	logger.Debug("New routing request", zap.String("type", string(upstreamType)))

	// Get best upstream
	upstream := r.healthCheck.GetBestUpstream(upstreamType)
	if upstream == nil {
		logger.Warn("No healthy upstream available")
		return fmt.Errorf("no healthy upstream")
	}

	// Create connection record
	conn := &Connection{
		ID:         connID,
		ClientConn: clientConn,
		Upstream:   upstream,
		StartTime:  time.Now(),
	}

	r.mu.Lock()
	r.connections[connID] = conn
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		delete(r.connections, connID)
		delete(r.reconnectAttempts, connID)
		r.mu.Unlock()
	}()

	// Connect to upstream
	if err := r.connectToUpstream(conn, logger); err != nil {
		r.metrics.IncUpstreamFailures(upstream.Name, string(upstream.Type))
		return err
	}

	// Start traffic relay with reconnection support
	r.relayWithReconnect(ctx, conn, logger)

	return nil
}

func (r *Router) connectToUpstream(conn *Connection, logger *zap.Logger) error {
	upstream := conn.Upstream
	upstreamAddr := fmt.Sprintf("%s:%d", upstream.Host, upstream.Port)

	dialer := &net.Dialer{Timeout: 5 * time.Second}
	upstreamConn, err := dialer.Dial("tcp", upstreamAddr)
	if err != nil {
		logger.Debug("Failed to connect to upstream",
			zap.String("upstream", upstream.Name),
			zap.Error(err))
		return err
	}

	// Perform protocol-specific handshake
	switch upstream.Type {
	case config.UpstreamTypeSOCKS5:
		if err := r.socks5Handshake(upstreamConn, upstream); err != nil {
			upstreamConn.Close()
			return err
		}
	case config.UpstreamTypeMTProto:
		// MTProto handshake is handled by the proxy layer
	}

	conn.UpstreamConn = upstreamConn
	logger.Debug("Connected to upstream", zap.String("upstream", upstream.Name))

	return nil
}

func (r *Router) socks5Handshake(conn net.Conn, upstream *config.Upstream) error {
	// SOCKS5 version
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return err
	}

	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return err
	}

	if resp[0] != 0x05 || resp[1] != 0x00 {
		return fmt.Errorf("SOCKS5 handshake failed")
	}

	return nil
}

func (r *Router) relayWithReconnect(ctx context.Context, conn *Connection, logger *zap.Logger) {
	reconnectCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	errChan := make(chan error, 2)

	// Start bidirectional relay
	wg.Add(2)

	// Client -> Upstream
	go func() {
		defer wg.Done()
		n, err := io.Copy(conn.UpstreamConn, conn.ClientConn)
		conn.mu.Lock()
		conn.BytesSent += n
		conn.mu.Unlock()
		if err != nil {
			errChan <- err
		}
	}()

	// Upstream -> Client
	go func() {
		defer wg.Done()
		n, err := io.Copy(conn.ClientConn, conn.UpstreamConn)
		conn.mu.Lock()
		conn.BytesRecv += n
		conn.mu.Unlock()
		if err != nil {
			errChan <- err
		}
	}()

	// Wait for error or completion
	select {
	case err := <-errChan:
		logger.Debug("Connection error", zap.Error(err))
		r.attemptReconnect(reconnectCtx, conn, logger)
	case <-ctx.Done():
		logger.Debug("Context cancelled")
	case <-reconnectCtx.Done():
		logger.Debug("Reconnect cancelled")
	}

	// Cleanup
	conn.ClientConn.Close()
	conn.UpstreamConn.Close()
	wg.Wait()

	// Record metrics
	duration := time.Since(conn.StartTime)
	r.metrics.ObserveConnectionDuration(duration)
	r.metrics.AddBytesTransferred(conn.BytesSent + conn.BytesRecv)
}

func (r *Router) attemptReconnect(ctx context.Context, conn *Connection, logger *zap.Logger) {
	r.mu.Lock()
	attempts := r.reconnectAttempts[conn.ID]
	r.mu.Unlock()

	if attempts >= r.maxReconnects {
		logger.Info("Max reconnection attempts reached", zap.Int("attempts", attempts))
		return
	}

	// Get a new upstream (could be different if health status changed)
	newUpstream := r.healthCheck.GetBestUpstream(conn.Upstream.Type)
	if newUpstream == nil {
		logger.Warn("No healthy upstream for reconnection")
		return
	}

	// Check if we need to switch upstreams
	needReconnect := newUpstream.Name != conn.Upstream.Name

	if !needReconnect {
		// Same upstream, just reconnect
		logger.Debug("Attempting reconnection to same upstream",
			zap.Int("attempt", attempts+1))
	} else {
		// Switch to new upstream
		logger.Info("Switching to new upstream",
			zap.String("old", conn.Upstream.Name),
			zap.String("new", newUpstream.Name))
		r.metrics.IncUpstreamReconnects(conn.Upstream.Name, string(conn.Upstream.Type))
	}

	// Delay before reconnect
	select {
	case <-time.After(r.reconnectDelay):
	case <-ctx.Done():
		return
	}

	// Close old upstream connection
	if conn.UpstreamConn != nil {
		conn.UpstreamConn.Close()
	}

	// Update upstream if needed
	if needReconnect {
		conn.Upstream = newUpstream
	}

	// Try to reconnect
	r.mu.Lock()
	r.reconnectAttempts[conn.ID] = attempts + 1
	r.mu.Unlock()

	if err := r.connectToUpstream(conn, logger); err != nil {
		logger.Debug("Reconnection failed", zap.Error(err))
		return
	}

	logger.Info("Reconnection successful",
		zap.String("upstream", conn.Upstream.Name),
		zap.Int("attempt", attempts+1))
}

func (r *Router) generateConnectionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.connectionID++
	return fmt.Sprintf("conn_%d", r.connectionID)
}

// GetActiveConnections returns the number of active connections
func (r *Router) GetActiveConnections() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.connections)
}

// GetConnectionStats returns statistics about active connections
func (r *Router) GetConnectionStats() map[string]interface{} {
	r.mu.RLock()
	defer r.mu.RUnlock()

	stats := make(map[string]interface{})
	stats["total"] = len(r.connections)

	var totalBytes int64
	var oldestConnection time.Time

	for _, conn := range r.connections {
		conn.mu.RLock()
		totalBytes += conn.BytesSent + conn.BytesRecv
		if oldestConnection.IsZero() || conn.StartTime.Before(oldestConnection) {
			oldestConnection = conn.StartTime
		}
		conn.mu.RUnlock()
	}

	stats["total_bytes"] = totalBytes
	if !oldestConnection.IsZero() {
		stats["oldest_connection_age"] = time.Since(oldestConnection).Seconds()
	}

	return stats
}

// GracefulShutdown gracefully closes all connections
func (r *Router) GracefulShutdown(timeout time.Duration) error {
	r.logger.Info("Starting graceful shutdown",
		zap.Int("active_connections", r.GetActiveConnections()))

	r.mu.RLock()
	connections := make([]*Connection, 0, len(r.connections))
	for _, conn := range r.connections {
		connections = append(connections, conn)
	}
	r.mu.RUnlock()

	// Set read deadline to force connections to close
	for _, conn := range connections {
		conn.ClientConn.SetReadDeadline(time.Now())
		conn.UpstreamConn.SetReadDeadline(time.Now())
	}

	// Wait for connections to close
	done := make(chan struct{})
	go func() {
		for {
			if r.GetActiveConnections() == 0 {
				close(done)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()

	select {
	case <-done:
		r.logger.Info("All connections closed gracefully")
		return nil
	case <-time.After(timeout):
		r.logger.Warn("Timeout waiting for connections to close")
		return fmt.Errorf("timeout waiting for connections to close")
	}
}
