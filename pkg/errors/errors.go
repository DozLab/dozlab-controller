package errors

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
)

// ErrorType represents different types of errors (following dozlab-api error patterns)
type ErrorType string

const (
	// Recoverable errors - should retry
	ErrorTypeTemporary     ErrorType = "temporary"
	ErrorTypeResourceQuota ErrorType = "resource_quota"
	ErrorTypeNetworkIssue  ErrorType = "network"
	ErrorTypeThrottling    ErrorType = "throttling"

	// Non-recoverable errors - should not retry  
	ErrorTypeValidation     ErrorType = "validation"
	ErrorTypeConfiguration  ErrorType = "configuration"
	ErrorTypeUnauthorized   ErrorType = "unauthorized"
	ErrorTypeNotFound       ErrorType = "not_found"

	// Critical errors - requires immediate attention
	ErrorTypeCritical ErrorType = "critical"
	ErrorTypeSecrity  ErrorType = "security"
)

// LabSessionError represents a structured error for lab session operations (following dozlab-api error patterns)
type LabSessionError struct {
	Type         ErrorType
	Message      string
	Underlying   error
	Namespace    string
	SessionID    string
	UserID       string
	Operation    string
	Timestamp    time.Time
	Retryable    bool
	RetryAfter   time.Duration
	Context      map[string]interface{}
}

// Error implements the error interface
func (e *LabSessionError) Error() string {
	if e.Underlying != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Type, e.Message, e.Underlying)
	}
	return fmt.Sprintf("[%s] %s", e.Type, e.Message)
}

// Unwrap returns the underlying error
func (e *LabSessionError) Unwrap() error {
	return e.Underlying
}

// Is checks if the error is of a specific type
func (e *LabSessionError) Is(target error) bool {
	if t, ok := target.(*LabSessionError); ok {
		return e.Type == t.Type
	}
	return false
}

// IsRetryable returns whether the error is retryable
func (e *LabSessionError) IsRetryable() bool {
	return e.Retryable
}

// GetRetryAfter returns the duration to wait before retrying
func (e *LabSessionError) GetRetryAfter() time.Duration {
	if e.RetryAfter > 0 {
		return e.RetryAfter
	}
	return 30 * time.Second // Default retry interval
}

// GetContext returns additional error context
func (e *LabSessionError) GetContext() map[string]interface{} {
	if e.Context == nil {
		return make(map[string]interface{})
	}
	return e.Context
}

// ErrorBuilder provides a fluent interface for building errors (following dozlab-api builder patterns)
type ErrorBuilder struct {
	err *LabSessionError
}

// NewError creates a new error builder
func NewError(errorType ErrorType, message string) *ErrorBuilder {
	return &ErrorBuilder{
		err: &LabSessionError{
			Type:      errorType,
			Message:   message,
			Timestamp: time.Now(),
			Retryable: isRetryableType(errorType),
			Context:   make(map[string]interface{}),
		},
	}
}

// WithUnderlying adds an underlying error
func (eb *ErrorBuilder) WithUnderlying(err error) *ErrorBuilder {
	eb.err.Underlying = err
	return eb
}

// WithSession adds session context
func (eb *ErrorBuilder) WithSession(namespace, sessionID, userID string) *ErrorBuilder {
	eb.err.Namespace = namespace
	eb.err.SessionID = sessionID
	eb.err.UserID = userID
	return eb
}

// WithOperation adds operation context
func (eb *ErrorBuilder) WithOperation(operation string) *ErrorBuilder {
	eb.err.Operation = operation
	return eb
}

// WithRetryAfter sets custom retry duration
func (eb *ErrorBuilder) WithRetryAfter(duration time.Duration) *ErrorBuilder {
	eb.err.RetryAfter = duration
	eb.err.Retryable = true
	return eb
}

// WithContext adds additional context
func (eb *ErrorBuilder) WithContext(key string, value interface{}) *ErrorBuilder {
	eb.err.Context[key] = value
	return eb
}

// WithContextMap adds multiple context fields
func (eb *ErrorBuilder) WithContextMap(ctx map[string]interface{}) *ErrorBuilder {
	for k, v := range ctx {
		eb.err.Context[k] = v
	}
	return eb
}

// Build returns the constructed error
func (eb *ErrorBuilder) Build() *LabSessionError {
	return eb.err
}

// Convenience constructors for common error types

// NewValidationError creates a validation error
func NewValidationError(message string, field string) *LabSessionError {
	return NewError(ErrorTypeValidation, message).
		WithContext("field", field).
		Build()
}

// NewResourceQuotaError creates a resource quota error  
func NewResourceQuotaError(message string, requested, limit string) *LabSessionError {
	return NewError(ErrorTypeResourceQuota, message).
		WithContext("requested", requested).
		WithContext("limit", limit).
		WithRetryAfter(2 * time.Minute).
		Build()
}

// NewTemporaryError creates a temporary error
func NewTemporaryError(message string, underlying error) *LabSessionError {
	return NewError(ErrorTypeTemporary, message).
		WithUnderlying(underlying).
		WithRetryAfter(30 * time.Second).
		Build()
}

// NewCriticalError creates a critical error
func NewCriticalError(message string, underlying error) *LabSessionError {
	return NewError(ErrorTypeCritical, message).
		WithUnderlying(underlying).
		Build()
}

// Error classification helpers

// isRetryableType determines if an error type is retryable by default
func isRetryableType(errorType ErrorType) bool {
	switch errorType {
	case ErrorTypeTemporary, ErrorTypeResourceQuota, ErrorTypeNetworkIssue, ErrorTypeThrottling:
		return true
	default:
		return false
	}
}

