package metrics

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	dto "github.com/prometheus/client_model/go"
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

	// Собираем метрики через Prometheus Gatherer
	metricFamilies, err := c.registry.Gather()
	if err != nil {
		metrics["active_connections"] = "N/A"
		metrics["total_connections"] = "N/A"
		metrics["bytes_transferred"] = "N/A"
		metrics["note"] = "Error gathering metrics: " + err.Error()
		return metrics
	}

	// Извлекаем значения
	metrics["active_connections"] = c.getGaugeValue(metricFamilies, "proxy_active_connections")
	metrics["total_connections"] = c.getCounterValue(metricFamilies, "proxy_total_connections")
	metrics["bytes_transferred"] = c.formatBytes(c.getCounterValue(metricFamilies, "proxy_bytes_transferred_total"))

	// Считаем количество upstream'ов
	upstreamCount := 0
	for _, mf := range metricFamilies {
		if *mf.Name == "upstream_health_status" {
			upstreamCount = len(mf.Metric)
			break
		}
	}

	metrics["note"] = fmt.Sprintf("📈 Tracking %d upstream(s) | Query /metrics for detailed data", upstreamCount)

	return metrics
}

// getGaugeValue извлекает значение gauge метрики
func (c *Collector) getGaugeValue(metricFamilies []*dto.MetricFamily, name string) string {
	for _, mf := range metricFamilies {
		if *mf.Name == name && len(mf.Metric) > 0 {
			if mf.Metric[0].Gauge != nil {
				return fmt.Sprintf("%.0f", *mf.Metric[0].Gauge.Value)
			}
		}
	}
	return "0"
}

// getCounterValue извлекает значение counter метрики
func (c *Collector) getCounterValue(metricFamilies []*dto.MetricFamily, name string) string {
	for _, mf := range metricFamilies {
		if *mf.Name == name && len(mf.Metric) > 0 {
			if mf.Metric[0].Counter != nil {
				return fmt.Sprintf("%.0f", *mf.Metric[0].Counter.Value)
			}
		}
	}
	return "0"
}

// formatBytes форматирует размер в человекочитаемый вид
func (c *Collector) formatBytes(value string) string {
	bytes, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return value
	}

	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%.0f B", bytes)
	}
	div, exp := float64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	return fmt.Sprintf("%.2f %s", bytes/div, units[exp])
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
