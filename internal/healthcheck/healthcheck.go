package healthcheck

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/Nakray/proxy-switcher/internal/config"
	"github.com/Nakray/proxy-switcher/internal/database"
	"github.com/Nakray/proxy-switcher/internal/metrics"
)

// UpstreamStatus represents the health status of an upstream
type UpstreamStatus struct {
	Upstream    config.Upstream
	Healthy     bool
	Latency     time.Duration
	LastCheck   time.Time
	Consecutive int // consecutive failures
}

// Checker performs health checks on upstream proxies
type Checker struct {
	config     *config.Config
	db         *database.UpstreamRepository
	metrics    *metrics.SafeCollector
	logger     *zap.Logger

	mu       sync.RWMutex
	statuses map[string]*UpstreamStatus

	ctx    context.Context
	cancel context.CancelFunc
}

// NewChecker creates a new health checker
func NewChecker(cfg *config.Config, db *database.UpstreamRepository, m *metrics.SafeCollector, logger *zap.Logger) *Checker {
	ctx, cancel := context.WithCancel(context.Background())

	checker := &Checker{
		config:   cfg,
		db:       db,
		metrics:  m,
		logger:   logger,
		statuses: make(map[string]*UpstreamStatus),
		ctx:      ctx,
		cancel:   cancel,
	}

	return checker
}

// LoadUpstreams loads upstreams from database and initializes statuses
func (h *Checker) LoadUpstreams() error {
	upstreams, err := h.db.List()
	if err != nil {
		return fmt.Errorf("failed to load upstreams from database: %w", err)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	for _, upstream := range upstreams {
		key := h.upstreamKey(upstream)
		h.statuses[key] = &UpstreamStatus{
			Upstream:  upstream,
			Healthy:   false,
			LastCheck: time.Time{},
		}
	}

	h.logger.Info("Upstreams loaded from database", zap.Int("count", len(upstreams)))
	return nil
}

// Start starts the health check loop
func (h *Checker) Start() {
	h.logger.Info("Starting health checker",
		zap.Duration("interval", h.config.HealthCheck.Interval),
		zap.Int("upstreams", len(h.config.Upstreams)))

	go h.runHealthCheckLoop()
}

// Stop stops the health check loop
func (h *Checker) Stop() {
	h.logger.Info("Stopping health checker")
	h.cancel()
}

func (h *Checker) runHealthCheckLoop() {
	ticker := time.NewTicker(h.config.HealthCheck.Interval)
	defer ticker.Stop()

	// Run initial check
	h.checkAllUpstreams()

	for {
		select {
		case <-h.ctx.Done():
			return
		case <-ticker.C:
			h.checkAllUpstreams()
		}
	}
}

func (h *Checker) checkAllUpstreams() {
	h.logger.Debug("Running health checks on all upstreams")

	var wg sync.WaitGroup
	h.mu.RLock()
	upstreamsToCheck := make([]config.Upstream, 0, len(h.statuses))
	for _, status := range h.statuses {
		if status.Upstream.Enabled {
			upstreamsToCheck = append(upstreamsToCheck, status.Upstream)
		}
	}
	h.mu.RUnlock()

	for _, upstream := range upstreamsToCheck {
		wg.Add(1)
		go func(u config.Upstream) {
			defer wg.Done()
			h.checkUpstream(u)
		}(upstream)
	}
	wg.Wait()

	// Log summary
	healthyCount := 0
	h.mu.RLock()
	for _, status := range h.statuses {
		if status.Healthy {
			healthyCount++
		}
	}
	h.mu.RUnlock()

	h.logger.Info("Health check completed",
		zap.Int("healthy", healthyCount),
		zap.Int("total", len(h.statuses)))
}

func (h *Checker) checkUpstream(upstream config.Upstream) {
	key := h.upstreamKey(upstream)
	startTime := time.Now()

	healthy, latency := h.probeUpstream(upstream)
	duration := time.Since(startTime)

	h.mu.Lock()
	status := h.statuses[key]
	status.LastCheck = startTime
	status.Latency = latency

	if healthy {
		status.Consecutive = 0
		if !status.Healthy {
			h.logger.Info("Upstream recovered",
				zap.String("name", upstream.Name),
				zap.String("type", string(upstream.Type)),
				zap.Duration("latency", latency))
		}
		status.Healthy = true
	} else {
		status.Consecutive++
		if status.Healthy {
			h.logger.Warn("Upstream failed health check",
				zap.String("name", upstream.Name),
				zap.String("type", string(upstream.Type)),
				zap.Int("consecutive_failures", status.Consecutive))
		}
		status.Healthy = status.Consecutive < h.config.HealthCheck.UnhealthyThreshold
	}
	h.mu.Unlock()

	// Update metrics
	h.metrics.SetUpstreamHealth(upstream.Name, string(upstream.Type), status.Healthy)
	if healthy && latency > 0 {
		h.metrics.SetUpstreamLatency(upstream.Name, string(upstream.Type), latency)
	}
	h.metrics.ObserveHealthCheckDuration(duration)
}

func (h *Checker) probeUpstream(upstream config.Upstream) (bool, time.Duration) {
	ctx, cancel := context.WithTimeout(h.ctx, h.config.HealthCheck.Timeout)
	defer cancel()

	switch upstream.Type {
	case config.UpstreamTypeSOCKS5:
		return h.probeSOCKS5(ctx, upstream)
	case config.UpstreamTypeMTProto:
		return h.probeMTProto(ctx, upstream)
	default:
		h.logger.Error("Unknown upstream type",
			zap.String("name", upstream.Name),
			zap.String("type", string(upstream.Type)))
		return false, 0
	}
}

func (h *Checker) probeSOCKS5(ctx context.Context, upstream config.Upstream) (bool, time.Duration) {
	startTime := time.Now()
	addr := fmt.Sprintf("%s:%d", upstream.Host, upstream.Port)

	dialer := &net.Dialer{Timeout: h.config.HealthCheck.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		h.logger.Debug("SOCKS5 probe failed",
			zap.String("upstream", upstream.Name),
			zap.Error(err))
		return false, 0
	}
	defer conn.Close()

	latency := time.Since(startTime)
	_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))

	// Try to read any response or just close
	buf := make([]byte, 1)
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, err = conn.Read(buf)
	// Ignore timeout error - connection established is enough
	if err != nil && err != context.DeadlineExceeded {
		if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
			return false, 0
		}
	}

	return true, latency
}

