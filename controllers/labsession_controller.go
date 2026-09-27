package controllers

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	labcontroller "dozlab-controller/internal/controller"
)

// LabSessionReconciler reconciles a LabSession object
type LabSessionReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Following dozlab-api pattern for resource management
	DefaultResourceLimits map[string]string
	MaxResourceLimits     map[string]string
	// Following dozlab-api retry patterns
	MaxRetries       int
	BaseRetryDelay   time.Duration
	// Following dozlab-api patterns for status updates
	statusUpdateMutex map[string]*sync.Mutex
	// ResourceBuilder builds the lab pod (shared with internal/controller)
	ResourceBuilder *labcontroller.ResourceBuilder
}

const (
	LabSessionFinalizer = "labsession.dozlab.io/finalizer"
	// Following dozlab-api patterns for resource management
	DefaultCPULimit     = "2"
	DefaultMemoryLimit  = "4Gi"
	MaxCPULimit         = "8"
	MaxMemoryLimit      = "16Gi"
	// Following dozlab-api patterns for retry logic
	MaxRetryAttempts    = 5
	BaseRetryInterval   = 2 * time.Second
)

// +kubebuilder:rbac:groups=dozlab.io,resources=labsessions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dozlab.io,resources=labsessions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dozlab.io,resources=labsessions/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete

func (r *LabSessionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx).WithValues("labsession", req.NamespacedName)

	// RACE CONDITION FIX: Initialize status update mutex for this resource (following dozlab-api pattern)
	if r.statusUpdateMutex == nil {
		r.statusUpdateMutex = make(map[string]*sync.Mutex)
	}
	key := req.NamespacedName.String()
	if r.statusUpdateMutex[key] == nil {
		r.statusUpdateMutex[key] = &sync.Mutex{}
	}

	// Fetch the LabSession instance with retry (following dozlab-api retry patterns)
	labSession := &unstructured.Unstructured{}
	labSession.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "dozlab.io",
		Version: "v1",
		Kind:    "LabSession",
	})

	err := r.getWithRetry(ctx, req.NamespacedName, labSession)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("LabSession resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get LabSession after retries")
		return ctrl.Result{RequeueAfter: r.calculateRetryDelay(1)}, err
	}

	// Handle deletion
	if labSession.GetDeletionTimestamp() != nil {
		return r.reconcileDelete(ctx, labSession)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(labSession, LabSessionFinalizer) {
		controllerutil.AddFinalizer(labSession, LabSessionFinalizer)
		return ctrl.Result{}, r.Update(ctx, labSession)
	}

	// Handle creation/update
	return r.reconcileLabSession(ctx, labSession)
}

