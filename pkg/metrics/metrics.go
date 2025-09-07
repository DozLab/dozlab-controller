package metrics

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// METRICS: Following dozlab-api patterns for metrics collection
var (
	// Counter metrics for lab session operations
	labSessionsCreated = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "lab_sessions_created_total",
			Help: "Total number of lab sessions created",
		},
		[]string{"namespace", "user_id"},
	)

	labSessionsDeleted = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "lab_sessions_deleted_total", 
			Help: "Total number of lab sessions deleted",
		},
		[]string{"namespace", "user_id", "reason"},
	)

	labSessionErrors = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "lab_session_errors_total",
			Help: "Total number of lab session errors",
		},
		[]string{"namespace", "session_id", "error_type"},
	)

	// Gauge metrics for current state
	labSessionsActive = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "lab_sessions_active",
			Help: "Number of currently active lab sessions",
		},
		[]string{"namespace"},
	)

	// Histogram metrics for performance tracking
	reconcileDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "lab_session_reconcile_duration_seconds",
			Help: "Time taken to reconcile lab sessions",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"namespace", "result"},
	)

	podCreationDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "lab_session_pod_creation_duration_seconds",
			Help: "Time taken to create lab session pods",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"namespace"},
	)

	// Resource usage metrics
	resourceUsage = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "lab_session_resource_usage",
			Help: "Resource usage by lab sessions",
		},
		[]string{"namespace", "session_id", "resource_type"},
	)
)

// Collector provides methods for recording metrics (following dozlab-api patterns)
type Collector struct{}

// NewCollector creates a new metrics collector
func NewCollector() *Collector {
	return &Collector{}
}

// Init initializes the metrics collector (following dozlab-api initialization pattern)
func (c *Collector) Init() {
	// Register custom metrics with controller runtime
	metrics.Registry.MustRegister(
		labSessionsCreated,
		labSessionsDeleted,
		labSessionErrors,
		labSessionsActive,
		reconcileDuration,
		podCreationDuration,
		resourceUsage,
	)
}

// RecordLabSessionCreated records when a lab session is created
func (c *Collector) RecordLabSessionCreated(namespace, userID string) {
	labSessionsCreated.WithLabelValues(namespace, userID).Inc()
	labSessionsActive.WithLabelValues(namespace).Inc()
}

// RecordLabSessionDeleted records when a lab session is deleted
func (c *Collector) RecordLabSessionDeleted(namespace, userID, reason string) {
	labSessionsDeleted.WithLabelValues(namespace, userID, reason).Inc()
	labSessionsActive.WithLabelValues(namespace).Dec()
}

// RecordLabSessionError records when a lab session encounters an error
func (c *Collector) RecordLabSessionError(namespace, sessionID, errorType string) {
	labSessionErrors.WithLabelValues(namespace, sessionID, errorType).Inc()
}

// RecordReconcileDuration records how long a reconcile operation took
func (c *Collector) RecordReconcileDuration(namespace, result string, duration time.Duration) {
	reconcileDuration.WithLabelValues(namespace, result).Observe(duration.Seconds())
}

// RecordPodCreationDuration records how long pod creation took
func (c *Collector) RecordPodCreationDuration(namespace string, duration time.Duration) {
	podCreationDuration.WithLabelValues(namespace).Observe(duration.Seconds())
}

// RecordResourceUsage records resource usage for a session
func (c *Collector) RecordResourceUsage(namespace, sessionID, resourceType string, value float64) {
	resourceUsage.WithLabelValues(namespace, sessionID, resourceType).Set(value)
}

// Timer provides timing functionality for operations (following dozlab-api timer pattern)
type Timer struct {
	start     time.Time
	collector *Collector
}

// NewTimer creates a new timer
func (c *Collector) NewTimer() *Timer {
	return &Timer{
		start:     time.Now(),
		collector: c,
	}
}

// RecordReconcile records the duration of a reconcile operation
func (t *Timer) RecordReconcile(namespace, result string) {
	duration := time.Since(t.start)
	t.collector.RecordReconcileDuration(namespace, result, duration)
}

// RecordPodCreation records the duration of a pod creation operation
func (t *Timer) RecordPodCreation(namespace string) {
	duration := time.Since(t.start)
	t.collector.RecordPodCreationDuration(namespace, duration)
}

// GetMetrics returns current metric values for debugging (following dozlab-api debug pattern)
func (c *Collector) GetMetrics(ctx context.Context) (map[string]interface{}, error) {
	// This would typically gather current metric values
	// Implementation depends on your specific monitoring needs
	return map[string]interface{}{
		"collector_initialized": true,
		"timestamp":            time.Now().Unix(),
	}, nil
}