func (h *Checker) probeMTProto(ctx context.Context, upstream config.Upstream) (bool, time.Duration) {
	startTime := time.Now()
	addr := fmt.Sprintf("%s:%d", upstream.Host, upstream.Port)

	dialer := &net.Dialer{Timeout: h.config.HealthCheck.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		h.logger.Debug("MTProto probe failed",
			zap.String("upstream", upstream.Name),
			zap.Error(err))
		return false, 0
	}
	defer conn.Close()

	// MTProto basic connectivity check
	// We just verify TCP connection is possible
	// Full MTProto handshake would require the secret
	latency := time.Since(startTime)

	return true, latency
}

func (h *Checker) upstreamKey(upstream config.Upstream) string {
	return fmt.Sprintf("%s_%s", upstream.Name, upstream.Type)
}

// GetHealthyUpstreams returns a list of healthy upstreams
func (h *Checker) GetHealthyUpstreams() []config.Upstream {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var healthy []config.Upstream
	for _, status := range h.statuses {
		if status.Healthy {
			healthy = append(healthy, status.Upstream)
		}
	}
	return healthy
}

// GetBestUpstream returns the healthy upstream with lowest latency
func (h *Checker) GetBestUpstream(upstreamType config.UpstreamType) *config.Upstream {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var best *UpstreamStatus
	for _, status := range h.statuses {
		if !status.Healthy {
			continue
		}
		if !status.Upstream.Enabled {
			continue
		}
		if upstreamType != "" && status.Upstream.Type != upstreamType {
			continue
		}
		if best == nil || status.Latency < best.Latency {
			best = status
		}
	}

	if best == nil {
		return nil
	}
	return &best.Upstream
}

