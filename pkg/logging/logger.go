package logging

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel/trace"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// LogLevel represents the log level (following dozlab-api logging patterns)
type LogLevel int

const (
	DebugLevel LogLevel = iota
	InfoLevel
	WarnLevel
	ErrorLevel
)

func (l LogLevel) String() string {
	switch l {
	case DebugLevel:
		return "DEBUG"
	case InfoLevel:
		return "INFO"
	case WarnLevel:
		return "WARN"
	case ErrorLevel:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// Fields represents structured log fields (following dozlab-api field patterns)
type Fields map[string]interface{}

// Logger provides structured logging with context and tracing (following dozlab-api patterns)
type Logger struct {
	base logr.Logger
	name string
}

// NewLogger creates a new structured logger
func NewLogger(name string) *Logger {
	return &Logger{
		base: log.Log.WithName(name),
		name: name,
	}
}

// WithContext adds context information to the logger
func (l *Logger) WithContext(ctx context.Context) *Logger {
	logger := &Logger{
		base: l.base,
		name: l.name,
	}

	// Add trace information if available
	span := trace.SpanFromContext(ctx)
	if span.SpanContext().IsValid() {
		logger.base = logger.base.WithValues(
			"trace_id", span.SpanContext().TraceID().String(),
			"span_id", span.SpanContext().SpanID().String(),
		)
	}

	return logger
}

// WithFields adds structured fields to the logger
func (l *Logger) WithFields(fields Fields) *Logger {
	values := make([]interface{}, 0, len(fields)*2)
	for k, v := range fields {
		values = append(values, k, v)
	}

	return &Logger{
		base: l.base.WithValues(values...),
		name: l.name,
	}
}

// WithLabSession adds lab session context to the logger
func (l *Logger) WithLabSession(namespace, sessionID, userID string) *Logger {
	return l.WithFields(Fields{
		"namespace":  namespace,
		"session_id": sessionID,
		"user_id":    userID,
	})
}

// WithResource adds Kubernetes resource context to the logger  
func (l *Logger) WithResource(kind, namespace, name string) *Logger {
	return l.WithFields(Fields{
		"resource_kind":      kind,
		"resource_namespace": namespace,
		"resource_name":      name,
	})
}

// WithError adds error context to the logger
func (l *Logger) WithError(err error) *Logger {
	if err == nil {
		return l
	}
	
	return l.WithFields(Fields{
		"error":         err.Error(),
		"error_type":    fmt.Sprintf("%T", err),
		"has_error":     true,
	})
}

// Debug logs a debug message
func (l *Logger) Debug(msg string, fields ...Fields) {
	l.logWithFields(DebugLevel, msg, fields...)
}

// Info logs an info message  
func (l *Logger) Info(msg string, fields ...Fields) {
	l.logWithFields(InfoLevel, msg, fields...)
}

// Warn logs a warning message
func (l *Logger) Warn(msg string, fields ...Fields) {
	l.logWithFields(WarnLevel, msg, fields...)
}

// Error logs an error message
func (l *Logger) Error(err error, msg string, fields ...Fields) {
	logger := l.WithError(err)
	logger.logWithFields(ErrorLevel, msg, fields...)
}

// DebugWithContext logs a debug message with context
func (l *Logger) DebugWithContext(ctx context.Context, msg string, fields ...Fields) {
	l.WithContext(ctx).Debug(msg, fields...)
}

// InfoWithContext logs an info message with context
func (l *Logger) InfoWithContext(ctx context.Context, msg string, fields ...Fields) {
	l.WithContext(ctx).Info(msg, fields...)
}

// WarnWithContext logs a warning message with context
func (l *Logger) WarnWithContext(ctx context.Context, msg string, fields ...Fields) {
	l.WithContext(ctx).Warn(msg, fields...)
}

// ErrorWithContext logs an error message with context
func (l *Logger) ErrorWithContext(ctx context.Context, err error, msg string, fields ...Fields) {
	l.WithContext(ctx).Error(err, msg, fields...)
}

// logWithFields logs a message with the given fields
func (l *Logger) logWithFields(level LogLevel, msg string, fieldsList ...Fields) {
	// Merge all fields
	allFields := make(Fields)
	for _, fields := range fieldsList {
		for k, v := range fields {
			allFields[k] = v
		}
	}
	
	// Add timestamp and level
	allFields["timestamp"] = time.Now().Format(time.RFC3339)
	allFields["level"] = level.String()
	allFields["logger"] = l.name

	// Convert to key-value pairs for logr
	values := make([]interface{}, 0, len(allFields)*2)
	for k, v := range allFields {
		values = append(values, k, v)
	}

	// Log based on level
	switch level {
	case DebugLevel:
		l.base.V(1).Info(msg, values...)
	case InfoLevel:
		l.base.Info(msg, values...)
	case WarnLevel:
		l.base.Info(fmt.Sprintf("WARN: %s", msg), values...)
	case ErrorLevel:
		l.base.Error(nil, msg, values...)
	}
}

// ReconcileLogger provides logging utilities for reconciliation operations
type ReconcileLogger struct {
	*Logger
	namespace string
	name      string
}

// NewReconcileLogger creates a logger for reconciliation operations
func NewReconcileLogger(name, namespace, resourceName string) *ReconcileLogger {
	base := NewLogger("reconciler").WithFields(Fields{
		"controller":         name,
		"resource_namespace": namespace,
		"resource_name":      resourceName,
	})

	return &ReconcileLogger{
		Logger:    base,
		namespace: namespace,
		name:      resourceName,
	}
}

// StartReconcile logs the start of a reconciliation
func (rl *ReconcileLogger) StartReconcile(ctx context.Context) *ReconcileLogger {
	rl.InfoWithContext(ctx, "Starting reconciliation")
	return rl.WithContext(ctx).(*ReconcileLogger)
}

// EndReconcile logs the end of a reconciliation
func (rl *ReconcileLogger) EndReconcile(ctx context.Context, duration time.Duration, err error) {
	fields := Fields{
		"duration_ms": duration.Milliseconds(),
		"success":     err == nil,
	}
	
	if err != nil {
		rl.ErrorWithContext(ctx, err, "Reconciliation failed", fields)
	} else {
		rl.InfoWithContext(ctx, "Reconciliation completed", fields)
	}
}

// LogOperation logs a specific operation within reconciliation
func (rl *ReconcileLogger) LogOperation(ctx context.Context, operation string, fn func() error) error {
	start := time.Now()
	rl.DebugWithContext(ctx, fmt.Sprintf("Starting %s", operation))
	
	err := fn()
	duration := time.Since(start)
	
	fields := Fields{
		"operation":   operation,
		"duration_ms": duration.Milliseconds(),
		"success":     err == nil,
	}
	
	if err != nil {
		rl.ErrorWithContext(ctx, err, fmt.Sprintf("Operation %s failed", operation), fields)
	} else {
		rl.DebugWithContext(ctx, fmt.Sprintf("Operation %s completed", operation), fields)
	}
	
	return err
}

// EventLogger provides logging for Kubernetes events
type EventLogger struct {
	*Logger
}

// NewEventLogger creates a logger for Kubernetes events
func NewEventLogger() *EventLogger {
	return &EventLogger{
		Logger: NewLogger("events"),
	}
}

// LogEvent logs a Kubernetes event
func (el *EventLogger) LogEvent(ctx context.Context, eventType, reason, message string, obj map[string]interface{}) {
	fields := Fields{
		"event_type": eventType,
		"reason":     reason,
		"object":     obj,
	}
	
	switch eventType {
	case "Warning":
		el.WarnWithContext(ctx, message, fields)
	case "Error":
		el.ErrorWithContext(ctx, fmt.Errorf("kubernetes event: %s", message), message, fields)
	default:
		el.InfoWithContext(ctx, message, fields)
	}
}

// Global logger instances for convenience
var (
	ControllerLogger  = NewLogger("controller")
	ReconcilerLogger  = NewLogger("reconciler")
	MetricsLogger     = NewLogger("metrics")
	CircuitLogger     = NewLogger("circuit-breaker")
)