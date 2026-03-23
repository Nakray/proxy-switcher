package metrics

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

// Collector holds all Prometheus metrics
type Collector struct {
	registry *prometheus.Registry

	// Connection metrics
	activeConnections   prometheus.Gauge
	totalConnections    prometheus.Counter
	connectionDuration  prometheus.Histogram
	bytesTransferred    prometheus.Counter

	// Upstream metrics
	upstreamLatency      *prometheus.GaugeVec
	upstreamHealth       *prometheus.GaugeVec
	upstreamRequests     *prometheus.CounterVec
	upstreamFailures     *prometheus.CounterVec
	upstreamReconnects   *prometheus.CounterVec

	// Health check metrics
	healthCheckDuration prometheus.Histogram
	healthCheckErrors   prometheus.Counter

	// Bot metrics
	botMessagesSent prometheus.Counter
	botCommands     *prometheus.CounterVec

	logger *zap.Logger
	server *http.Server
}

// NewCollector creates a new metrics collector
func NewCollector(logger *zap.Logger, upstreamNames []string) *Collector {
	registry := prometheus.NewRegistry()

	c := &Collector{
		registry: registry,
		logger:   logger,
	}

	// Initialize connection metrics
	c.activeConnections = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxy_active_connections",
		Help: "Number of currently active proxy connections",
	})

	c.totalConnections = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxy_total_connections",
		Help: "Total number of proxy connections",
	})

	c.connectionDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "proxy_connection_duration_seconds",
		Help:    "Duration of proxy connections in seconds",
		Buckets: prometheus.ExponentialBuckets(0.1, 2, 15),
	})

	c.bytesTransferred = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxy_bytes_transferred_total",
		Help: "Total bytes transferred through the proxy",
	})

	// Initialize upstream metrics with labels
	c.upstreamLatency = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "upstream_latency_milliseconds",
		Help: "Latency to upstream proxies in milliseconds",
	}, []string{"upstream", "type"})

	c.upstreamHealth = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "upstream_health_status",
		Help: "Health status of upstream proxies (1=healthy, 0=unhealthy)",
	}, []string{"upstream", "type"})

	c.upstreamRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "upstream_requests_total",
		Help: "Total requests forwarded to upstream",
	}, []string{"upstream", "type"})

	c.upstreamFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "upstream_failures_total",
		Help: "Total upstream connection failures",
	}, []string{"upstream", "type"})

	c.upstreamReconnects = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "upstream_reconnects_total",
		Help: "Total upstream reconnection attempts",
	}, []string{"upstream", "type"})

	// Initialize health check metrics
	c.healthCheckDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "health_check_duration_seconds",
		Help:    "Duration of health checks in seconds",
		Buckets: prometheus.ExponentialBuckets(0.01, 2, 10),
	})

	c.healthCheckErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "health_check_errors_total",
		Help: "Total health check errors",
	})

	// Initialize bot metrics
	c.botMessagesSent = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "bot_messages_sent_total",
		Help: "Total messages sent by the bot",
	})

	c.botCommands = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "bot_commands_total",
		Help: "Total bot commands received",
	}, []string{"command"})

	// Register all metrics
	c.registry.MustRegister(
		c.activeConnections,
		c.totalConnections,
		c.connectionDuration,
		c.bytesTransferred,
		c.upstreamLatency,
		c.upstreamHealth,
		c.upstreamRequests,
		c.upstreamFailures,
		c.upstreamReconnects,
		c.healthCheckDuration,
		c.healthCheckErrors,
		c.botMessagesSent,
		c.botCommands,
	)

	// Initialize upstream health status to 0 (unknown)
	for _, name := range upstreamNames {
		c.upstreamHealth.WithLabelValues(name, "socks5").Set(-1)
		c.upstreamHealth.WithLabelValues(name, "mtproto").Set(-1)
	}

	return c
}

// StartServer starts the HTTP server for metrics exposure
func (c *Collector) StartServer(ctx context.Context, port int, path string) error {
	mux := http.NewServeMux()
	mux.Handle(path, promhttp.HandlerFor(c.registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	}))

	// Add health endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	c.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		c.logger.Info("Starting metrics server",
			zap.Int("port", port),
			zap.String("path", path))
		if err := c.server.ListenAndServe(); err != http.ErrServerClosed {
			c.logger.Error("Metrics server failed", zap.Error(err))
		}
	}()

	return nil
}

// StopServer gracefully stops the metrics server
func (c *Collector) StopServer(ctx context.Context) error {
	if c.server == nil {
		return nil
	}
	return c.server.Shutdown(ctx)
}

// Connection metrics
func (c *Collector) IncActiveConnections() {
	c.activeConnections.Inc()
}

func (c *Collector) DecActiveConnections() {
	c.activeConnections.Dec()
}

func (c *Collector) IncTotalConnections() {
	c.totalConnections.Inc()
}