func (r *LabSessionReconciler) reconcileLabSession(ctx context.Context, labSession *unstructured.Unstructured) (ctrl.Result, error) {
	// Extract spec fields
	spec, found, err := unstructured.NestedMap(labSession.Object, "spec")
	if err != nil || !found {
		return ctrl.Result{}, fmt.Errorf("failed to get spec: %w", err)
	}

	userID, _ := spec["userId"].(string)
	sessionID, _ := spec["sessionId"].(string)

	if userID == "" || sessionID == "" {
		return ctrl.Result{}, fmt.Errorf("userId and sessionId are required")
	}

	// Update status to Creating if currently Pending
	currentPhase := r.getStatusField(labSession, "phase")
	if currentPhase == "" || currentPhase == "Pending" {
		r.updateStatus(ctx, labSession, "Creating", "Creating lab session resources", nil)
	}

	// STORAGE: Create PVCs first to ensure persistent storage
	if sessionID != "" {
		err := r.reconcilePVCs(ctx, labSession, sessionID)
		if err != nil {
			r.updateStatus(ctx, labSession, "Failed", fmt.Sprintf("Failed to create PVCs: %v", err), nil)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, err
		}
	}

	// Reconcile Pod
	pod, err := r.reconcilePod(ctx, labSession, spec)
	if err != nil {
		r.updateStatus(ctx, labSession, "Failed", fmt.Sprintf("Failed to create pod: %v", err), nil)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	// Reconcile Service
	service, err := r.reconcileService(ctx, labSession, spec)
	if err != nil {
		r.updateStatus(ctx, labSession, "Failed", fmt.Sprintf("Failed to create service: %v", err), nil)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	// Update status based on pod and service state
	return r.updateLabSessionStatus(ctx, labSession, pod, service)
}

func (r *LabSessionReconciler) reconcilePod(ctx context.Context, labSession *unstructured.Unstructured, spec map[string]interface{}) (*corev1.Pod, error) {
	sessionID, _ := spec["sessionId"].(string)

	podName := fmt.Sprintf("lab-session-%s", sessionID)
	pod := &corev1.Pod{}

	err := r.Get(ctx, types.NamespacedName{
		Name:      podName,
		Namespace: labSession.GetNamespace(),
	}, pod)

	if err != nil && errors.IsNotFound(err) {
		// Create new pod
		pod, err = r.buildPod(labSession, spec)
		if err != nil {
			return nil, err
		}
		if err := ctrl.SetControllerReference(labSession, pod, r.Scheme); err != nil {
			return nil, err
		}
		return pod, r.Create(ctx, pod)
	} else if err != nil {
		return nil, err
	}

	return pod, nil
}

// buildPod builds the lab pod with the shared internal/controller builder. The
// legacy resource caps and VS Code password default still apply here.
func (r *LabSessionReconciler) buildPod(labSession *unstructured.Unstructured, spec map[string]interface{}) (*corev1.Pod, error) {
	session := &labcontroller.LabSession{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(spec, &session.Spec); err != nil {
		return nil, fmt.Errorf("invalid LabSession spec: %w", err)
	}
	session.Namespace = labSession.GetNamespace()

	resourceLimits := r.extractResourceLimits(spec)
	session.Spec.Resources.Memory = resourceLimits.Memory
	session.Spec.Resources.CPU = resourceLimits.CPU
	session.Spec.Config.VSCodePassword = r.extractPodConfig(spec).VSCodePassword

	return r.ResourceBuilder.BuildPod(session), nil
}

func (r *LabSessionReconciler) reconcileService(ctx context.Context, labSession *unstructured.Unstructured, spec map[string]interface{}) (*corev1.Service, error) {
	sessionID, _ := spec["sessionId"].(string)
	serviceName := fmt.Sprintf("lab-service-%s", sessionID)

	service := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      serviceName,
		Namespace: labSession.GetNamespace(),
	}, service)

	if err != nil && errors.IsNotFound(err) {
		// Create new service
		service = r.buildService(labSession, spec)
		if err := ctrl.SetControllerReference(labSession, service, r.Scheme); err != nil {
			return nil, err
		}
		return service, r.Create(ctx, service)
	} else if err != nil {
		return nil, err
	}

	return service, nil
}

func (r *LabSessionReconciler) buildService(labSession *unstructured.Unstructured, spec map[string]interface{}) *corev1.Service {
	sessionID, _ := spec["sessionId"].(string)
	serviceName := fmt.Sprintf("lab-service-%s", sessionID)

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: labSession.GetNamespace(),
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"session-id": sessionID,
			},
			Ports: []corev1.ServicePort{
				{
					Name:       "vscode",
					Port:       8080,
					TargetPort: intstr.FromInt(8080),
				},
				{
					Name:       "terminal",
					Port:       8081,
					TargetPort: intstr.FromInt(8081),
				},
				{
					Name:       "vm-ssh",
					Port:       22,
					TargetPort: intstr.FromInt(22),
				},
			},
		},
	}

	return service
}

