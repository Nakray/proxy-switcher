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

	Mu       sync.RWMutex
	Statuses map[string]*UpstreamStatus

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
		Statuses: make(map[string]*UpstreamStatus),
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

	h.Mu.Lock()
	defer h.Mu.Unlock()

	for _, upstream := range upstreams {
		key := h.upstreamKey(upstream)
		h.Statuses[key] = &UpstreamStatus{
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
	h.Mu.RLock()
	upstreamsToCheck := make([]config.Upstream, 0, len(h.Statuses))
	for _, status := range h.Statuses {
		if status.Upstream.Enabled {
			upstreamsToCheck = append(upstreamsToCheck, status.Upstream)
		}
	}
	h.Mu.RUnlock()

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
	h.Mu.RLock()
	for _, status := range h.Statuses {
		if status.Healthy {
			healthyCount++
		}
	}
	h.Mu.RUnlock()

	h.logger.Info("Health check completed",
		zap.Int("healthy", healthyCount),
		zap.Int("total", len(h.Statuses)))
}

func (h *Checker) checkUpstream(upstream config.Upstream) {
	key := h.upstreamKey(upstream)
	startTime := time.Now()

	healthy, latency := h.probeUpstream(upstream)
	duration := time.Since(startTime)

	h.Mu.Lock()
	status := h.Statuses[key]
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
	h.Mu.Unlock()

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

// ProbeSOCKS5 performs a SOCKS5 health check (exported for tests)
func (h *Checker) ProbeSOCKS5(ctx context.Context, upstream config.Upstream) (bool, time.Duration) {
	return h.probeSOCKS5(ctx, upstream)
}

// ProbeMTProto performs an MTProto health check (exported for tests)
func (h *Checker) ProbeMTProto(ctx context.Context, upstream config.Upstream) (bool, time.Duration) {
	return h.probeMTProto(ctx, upstream)
}

func (h *Checker) upstreamKey(upstream config.Upstream) string {
	return fmt.Sprintf("%s_%s", upstream.Name, upstream.Type)
}

// GetHealthyUpstreams returns a list of healthy upstreams
func (h *Checker) GetHealthyUpstreams() []config.Upstream {
	h.Mu.RLock()
	defer h.Mu.RUnlock()

	var healthy []config.Upstream
	for _, status := range h.Statuses {
		if status.Healthy {
			healthy = append(healthy, status.Upstream)
		}
	}
	return healthy
}

// GetBestUpstream returns the healthy upstream with lowest latency
func (h *Checker) GetBestUpstream(upstreamType config.UpstreamType) *config.Upstream {
	h.Mu.RLock()
	defer h.Mu.RUnlock()

	var best *UpstreamStatus
	for _, status := range h.Statuses {
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
	h.Mu.RLock()
	defer h.Mu.RUnlock()

	for key, status := range h.Statuses {
		if status.Upstream.Name == name {
			_ = key
			return status
		}
	}
	return nil
}

// GetAllStatuses returns all upstream statuses
func (h *Checker) GetAllStatuses() map[string]*UpstreamStatus {
	h.Mu.RLock()
	defer h.Mu.RUnlock()

	result := make(map[string]*UpstreamStatus)
	for k, v := range h.Statuses {
		result[k] = v
	}
	return result
}

// AreAllUpstreamsDown returns true if all upstreams are unhealthy
func (h *Checker) AreAllUpstreamsDown() bool {
	h.Mu.RLock()
	defer h.Mu.RUnlock()

	for _, status := range h.Statuses {
		if status.Healthy {
			return false
		}
	}
	return true
}

// GetHealthyCount returns the number of healthy upstreams
func (h *Checker) GetHealthyCount() int {
	h.Mu.RLock()
	defer h.Mu.RUnlock()

	count := 0
	for _, status := range h.Statuses {
		if status.Healthy {
			count++
		}
	}
	return count
}

// AddUpstream adds a new upstream to the checker
func (h *Checker) AddUpstream(upstream config.Upstream) error {
	h.Mu.Lock()
	defer h.Mu.Unlock()

	key := h.upstreamKey(upstream)
	if _, exists := h.Statuses[key]; exists {
		return fmt.Errorf("upstream %s already exists", upstream.Name)
	}

	// Save to database
	if err := h.db.Create(upstream); err != nil {
		return err
	}

	h.Statuses[key] = &UpstreamStatus{
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
	h.Mu.Lock()
	defer h.Mu.Unlock()

	var found bool
	var upstreamType string
	for key, status := range h.Statuses {
		if status.Upstream.Name == name {
			found = true
			upstreamType = string(status.Upstream.Type)
			delete(h.Statuses, key)
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
	h.Mu.Lock()
	defer h.Mu.Unlock()

	for _, status := range h.Statuses {
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
	h.Mu.Lock()
	defer h.Mu.Unlock()

	for _, status := range h.Statuses {
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
	h.Mu.RLock()
	defer h.Mu.RUnlock()

	names := make([]string, 0, len(h.Statuses))
	for _, status := range h.Statuses {
		names = append(names, status.Upstream.Name)
	}
	return names
}

// GetUpstreamByName returns an upstream by name
func (h *Checker) GetUpstreamByName(name string) *config.Upstream {
	h.Mu.RLock()
	defer h.Mu.RUnlock()

	for _, status := range h.Statuses {
		if status.Upstream.Name == name {
			upstream := status.Upstream
			return &upstream
		}
	}
	return nil
}
