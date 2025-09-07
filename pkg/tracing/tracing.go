package tracing

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/jaeger"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/semconv/v1.17.0"
	oteltrace "go.opentelemetry.io/otel/trace"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	// Service name for tracing
	ServiceName = "lab-session-controller"
	
	// Common attribute keys (following dozlab-api attribute patterns)
	AttrNamespace     = "k8s.namespace"
	AttrResourceName  = "k8s.resource.name"
	AttrResourceKind  = "k8s.resource.kind"
	AttrSessionID     = "lab.session_id" 
	AttrUserID        = "lab.user_id"
	AttrOperation     = "operation"
	AttrErrorType     = "error.type"
	AttrRetryAttempt  = "retry.attempt"
)

// TracingConfig holds tracing configuration (following dozlab-api config patterns)
type TracingConfig struct {
	Enabled         bool
	JaegerEndpoint  string
	ServiceName     string
	ServiceVersion  string
	SampleRate      float64
}

// Tracer provides distributed tracing capabilities (following dozlab-api patterns)
type Tracer struct {
	tracer   oteltrace.Tracer
	config   TracingConfig
	provider *trace.TracerProvider
}

// NewTracer creates a new tracer instance
func NewTracer(config TracingConfig) (*Tracer, error) {
	if !config.Enabled {
		return &Tracer{
			tracer: otel.Tracer(ServiceName),
			config: config,
		}, nil
	}

	// Create Jaeger exporter
	exporter, err := jaeger.New(
		jaeger.WithCollectorEndpoint(jaeger.WithEndpoint(config.JaegerEndpoint)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create Jaeger exporter: %w", err)
	}

	// Create resource
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(config.ServiceName),
			semconv.ServiceVersion(config.ServiceVersion),
			attribute.String("environment", "production"),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	// Create tracer provider
	tp := trace.NewTracerProvider(
		trace.WithBatcher(exporter),
		trace.WithResource(res),
		trace.WithSampler(trace.TraceIDRatioBased(config.SampleRate)),
	)

	otel.SetTracerProvider(tp)

	tracer := &Tracer{
		tracer:   tp.Tracer(ServiceName),
		config:   config,
		provider: tp,
	}

	return tracer, nil
}

// Close shuts down the tracer
func (t *Tracer) Close(ctx context.Context) error {
	if t.provider != nil {
		return t.provider.Shutdown(ctx)
	}
	return nil
}

// StartSpan starts a new span (following dozlab-api span patterns)
func (t *Tracer) StartSpan(ctx context.Context, name string, opts ...oteltrace.SpanStartOption) (context.Context, oteltrace.Span) {
	return t.tracer.Start(ctx, name, opts...)
}

// ReconcileSpan creates a span for reconciliation operations
func (t *Tracer) ReconcileSpan(ctx context.Context, namespace, name string) (context.Context, oteltrace.Span) {
	return t.StartSpan(ctx, "reconcile",
		oteltrace.WithAttributes(
			attribute.String(AttrNamespace, namespace),
			attribute.String(AttrResourceName, name),
			attribute.String(AttrResourceKind, "LabSession"),
			attribute.String(AttrOperation, "reconcile"),
		),
		oteltrace.WithSpanKind(oteltrace.SpanKindServer),
	)
}

// OperationSpan creates a span for a specific operation
func (t *Tracer) OperationSpan(ctx context.Context, operation string, attrs ...attribute.KeyValue) (context.Context, oteltrace.Span) {
	allAttrs := []attribute.KeyValue{
		attribute.String(AttrOperation, operation),
	}
	allAttrs = append(allAttrs, attrs...)

	return t.StartSpan(ctx, operation,
		oteltrace.WithAttributes(allAttrs...),
		oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
	)
}

// PodCreationSpan creates a span for pod creation
func (t *Tracer) PodCreationSpan(ctx context.Context, namespace, sessionID string) (context.Context, oteltrace.Span) {
	return t.StartSpan(ctx, "create-pod",
		oteltrace.WithAttributes(
			attribute.String(AttrNamespace, namespace),
			attribute.String(AttrSessionID, sessionID),
			attribute.String(AttrOperation, "create-pod"),
		),
		oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
	)
}

// ServiceCreationSpan creates a span for service creation
func (t *Tracer) ServiceCreationSpan(ctx context.Context, namespace, sessionID string) (context.Context, oteltrace.Span) {
	return t.StartSpan(ctx, "create-service",
		oteltrace.WithAttributes(
			attribute.String(AttrNamespace, namespace),
			attribute.String(AttrSessionID, sessionID),
			attribute.String(AttrOperation, "create-service"),
		),
		oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
	)
}

// PVCCreationSpan creates a span for PVC creation
func (t *Tracer) PVCCreationSpan(ctx context.Context, namespace, sessionID string) (context.Context, oteltrace.Span) {
	return t.StartSpan(ctx, "create-pvc",
		oteltrace.WithAttributes(
			attribute.String(AttrNamespace, namespace),
			attribute.String(AttrSessionID, sessionID),
			attribute.String(AttrOperation, "create-pvc"),
		),
		oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
	)
}

// RetrySpan creates a span for retry operations
func (t *Tracer) RetrySpan(ctx context.Context, operation string, attempt int) (context.Context, oteltrace.Span) {
	return t.StartSpan(ctx, fmt.Sprintf("retry-%s", operation),
		oteltrace.WithAttributes(
			attribute.String(AttrOperation, operation),
			attribute.Int(AttrRetryAttempt, attempt),
		),
		oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
	)
}

// Span utilities for common operations

// RecordError records an error in the current span
func RecordError(span oteltrace.Span, err error, errorType string) {
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(
			attribute.String(AttrErrorType, errorType),
			attribute.Bool("error", true),
		)
		span.SetStatus(oteltrace.Status{
			Code:        oteltrace.StatusCodeError,
			Description: err.Error(),
		})
	}
}