func (r *LabSessionReconciler) updateLabSessionStatus(ctx context.Context, labSession *unstructured.Unstructured, pod *corev1.Pod, service *corev1.Service) (ctrl.Result, error) {
	// Determine overall phase based on pod status
	phase := "Creating"
	message := "Creating lab session resources"
	endpoints := make(map[string]interface{})

	if pod.Status.Phase == corev1.PodRunning {
		phase = "Running"
		message = "Lab session is running"
		
		// Build endpoints
		if service != nil && pod.Status.PodIP != "" {
			clusterIP := service.Spec.ClusterIP
			if clusterIP != "" {
				endpoints["vscode"] = fmt.Sprintf("http://%s:8080", clusterIP)
				endpoints["terminal"] = fmt.Sprintf("ws://%s:8081", clusterIP)
				endpoints["ssh"] = fmt.Sprintf("ssh://root@%s:22", clusterIP)
			}
		}
	} else if pod.Status.Phase == corev1.PodFailed {
		phase = "Failed"
		message = fmt.Sprintf("Pod failed: %s", pod.Status.Message)
	}

	// Calculate VM IP if pod IP is available
	vmIP := ""
	if pod.Status.PodIP != "" {
		vmIP = calculateVMIP(pod.Status.PodIP)
	}

	statusUpdate := map[string]interface{}{
		"phase":       phase,
		"message":     message,
		"podName":     pod.Name,
		"serviceName": service.Name,
		"endpoints":   endpoints,
		"podIP":       pod.Status.PodIP,
		"vmIP":        vmIP,
	}

	r.updateStatus(ctx, labSession, phase, message, statusUpdate)

	// Requeue if still creating
	if phase == "Creating" {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// Helper functions

func (r *LabSessionReconciler) getStatusField(labSession *unstructured.Unstructured, field string) string {
	if value, found, err := unstructured.NestedString(labSession.Object, "status", field); err == nil && found {
		return value
	}
	return ""
}

func (r *LabSessionReconciler) updateStatus(ctx context.Context, labSession *unstructured.Unstructured, phase, message string, extraFields map[string]interface{}) error {
	// RACE CONDITION FIX: Use mutex to prevent concurrent status updates (following dozlab-api pattern)
	key := labSession.GetNamespace() + "/" + labSession.GetName()
	if r.statusUpdateMutex == nil {
		r.statusUpdateMutex = make(map[string]*sync.Mutex)
	}
	if r.statusUpdateMutex[key] == nil {
		r.statusUpdateMutex[key] = &sync.Mutex{}
	}
	
	mutex := r.statusUpdateMutex[key]
	mutex.Lock()
	defer mutex.Unlock()
	
	// Get fresh copy of the resource to avoid conflicts
	fresh := &unstructured.Unstructured{}
	fresh.SetGroupVersionKind(labSession.GetObjectKind().GroupVersionKind())
	if err := r.Get(ctx, types.NamespacedName{
		Name:      labSession.GetName(),
		Namespace: labSession.GetNamespace(),
	}, fresh); err != nil {
		return err
	}

	status := map[string]interface{}{
		"phase":   phase,
		"message": message,
		"lastUpdateTime": time.Now().Format(time.RFC3339),
	}

	// Add extra fields
	for k, v := range extraFields {
		status[k] = v
	}

	unstructured.SetNestedMap(fresh.Object, status, "status")
	
	// RETRY LOGIC: Retry status updates with exponential backoff
	return r.updateStatusWithRetry(ctx, fresh, MaxRetryAttempts)
}

func getStringFromMap(m map[string]interface{}, key, defaultVal string) string {
	if m == nil {
		return defaultVal
	}
	if val, ok := m[key].(string); ok {
		return val
	}
	return defaultVal
}



func calculateVMIP(podIP string) string {
	parts := strings.Split(podIP, ".")
	if len(parts) == 4 {
		if lastOctet, err := strconv.Atoi(parts[3]); err == nil {
			parts[3] = strconv.Itoa(lastOctet + 1)
			return strings.Join(parts, ".")
		}
	}
	return ""
}

// Following dozlab-api patterns: Helper functions for enhanced controller reliability

// RESOURCE MANAGEMENT FIX: Enforce resource quotas and limits
func (r *LabSessionReconciler) enforceResourceLimits(requested, maxAllowed, resourceType string) string {
	// Parse requested and max values
	requestedQ, err := resource.ParseQuantity(requested)
	if err != nil {
		log.Log.Error(err, "Invalid resource quantity", "requested", requested, "type", resourceType)
		if resourceType == "memory" {
			return DefaultMemoryLimit
		}
		return DefaultCPULimit
	}
	
	maxQ, err := resource.ParseQuantity(maxAllowed)
	if err != nil {
		return requested // Use requested if max is invalid
	}
	
	// Enforce limit
	if requestedQ.Cmp(maxQ) > 0 {
		log.Log.Info("Resource request exceeds maximum, capping", 
			"requested", requested, "max", maxAllowed, "type", resourceType)
		return maxAllowed
	}
	
	return requested
}

// RETRY LOGIC: Get resource with exponential backoff
func (r *LabSessionReconciler) getWithRetry(ctx context.Context, key types.NamespacedName, obj client.Object) error {
	var lastErr error
	for attempt := 0; attempt < MaxRetryAttempts; attempt++ {
		err := r.Get(ctx, key, obj)
		if err == nil {
			return nil
		}
		
		if errors.IsNotFound(err) {
			return err // Don't retry NotFound errors
		}
		
		lastErr = err
		if attempt < MaxRetryAttempts-1 {
			delay := r.calculateRetryDelay(attempt + 1)
			log.Log.Info("Retrying Get operation", "attempt", attempt+1, "delay", delay, "error", err)
			time.Sleep(delay)
		}
	}
	
	return fmt.Errorf("failed after %d attempts: %w", MaxRetryAttempts, lastErr)
}

// RETRY LOGIC: Calculate exponential backoff delay
func (r *LabSessionReconciler) calculateRetryDelay(attempt int) time.Duration {
	baseDelay := r.BaseRetryDelay
	if baseDelay == 0 {
		baseDelay = BaseRetryInterval // Fallback to constant
	}
	
	if attempt <= 0 {
		return baseDelay
	}
	// Exponential backoff with jitter: base * 2^attempt, max 60s
	delay := time.Duration(float64(baseDelay) * math.Pow(2, float64(attempt-1)))
	if delay > 60*time.Second {
		delay = 60 * time.Second
	}
	return delay
}

// RETRY LOGIC: Update status with retry and exponential backoff
func (r *LabSessionReconciler) updateStatusWithRetry(ctx context.Context, obj *unstructured.Unstructured, maxAttempts int) error {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err := r.Status().Update(ctx, obj)
		if err == nil {
			return nil
		}
		
		// Check if it's a conflict error (resource was modified)
		if errors.IsConflict(err) {
			// Get fresh copy for next attempt
			fresh := &unstructured.Unstructured{}
			fresh.SetGroupVersionKind(obj.GetObjectKind().GroupVersionKind())
			if getErr := r.Get(ctx, types.NamespacedName{
				Name:      obj.GetName(),
				Namespace: obj.GetNamespace(),
			}, fresh); getErr == nil {
				// Copy status from our version to the fresh version
				if status, found, _ := unstructured.NestedMap(obj.Object, "status"); found {
					unstructured.SetNestedMap(fresh.Object, status, "status")
				}
				obj = fresh
			}
		}
		
		lastErr = err
		if attempt < maxAttempts-1 {
			delay := r.calculateRetryDelay(attempt + 1)
			log.Log.Info("Retrying status update", "attempt", attempt+1, "delay", delay, "error", err)
			time.Sleep(delay)
		}
	}
	
	return fmt.Errorf("failed to update status after %d attempts: %w", maxAttempts, lastErr)
}

// FINALIZER DEADLOCK FIX: Safe finalizer removal with timeout
func (r *LabSessionReconciler) reconcileDelete(ctx context.Context, labSession *unstructured.Unstructured) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("Deleting LabSession resources")

	// FINALIZER DEADLOCK FIX: Add timeout to prevent deadlocks (following dozlab-api timeout pattern)
	deleteCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	// Update status to Terminating with mutex protection
	r.updateStatus(deleteCtx, labSession, "Terminating", "Cleaning up lab session resources", nil)

	// Verify all owned resources are deleted before removing finalizer
	sessionID, _ := getNestedString(labSession.Object, "spec", "sessionId")
	if sessionID != "" {
		if err := r.verifyResourcesDeleted(deleteCtx, labSession, sessionID); err != nil {
			log.Error(err, "Waiting for resources to be deleted")
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
	}

	// Safe finalizer removal
	controllerutil.RemoveFinalizer(labSession, LabSessionFinalizer)
	
	// Use retry logic for finalizer update
	updateErr := r.updateWithRetry(deleteCtx, labSession, MaxRetryAttempts)
	if updateErr != nil {
		log.Error(updateErr, "Failed to remove finalizer")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, updateErr
	}
	
	log.Info("Successfully removed finalizer")
	return ctrl.Result{}, nil
}

// FINALIZER DEADLOCK FIX: Verify owned resources are properly deleted
func (r *LabSessionReconciler) verifyResourcesDeleted(ctx context.Context, labSession *unstructured.Unstructured, sessionID string) error {
	namespace := labSession.GetNamespace()
	
	// Check if pod is deleted
	pod := &corev1.Pod{}
	podName := fmt.Sprintf("lab-session-%s", sessionID)
	err := r.Get(ctx, types.NamespacedName{Name: podName, Namespace: namespace}, pod)
	if err == nil {
		return fmt.Errorf("pod %s still exists", podName)
	} else if !errors.IsNotFound(err) {
		return err
	}
	
	// Check if service is deleted
	service := &corev1.Service{}
	serviceName := fmt.Sprintf("lab-service-%s", sessionID)
	err = r.Get(ctx, types.NamespacedName{Name: serviceName, Namespace: namespace}, service)
	if err == nil {
		return fmt.Errorf("service %s still exists", serviceName)
	} else if !errors.IsNotFound(err) {
		return err
	}
	
	return nil
}

// RETRY LOGIC: Update resource with retry logic
func (r *LabSessionReconciler) updateWithRetry(ctx context.Context, obj client.Object, maxAttempts int) error {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err := r.Update(ctx, obj)
		if err == nil {
			return nil
		}
		
		lastErr = err
		if attempt < maxAttempts-1 {
			delay := r.calculateRetryDelay(attempt + 1)
			time.Sleep(delay)
		}
	}
	
	return fmt.Errorf("failed to update after %d attempts: %w", maxAttempts, lastErr)
}