// ClassifyKubernetesError classifies a Kubernetes API error (following dozlab-api classification patterns)
func ClassifyKubernetesError(err error) *LabSessionError {
	if err == nil {
		return nil
	}

	if errors.IsNotFound(err) {
		return NewError(ErrorTypeNotFound, "Resource not found").
			WithUnderlying(err).
			Build()
	}

	if errors.IsAlreadyExists(err) {
		return NewError(ErrorTypeValidation, "Resource already exists").
			WithUnderlying(err).
			Build()
	}

	if errors.IsUnauthorized(err) || errors.IsForbidden(err) {
		return NewError(ErrorTypeUnauthorized, "Access denied").
			WithUnderlying(err).
			Build()
	}

	if errors.IsConflict(err) {
		return NewError(ErrorTypeTemporary, "Resource conflict").
			WithUnderlying(err).
			WithRetryAfter(5 * time.Second).
			Build()
	}

	if errors.IsServerTimeout(err) || errors.IsTimeout(err) {
		return NewError(ErrorTypeNetworkIssue, "Request timeout").
			WithUnderlying(err).
			WithRetryAfter(10 * time.Second).
			Build()
	}

	if errors.IsTooManyRequests(err) {
		return NewError(ErrorTypeThrottling, "Rate limited").
			WithUnderlying(err).
			WithRetryAfter(60 * time.Second).
			Build()
	}

	if errors.IsInvalid(err) || errors.IsBadRequest(err) {
		return NewError(ErrorTypeValidation, "Invalid request").
			WithUnderlying(err).
			Build()
	}

	// Default to temporary error for unknown Kubernetes errors
	return NewError(ErrorTypeTemporary, "Kubernetes API error").
		WithUnderlying(err).
		WithRetryAfter(30 * time.Second).
		Build()
}

// Result helpers for controller reconciliation

// ReconcileResult creates a reconcile result based on an error (following dozlab-api result patterns)
func ReconcileResult(err error) (ctrl.Result, error) {
	if err == nil {
		return ctrl.Result{}, nil
	}

	if labErr, ok := err.(*LabSessionError); ok {
		if labErr.IsRetryable() {
			return ctrl.Result{RequeueAfter: labErr.GetRetryAfter()}, nil
		}
		// For non-retryable errors, return the error to stop reconciliation
		return ctrl.Result{}, labErr
	}

	// For non-LabSessionError, classify and handle
	classifiedErr := ClassifyKubernetesError(err)
	if classifiedErr.IsRetryable() {
		return ctrl.Result{RequeueAfter: classifiedErr.GetRetryAfter()}, nil
	}

	return ctrl.Result{}, classifiedErr
}

// ReconcileSuccess creates a successful reconcile result
func ReconcileSuccess() (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

// ReconcileRequeue creates a reconcile result that requeues after a delay
func ReconcileRequeue(after time.Duration) (ctrl.Result, error) {
	return ctrl.Result{RequeueAfter: after}, nil
}

// ErrorHandler provides centralized error handling (following dozlab-api handler patterns)
type ErrorHandler struct {
	onCritical func(ctx context.Context, err *LabSessionError)
	onRetry    func(ctx context.Context, err *LabSessionError, attempt int)
}

// NewErrorHandler creates a new error handler
func NewErrorHandler() *ErrorHandler {
	return &ErrorHandler{}
}

// OnCritical sets the critical error handler
func (eh *ErrorHandler) OnCritical(handler func(ctx context.Context, err *LabSessionError)) *ErrorHandler {
	eh.onCritical = handler
	return eh
}

// OnRetry sets the retry handler
func (eh *ErrorHandler) OnRetry(handler func(ctx context.Context, err *LabSessionError, attempt int)) *ErrorHandler {
	eh.onRetry = handler
	return eh
}

// Handle processes an error and returns appropriate reconcile result
func (eh *ErrorHandler) Handle(ctx context.Context, err error, attempt int) (ctrl.Result, error) {
	if err == nil {
		return ReconcileSuccess()
	}

	var labErr *LabSessionError
	if le, ok := err.(*LabSessionError); ok {
		labErr = le
	} else {
		labErr = ClassifyKubernetesError(err)
	}

	// Handle critical errors
	if labErr.Type == ErrorTypeCritical && eh.onCritical != nil {
		eh.onCritical(ctx, labErr)
	}

	// Handle retries
	if labErr.IsRetryable() && eh.onRetry != nil {
		eh.onRetry(ctx, labErr, attempt)
	}

	return ReconcileResult(labErr)
}

// ErrorMetrics provides metrics for error tracking
type ErrorMetrics struct {
	errorCounts    map[ErrorType]int
	retryCount     int
	criticalCount  int
	lastErrorTime  time.Time
}

// NewErrorMetrics creates new error metrics
func NewErrorMetrics() *ErrorMetrics {
	return &ErrorMetrics{
		errorCounts: make(map[ErrorType]int),
	}
}

// RecordError records an error occurrence
func (em *ErrorMetrics) RecordError(err *LabSessionError) {
	em.errorCounts[err.Type]++
	em.lastErrorTime = time.Now()

	if err.Type == ErrorTypeCritical {
		em.criticalCount++
	}

	if err.IsRetryable() {
		em.retryCount++
	}
}

// GetStats returns error statistics
func (em *ErrorMetrics) GetStats() map[string]interface{} {
	stats := map[string]interface{}{
		"total_retries":      em.retryCount,
		"critical_errors":    em.criticalCount,
		"last_error_time":    em.lastErrorTime.Unix(),
		"error_counts":       em.errorCounts,
	}

	return stats
}