// RecordSuccess marks a span as successful
func RecordSuccess(span oteltrace.Span) {
	span.SetAttributes(attribute.Bool("success", true))
	span.SetStatus(oteltrace.Status{Code: oteltrace.StatusCodeOk})
}

// AddSessionContext adds session context to a span
func AddSessionContext(span oteltrace.Span, sessionID, userID string) {
	span.SetAttributes(
		attribute.String(AttrSessionID, sessionID),
		attribute.String(AttrUserID, userID),
	)
}

// AddResourceContext adds Kubernetes resource context to a span
func AddResourceContext(span oteltrace.Span, kind, namespace, name string) {
	span.SetAttributes(
		attribute.String(AttrResourceKind, kind),
		attribute.String(AttrNamespace, namespace),
		attribute.String(AttrResourceName, name),
	)
}

// TracedReconciler wraps a reconciler with tracing (following dozlab-api wrapper patterns)
type TracedReconciler struct {
	reconciler ctrl.Reconciler
	tracer     *Tracer
}

// NewTracedReconciler creates a reconciler wrapper with tracing
func NewTracedReconciler(reconciler ctrl.Reconciler, tracer *Tracer) *TracedReconciler {
	return &TracedReconciler{
		reconciler: reconciler,
		tracer:     tracer,
	}
}

// Reconcile implements the Reconciler interface with tracing
func (tr *TracedReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Start tracing span
	spanCtx, span := tr.tracer.ReconcileSpan(ctx, req.Namespace, req.Name)
	defer span.End()

	// Record reconciliation start
	start := time.Now()
	span.AddEvent("reconciliation.started")

	// Execute reconciliation
	result, err := tr.reconciler.Reconcile(spanCtx, req)

	// Record duration and result
	duration := time.Since(start)
	span.SetAttributes(
		attribute.Int64("reconcile.duration_ms", duration.Milliseconds()),
		attribute.Bool("reconcile.requeue", !result.IsZero()),
	)

	if result.RequeueAfter > 0 {
		span.SetAttributes(
			attribute.Int64("reconcile.requeue_after_ms", result.RequeueAfter.Milliseconds()),
		)
	}

	// Record error or success
	if err != nil {
		RecordError(span, err, "reconciliation")
		span.AddEvent("reconciliation.failed")
	} else {
		RecordSuccess(span)
		span.AddEvent("reconciliation.completed")
	}

	return result, err
}

// TracedOperation executes an operation with tracing
func (t *Tracer) TracedOperation(ctx context.Context, name string, fn func(context.Context) error) error {
	spanCtx, span := t.OperationSpan(ctx, name)
	defer span.End()

	start := time.Now()
	span.AddEvent(fmt.Sprintf("%s.started", name))

	err := fn(spanCtx)
	
	duration := time.Since(start)
	span.SetAttributes(attribute.Int64("duration_ms", duration.Milliseconds()))

	if err != nil {
		RecordError(span, err, name)
		span.AddEvent(fmt.Sprintf("%s.failed", name))
	} else {
		RecordSuccess(span)
		span.AddEvent(fmt.Sprintf("%s.completed", name))
	}

	return err
}

// Global tracer instance
var GlobalTracer *Tracer

// InitTracing initializes global tracing
func InitTracing(config TracingConfig) error {
	tracer, err := NewTracer(config)
	if err != nil {
		return err
	}
	
	GlobalTracer = tracer
	return nil
}

// GetTracer returns the global tracer instance
func GetTracer() *Tracer {
	if GlobalTracer == nil {
		// Return a no-op tracer if not initialized
		GlobalTracer = &Tracer{
			tracer: otel.Tracer(ServiceName),
			config: TracingConfig{Enabled: false},
		}
	}
	return GlobalTracer
}

// Convenience functions using global tracer

// StartOperation starts a traced operation using the global tracer
func StartOperation(ctx context.Context, name string) (context.Context, oteltrace.Span) {
	return GetTracer().OperationSpan(ctx, name)
}

// TraceOperation executes an operation with tracing using the global tracer
func TraceOperation(ctx context.Context, name string, fn func(context.Context) error) error {
	return GetTracer().TracedOperation(ctx, name, fn)
}