// Helper function to get nested string values safely
func getNestedString(obj map[string]interface{}, fields ...string) (string, bool) {
	val, found, err := unstructured.NestedString(obj, fields...)
	if err != nil || !found {
		return "", false
	}
	return val, true
}

// SetupWithManager sets up the controller with the Manager
func (r *LabSessionReconciler) SetupWithManager(mgr ctrl.Manager, maxConcurrentReconciles int) error {
	if r.ResourceBuilder == nil {
		return fmt.Errorf("LabSessionReconciler.ResourceBuilder must be set")
	}
	// Initialize controller with proper defaults (following dozlab-api initialization pattern)
	if r.MaxRetries == 0 {
		r.MaxRetries = MaxRetryAttempts
	}
	if r.BaseRetryDelay == 0 {
		r.BaseRetryDelay = BaseRetryInterval
	}
	if r.statusUpdateMutex == nil {
		r.statusUpdateMutex = make(map[string]*sync.Mutex)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&unstructured.Unstructured{}).
		WithEventFilter(predicate.NewPredicateFuncs(func(obj client.Object) bool {
			// Only watch LabSession resources
			gvk := obj.GetObjectKind().GroupVersionKind()
			return gvk.Group == "dozlab.io" && gvk.Kind == "LabSession"
		})).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: maxConcurrentReconciles,
		}).
		Complete(r)
}

