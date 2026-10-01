package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
)

// usageOf reports what a session holds: the CPU and memory its pod's containers reserve while
// the pod runs, and the storage of its volume claims. pod is nil when there is none.
func usageOf(pod *corev1.Pod, pvcs []*corev1.PersistentVolumeClaim) LabSessionUsage {
	var usage LabSessionUsage

	storage := resource.NewQuantity(0, resource.BinarySI)
	for _, pvc := range pvcs {
		storage.Add(pvc.Spec.Resources.Requests[corev1.ResourceStorage])
	}
	if !storage.IsZero() {
		usage.Storage = storage.String()
	}

	if !podHoldsResources(pod) {
		return usage
	}
	cpu := resource.NewMilliQuantity(0, resource.DecimalSI)
	memory := resource.NewQuantity(0, resource.BinarySI)
	for _, c := range pod.Spec.Containers {
		cpu.Add(c.Resources.Requests[corev1.ResourceCPU])
		memory.Add(c.Resources.Requests[corev1.ResourceMemory])
	}
	usage.Running = true
	usage.CPURequest = cpu.String()
	usage.MemoryRequest = memory.String()
	return usage
}

// A pod holds its requests from when it is created until it has finished or is being deleted.
func podHoldsResources(pod *corev1.Pod) bool {
	if pod == nil || pod.DeletionTimestamp != nil {
		return false
	}
	return pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed
}

// sessionPod returns the session's pod, or nil if it has none or it can't be read.
func (r *LabSessionReconciler) sessionPod(ctx context.Context, session *LabSession) *corev1.Pod {
	if session.Status.PodName == "" {
		return nil
	}
	pod := &corev1.Pod{}
	if err := r.Get(ctx, types.NamespacedName{Name: session.Status.PodName, Namespace: session.Namespace}, pod); err != nil {
		return nil
	}
	return pod
}
