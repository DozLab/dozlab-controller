package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	LabSessionFinalizer = "labsession.dozlab.io/finalizer"
)

// LabSessionReconciler reconciles a LabSession object
type LabSessionReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	Recorder        record.EventRecorder
	ResourceBuilder *ResourceBuilder
}

// +kubebuilder:rbac:groups=dozlab.io,resources=labsessions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dozlab.io,resources=labsessions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dozlab.io,resources=labsessions/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile implements the reconciliation loop
func (r *LabSessionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling LabSession", "namespace", req.Namespace, "name", req.Name)

	// Fetch the LabSession instance
	session := &LabSession{}
	if err := r.Get(ctx, req.NamespacedName, session); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("LabSession resource not found, ignoring")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get LabSession")
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !session.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, session)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(session, LabSessionFinalizer) {
		controllerutil.AddFinalizer(session, LabSessionFinalizer)
		if err := r.Update(ctx, session); err != nil {
			logger.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// State machine reconciliation based on current phase
	switch session.Status.Phase {
	case SessionPhasePending, "":
		return r.reconcilePending(ctx, session)
	case SessionPhaseCreating:
		return r.reconcileCreating(ctx, session)
	case SessionPhaseRunning:
		return r.reconcileRunning(ctx, session)
	case SessionPhaseFailed:
		return r.reconcileFailed(ctx, session)
	default:
		logger.Info("Unknown phase", "phase", session.Status.Phase)
		return ctrl.Result{}, nil
	}
}

// reconcilePending handles the Pending phase
func (r *LabSessionReconciler) reconcilePending(ctx context.Context, session *LabSession) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Transitioning from Pending to Creating")

	// Update phase to Creating
	session.Status.Phase = SessionPhaseCreating
	session.Status.Message = "Creating lab session resources"
	session.Status.StartTime = &metav1.Time{Time: time.Now()}
	session.Status.ObservedGeneration = session.Generation

	if err := r.Status().Update(ctx, session); err != nil {
		logger.Error(err, "Failed to update status to Creating")
		return ctrl.Result{}, err
	}

	r.Recorder.Event(session, corev1.EventTypeNormal, "Creating", "Starting lab session creation")
	return ctrl.Result{Requeue: true}, nil
}