// STORAGE: Create PVCs for persistent storage
func (r *LabSessionReconciler) reconcilePVCs(ctx context.Context, labSession *unstructured.Unstructured, sessionID string) error {
	namespace := labSession.GetNamespace()
	
	// Define PVC specifications
	pvcs := []struct {
		name string
		size string
	}{
		{fmt.Sprintf("vm-data-%s", sessionID), "10Gi"},
		{fmt.Sprintf("vscode-data-%s", sessionID), "5Gi"},
	}
	
	for _, pvcSpec := range pvcs {
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pvcSpec.name,
				Namespace: namespace,
				Labels: map[string]string{
					"app":        "lab-environment",
					"session-id": sessionID,
				},
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{
					corev1.ReadWriteOnce,
				},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse(pvcSpec.size),
					},
				},
				StorageClassName: getStringPointer("default"), // Use default storage class
			},
		}
		
		// Set owner reference for cleanup
		if err := controllerutil.SetControllerReference(labSession, pvc, r.Scheme); err != nil {
			return err
		}
		
		// Create or update PVC
		foundPVC := &corev1.PersistentVolumeClaim{}
		err := r.Get(ctx, types.NamespacedName{Name: pvc.Name, Namespace: pvc.Namespace}, foundPVC)
		if err != nil && errors.IsNotFound(err) {
			if err := r.Create(ctx, pvc); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	
	return nil
}

// Helper function for string pointer
func getStringPointer(s string) *string {
	return &s
}

// HELPER: Resource limits structure for better organization
type ResourceLimits struct {
	Memory string
	CPU    string
}

// HELPER: Pod configuration structure for better organization  
type PodConfig struct {
	VSCodePassword string
}

// HELPER: Extract and validate resource limits from spec
func (r *LabSessionReconciler) extractResourceLimits(spec map[string]interface{}) ResourceLimits {
	resources, _ := spec["resources"].(map[string]interface{})
	return ResourceLimits{
		Memory: r.enforceResourceLimits(getStringFromMap(resources, "memory", DefaultMemoryLimit), MaxMemoryLimit, "memory"),
		CPU:    r.enforceResourceLimits(getStringFromMap(resources, "cpu", DefaultCPULimit), MaxCPULimit, "cpu"),
	}
}

// HELPER: Extract pod configuration parameters
func (r *LabSessionReconciler) extractPodConfig(spec map[string]interface{}) PodConfig {
	config, _ := spec["config"].(map[string]interface{})
	return PodConfig{
		VSCodePassword: getStringFromMap(config, "vsCodePassword", "password123"),
	}
}