// GetUpstreamStatus returns the status of a specific upstream
func (h *Checker) GetUpstreamStatus(name string) *UpstreamStatus {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for key, status := range h.statuses {
		if status.Upstream.Name == name {
			_ = key
			return status
		}
	}
	return nil
}

// GetAllStatuses returns all upstream statuses
func (h *Checker) GetAllStatuses() map[string]*UpstreamStatus {
	h.mu.RLock()
	defer h.mu.RUnlock()

	result := make(map[string]*UpstreamStatus)
	for k, v := range h.statuses {
		result[k] = v
	}
	return result
}

// AreAllUpstreamsDown returns true if all upstreams are unhealthy
func (h *Checker) AreAllUpstreamsDown() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, status := range h.statuses {
		if status.Healthy {
			return false
		}
	}
	return true
}

// GetHealthyCount returns the number of healthy upstreams
func (h *Checker) GetHealthyCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	count := 0
	for _, status := range h.statuses {
		if status.Healthy {
			count++
		}
	}
	return count
}

// AddUpstream adds a new upstream to the checker
func (h *Checker) AddUpstream(upstream config.Upstream) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	key := h.upstreamKey(upstream)
	if _, exists := h.statuses[key]; exists {
		return fmt.Errorf("upstream %s already exists", upstream.Name)
	}

	// Save to database
	if err := h.db.Create(upstream); err != nil {
		return err
	}

	h.statuses[key] = &UpstreamStatus{
		Upstream:  upstream,
		Healthy:   false,
		LastCheck: time.Time{},
	}

	// Update metrics
	h.metrics.SetUpstreamHealth(upstream.Name, string(upstream.Type), false)

	h.logger.Info("Upstream added",
		zap.String("name", upstream.Name),
		zap.String("type", string(upstream.Type)),
		zap.String("host", upstream.Host),
		zap.Int("port", upstream.Port))

	return nil
}

// RemoveUpstream removes an upstream from the checker
func (h *Checker) RemoveUpstream(name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	var found bool
	var upstreamType string
	for key, status := range h.statuses {
		if status.Upstream.Name == name {
			found = true
			upstreamType = string(status.Upstream.Type)
			delete(h.statuses, key)
			break
		}
	}

	if !found {
		return fmt.Errorf("upstream %s not found", name)
	}

	// Delete from database
	if err := h.db.Delete(name); err != nil {
		return err
	}

	h.logger.Info("Upstream removed",
		zap.String("name", name),
		zap.String("type", upstreamType))

	return nil
}

// EnableUpstream enables an upstream
func (h *Checker) EnableUpstream(name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, status := range h.statuses {
		if status.Upstream.Name == name {
			status.Upstream.Enabled = true
			// Update database
			if err := h.db.SetEnabled(name, true); err != nil {
				h.logger.Warn("Failed to update database", zap.Error(err))
			}
			h.logger.Info("Upstream enabled", zap.String("name", name))
			return nil
		}
	}

	return fmt.Errorf("upstream %s not found", name)
}

// DisableUpstream disables an upstream
func (h *Checker) DisableUpstream(name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, status := range h.statuses {
		if status.Upstream.Name == name {
			status.Upstream.Enabled = false
			// Update database
			if err := h.db.SetEnabled(name, false); err != nil {
				h.logger.Warn("Failed to update database", zap.Error(err))
			}
			h.logger.Info("Upstream disabled", zap.String("name", name))
			return nil
		}
	}

	return fmt.Errorf("upstream %s not found", name)
}

// GetUpstreamNames returns a list of all upstream names
func (h *Checker) GetUpstreamNames() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	names := make([]string, 0, len(h.statuses))
	for _, status := range h.statuses {
		names = append(names, status.Upstream.Name)
	}
	return names
}

// GetUpstreamByName returns an upstream by name
func (h *Checker) GetUpstreamByName(name string) *config.Upstream {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, status := range h.statuses {
		if status.Upstream.Name == name {
			upstream := status.Upstream
			return &upstream
		}
	}
	return nil
}