// reconcileCreating handles the Creating phase
func (r *LabSessionReconciler) reconcileCreating(ctx context.Context, session *LabSession) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling Creating phase")

	// Step 1: Create PVCs
	if err := r.ensurePVCs(ctx, session); err != nil {
		r.updateStatusFailed(ctx, session, "Failed to create PVCs", err)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	// Step 2: Wait for PVCs to be bound
	pvcsReady, err := r.checkPVCsReady(ctx, session)
	if err != nil {
		logger.Error(err, "Failed to check PVC status")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, err
	}
	if !pvcsReady {
		logger.Info("Waiting for PVCs to be bound")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Step 3: Create Pod
	if err := r.ensurePod(ctx, session); err != nil {
		r.updateStatusFailed(ctx, session, "Failed to create Pod", err)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	// Step 4: Create Service
	if err := r.ensureService(ctx, session); err != nil {
		r.updateStatusFailed(ctx, session, "Failed to create Service", err)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	// Step 5: Check if pod is running
	podReady, podIP, err := r.checkPodReady(ctx, session)
	if err != nil {
		logger.Error(err, "Failed to check pod status")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, err
	}

	if podReady {
		// Pod is ready, transition to Running
		logger.Info("Pod is ready, transitioning to Running")
		session.Status.Phase = SessionPhaseRunning
		session.Status.Message = "Lab session is running"
		session.Status.PodIP = podIP
		session.Status.VMIP = calculateVMIP(podIP)

		// Update endpoints
		if err := r.updateEndpoints(ctx, session); err != nil {
			logger.Error(err, "Failed to update endpoints")
		}

		// Update conditions
		r.setCondition(session, ConditionTypePodReady, metav1.ConditionTrue, "PodRunning", "Pod is running")
		r.setCondition(session, ConditionTypeServiceReady, metav1.ConditionTrue, "ServiceCreated", "Service is ready")
		r.setCondition(session, ConditionTypeReady, metav1.ConditionTrue, "SessionReady", "Lab session is ready")

		if err := r.Status().Update(ctx, session); err != nil {
			logger.Error(err, "Failed to update status to Running")
			return ctrl.Result{}, err
		}

		r.Recorder.Event(session, corev1.EventTypeNormal, "Running", "Lab session is now running")
		return ctrl.Result{}, nil
	}

	// Pod not ready yet, requeue
	logger.Info("Waiting for pod to be ready")
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// reconcileRunning handles the Running phase
func (r *LabSessionReconciler) reconcileRunning(ctx context.Context, session *LabSession) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling Running phase")

	// Check if pod is still running
	podReady, _, err := r.checkPodReady(ctx, session)
	if err != nil {
		logger.Error(err, "Failed to check pod status")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	if !podReady {
		logger.Info("Pod is no longer ready, transitioning to Failed")
		r.updateStatusFailed(ctx, session, "Pod failed", fmt.Errorf("pod is no longer ready"))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Session is healthy, reconcile again after some time
	return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
}

// reconcileFailed handles the Failed phase
func (r *LabSessionReconciler) reconcileFailed(ctx context.Context, session *LabSession) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling Failed phase")

	// For now, just log - could implement retry logic here
	return ctrl.Result{}, nil
}

// reconcileDelete handles deletion
func (r *LabSessionReconciler) reconcileDelete(ctx context.Context, session *LabSession) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling deletion")

	// Update status to Terminating
	session.Status.Phase = SessionPhaseTerminating
	session.Status.Message = "Cleaning up lab session resources"
	if err := r.Status().Update(ctx, session); err != nil {
		logger.Error(err, "Failed to update status to Terminating")
		// Continue with deletion anyway
	}

	r.Recorder.Event(session, corev1.EventTypeNormal, "Terminating", "Cleaning up lab session")

	// Remove finalizer
	controllerutil.RemoveFinalizer(session, LabSessionFinalizer)
	if err := r.Update(ctx, session); err != nil {
		logger.Error(err, "Failed to remove finalizer")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully deleted LabSession")
	return ctrl.Result{}, nil
}

// ensurePVCs creates PVCs if they don't exist
func (r *LabSessionReconciler) ensurePVCs(ctx context.Context, session *LabSession) error {
	logger := log.FromContext(ctx)
	pvcs := r.ResourceBuilder.BuildPVCs(session)

	for _, pvc := range pvcs {
		// Set owner reference
		if err := controllerutil.SetControllerReference(session, pvc, r.Scheme); err != nil {
			return fmt.Errorf("failed to set owner reference on PVC %s: %w", pvc.Name, err)
		}

		// Check if PVC already exists
		found := &corev1.PersistentVolumeClaim{}
		err := r.Get(ctx, types.NamespacedName{Name: pvc.Name, Namespace: pvc.Namespace}, found)
		if err != nil && errors.IsNotFound(err) {
			logger.Info("Creating PVC", "name", pvc.Name)
			if err := r.Create(ctx, pvc); err != nil {
				return fmt.Errorf("failed to create PVC %s: %w", pvc.Name, err)
			}
			r.Recorder.Eventf(session, corev1.EventTypeNormal, "PVCCreated", "Created PVC %s", pvc.Name)
		} else if err != nil {
			return fmt.Errorf("failed to get PVC %s: %w", pvc.Name, err)
		}
	}

	return nil
}

// checkPVCsReady checks if all PVCs are bound
func (r *LabSessionReconciler) checkPVCsReady(ctx context.Context, session *LabSession) (bool, error) {
	sessionID := session.Spec.SessionID
	pvcNames := []string{
		fmt.Sprintf("vm-data-%s", sessionID),
		fmt.Sprintf("vscode-data-%s", sessionID),
	}

	for _, pvcName := range pvcNames {
		pvc := &corev1.PersistentVolumeClaim{}
		err := r.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: session.Namespace}, pvc)
		if err != nil {
			return false, err
		}
		if pvc.Status.Phase != corev1.ClaimBound {
			return false, nil
		}
	}

	return true, nil
}

// ensurePod creates a pod if it doesn't exist
func (r *LabSessionReconciler) ensurePod(ctx context.Context, session *LabSession) error {
	logger := log.FromContext(ctx)
	pod := r.ResourceBuilder.BuildPod(session)

	// Set owner reference
	if err := controllerutil.SetControllerReference(session, pod, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on pod: %w", err)
	}

	// Check if pod already exists
	found := &corev1.Pod{}
	err := r.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, found)
	if err != nil && errors.IsNotFound(err) {
		logger.Info("Creating Pod", "name", pod.Name)
		if err := r.Create(ctx, pod); err != nil {
			return fmt.Errorf("failed to create pod: %w", err)
		}
		session.Status.PodName = pod.Name
		r.Recorder.Eventf(session, corev1.EventTypeNormal, "PodCreated", "Created Pod %s", pod.Name)
	} else if err != nil {
		return fmt.Errorf("failed to get pod: %w", err)
	} else {
		session.Status.PodName = found.Name
	}

	return nil
}

// ensureService creates a service if it doesn't exist
func (r *LabSessionReconciler) ensureService(ctx context.Context, session *LabSession) error {
	logger := log.FromContext(ctx)
	service := r.ResourceBuilder.BuildService(session)

	// Set owner reference
	if err := controllerutil.SetControllerReference(session, service, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on service: %w", err)
	}

	// Check if service already exists
	found := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: service.Name, Namespace: service.Namespace}, found)
	if err != nil && errors.IsNotFound(err) {
		logger.Info("Creating Service", "name", service.Name)
		if err := r.Create(ctx, service); err != nil {
			return fmt.Errorf("failed to create service: %w", err)
		}
		session.Status.ServiceName = service.Name
		r.Recorder.Eventf(session, corev1.EventTypeNormal, "ServiceCreated", "Created Service %s", service.Name)
	} else if err != nil {
		return fmt.Errorf("failed to get service: %w", err)
	} else {
		session.Status.ServiceName = found.Name
	}

	return nil
}