func (c *Collector) ObserveConnectionDuration(duration time.Duration) {
	c.connectionDuration.Observe(duration.Seconds())
}

func (c *Collector) AddBytesTransferred(bytes int64) {
	c.bytesTransferred.Add(float64(bytes))
}

// Upstream metrics
func (c *Collector) SetUpstreamLatency(name, upstreamType string, latency time.Duration) {
	c.upstreamLatency.WithLabelValues(name, upstreamType).Set(float64(latency.Milliseconds()))
}

func (c *Collector) SetUpstreamHealth(name, upstreamType string, healthy bool) {
	value := 0.0
	if healthy {
		value = 1.0
	}
	c.upstreamHealth.WithLabelValues(name, upstreamType).Set(value)
}

func (c *Collector) IncUpstreamRequests(name, upstreamType string) {
	c.upstreamRequests.WithLabelValues(name, upstreamType).Inc()
}

func (c *Collector) IncUpstreamFailures(name, upstreamType string) {
	c.upstreamFailures.WithLabelValues(name, upstreamType).Inc()
}

func (c *Collector) IncUpstreamReconnects(name, upstreamType string) {
	c.upstreamReconnects.WithLabelValues(name, upstreamType).Inc()
}

// Health check metrics
func (c *Collector) ObserveHealthCheckDuration(duration time.Duration) {
	c.healthCheckDuration.Observe(duration.Seconds())
}

func (c *Collector) IncHealthCheckErrors() {
	c.healthCheckErrors.Inc()
}

// Bot metrics
func (c *Collector) IncBotMessagesSent() {
	c.botMessagesSent.Inc()
}

func (c *Collector) IncBotCommand(command string) {
	c.botCommands.WithLabelValues(command).Inc()
}

// GetSummary returns a summary of current metrics
func (c *Collector) GetSummary() map[string]interface{} {
	metrics := make(map[string]interface{})

	// Note: Prometheus Go client doesn't expose Get() method directly
	// In production, you would use prometheus.Gatherer to collect metrics
	// For now, we return a basic summary
	
	metrics["active_connections"] = "see_prometheus"
	metrics["total_connections"] = "see_prometheus"
	metrics["bytes_transferred"] = "see_prometheus"
	metrics["upstreams"] = "see_prometheus"
	metrics["note"] = "Query /metrics endpoint for detailed data"

	return metrics
}

// SafeCollector wraps Collector with mutex for thread-safe operations
type SafeCollector struct {
	mu       sync.RWMutex
	collector *Collector
}

// NewSafeCollector creates a new thread-safe metrics collector
func NewSafeCollector(logger *zap.Logger, upstreamNames []string) *SafeCollector {
	return &SafeCollector{
		collector: NewCollector(logger, upstreamNames),
	}
}

func (s *SafeCollector) Collector() *Collector {
	return s.collector
}

func (s *SafeCollector) IncActiveConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collector.IncActiveConnections()
}

func (s *SafeCollector) DecActiveConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collector.DecActiveConnections()
}

func (s *SafeCollector) IncTotalConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collector.IncTotalConnections()
}

func (s *SafeCollector) ObserveConnectionDuration(duration time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collector.ObserveConnectionDuration(duration)
}

func (s *SafeCollector) AddBytesTransferred(bytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collector.AddBytesTransferred(bytes)
}

func (s *SafeCollector) SetUpstreamLatency(name, upstreamType string, latency time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collector.SetUpstreamLatency(name, upstreamType, latency)
}

func (s *SafeCollector) SetUpstreamHealth(name, upstreamType string, healthy bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collector.SetUpstreamHealth(name, upstreamType, healthy)
}

func (s *SafeCollector) IncUpstreamRequests(name, upstreamType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collector.IncUpstreamRequests(name, upstreamType)
}

func (s *SafeCollector) IncUpstreamFailures(name, upstreamType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collector.IncUpstreamFailures(name, upstreamType)
}

func (s *SafeCollector) IncUpstreamReconnects(name, upstreamType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collector.IncUpstreamReconnects(name, upstreamType)
}

func (s *SafeCollector) ObserveHealthCheckDuration(duration time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collector.ObserveHealthCheckDuration(duration)
}

func (s *SafeCollector) IncHealthCheckErrors() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collector.IncHealthCheckErrors()
}

func (s *SafeCollector) IncBotMessagesSent() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collector.IncBotMessagesSent()
}

func (s *SafeCollector) IncBotCommand(command string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collector.IncBotCommand(command)
}

func (s *SafeCollector) GetSummary() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.collector.GetSummary()
}

func (s *SafeCollector) StartServer(ctx context.Context, port int, path string) error {
	return s.collector.StartServer(ctx, port, path)
}

func (s *SafeCollector) StopServer(ctx context.Context) error {
	return s.collector.StopServer(ctx)
}