// checkPodReady checks if the pod is ready and returns its IP
func (r *LabSessionReconciler) checkPodReady(ctx context.Context, session *LabSession) (bool, string, error) {
	if session.Status.PodName == "" {
		return false, "", nil
	}

	pod := &corev1.Pod{}
	err := r.Get(ctx, types.NamespacedName{Name: session.Status.PodName, Namespace: session.Namespace}, pod)
	if err != nil {
		return false, "", err
	}

	// Check if pod is running
	if pod.Status.Phase != corev1.PodRunning {
		return false, "", nil
	}

	// Check if all containers are ready
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true, pod.Status.PodIP, nil
		}
	}

	return false, pod.Status.PodIP, nil
}

// updateEndpoints updates the service endpoints in the status
func (r *LabSessionReconciler) updateEndpoints(ctx context.Context, session *LabSession) error {
	if session.Status.ServiceName == "" {
		return nil
	}

	service := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: session.Status.ServiceName, Namespace: session.Namespace}, service)
	if err != nil {
		return err
	}

	endpoints := make(map[string]string)

	// For LoadBalancer services, get the external IP/hostname
	if service.Spec.Type == corev1.ServiceTypeLoadBalancer {
		if len(service.Status.LoadBalancer.Ingress) > 0 {
			ingress := service.Status.LoadBalancer.Ingress[0]
			host := ingress.IP
			if host == "" {
				host = ingress.Hostname
			}

			if host != "" {
				endpoints["vscode"] = fmt.Sprintf("http://%s:8080", host)
				endpoints["terminal"] = fmt.Sprintf("http://%s:8081", host)
				endpoints["ssh"] = fmt.Sprintf("ssh://%s:22", host)
			}
		}
	}

	session.Status.Endpoints = endpoints
	return nil
}

// updateStatusFailed updates the status to Failed
func (r *LabSessionReconciler) updateStatusFailed(ctx context.Context, session *LabSession, message string, err error) {
	logger := log.FromContext(ctx)

	session.Status.Phase = SessionPhaseFailed
	session.Status.Message = message
	if err != nil {
		session.Status.Reason = err.Error()
	}

	r.setCondition(session, ConditionTypeReady, metav1.ConditionFalse, "Failed", message)

	if updateErr := r.Status().Update(ctx, session); updateErr != nil {
		logger.Error(updateErr, "Failed to update status to Failed")
	}

	r.Recorder.Eventf(session, corev1.EventTypeWarning, "Failed", "%s: %v", message, err)
}

// setCondition sets a condition on the session
func (r *LabSessionReconciler) setCondition(session *LabSession, conditionType string, status metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	condition := metav1.Condition{
		Type:               conditionType,
		Status:             status,
		ObservedGeneration: session.Generation,
		LastTransitionTime: now,
		Reason:             reason,
		Message:            message,
	}

	// Find and update existing condition or append new one
	for i, c := range session.Status.Conditions {
		if c.Type == conditionType {
			if c.Status != status {
				session.Status.Conditions[i] = condition
			}
			return
		}
	}

	session.Status.Conditions = append(session.Status.Conditions, condition)
}

// SetupWithManager sets up the controller with the Manager
func (r *LabSessionReconciler) SetupWithManager(mgr ctrl.Manager, maxConcurrentReconciles int) error {
	// Initialize resource builder
	r.ResourceBuilder = NewResourceBuilder()

	return ctrl.NewControllerManagedBy(mgr).
		For(&LabSession{}).
		Owns(&corev1.Pod{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: maxConcurrentReconciles,
		}).
		Complete(r)
}

// Helper function to calculate VM IP from pod IP
func calculateVMIP(podIP string) string {
	parts := strings.Split(podIP, ".")
	if len(parts) == 4 {
		// Increment the last octet
		lastOctet := 0
		fmt.Sscanf(parts[3], "%d", &lastOctet)
		parts[3] = fmt.Sprintf("%d", lastOctet+1)
		return strings.Join(parts, ".")
	}
	return ""
}
