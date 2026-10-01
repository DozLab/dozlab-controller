package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestUsageOf(t *testing.T) {
	rb := NewResourceBuilder(testSettings)
	sized := newSession(LabSessionSpec{UserID: "u1", SessionID: "demo",
		Resources: ResourceConfig{CPU: "2", Memory: "2048Mi", Storage: "4Gi"}})
	unsized := newSession(LabSessionSpec{UserID: "u1", SessionID: "demo"})

	podIn := func(s *LabSession, phase corev1.PodPhase, deleting bool) *corev1.Pod {
		pod := rb.BuildPod(s)
		pod.Status.Phase = phase
		if deleting {
			now := metav1.Now()
			pod.DeletionTimestamp = &now
		}
		return pod
	}

	tests := []struct {
		name    string
		session *LabSession
		pod     *corev1.Pod
		want    LabSessionUsage
	}{
		{
			// VM 100m + terminal 250m + code-server 500m; (2048+128) + 256 + 1024 Mi;
			// vm-data 4Gi + vscode-data 5Gi
			name: "running pod, sized by the lab", session: sized, pod: podIn(sized, corev1.PodRunning, false),
			want: LabSessionUsage{Running: true, CPURequest: "850m", MemoryRequest: "3456Mi", Storage: "9Gi"},
		},
		{
			// the controller's default VM: 1024 Mi; vm-data defaults to 10Gi
			name: "running pod, no size in the spec", session: unsized, pod: podIn(unsized, corev1.PodRunning, false),
			want: LabSessionUsage{Running: true, CPURequest: "850m", MemoryRequest: "2432Mi", Storage: "15Gi"},
		},
		{
			name: "a pod that hasn't started yet holds its requests", session: sized, pod: podIn(sized, corev1.PodPending, false),
			want: LabSessionUsage{Running: true, CPURequest: "850m", MemoryRequest: "3456Mi", Storage: "9Gi"},
		},
		{
			name: "no pod: only the storage", session: sized, pod: nil,
			want: LabSessionUsage{Storage: "9Gi"},
		},
		{
			name: "a failed pod holds nothing", session: sized, pod: podIn(sized, corev1.PodFailed, false),
			want: LabSessionUsage{Storage: "9Gi"},
		},
		{
			name: "a finished pod holds nothing", session: sized, pod: podIn(sized, corev1.PodSucceeded, false),
			want: LabSessionUsage{Storage: "9Gi"},
		},
		{
			name: "a pod being deleted holds nothing", session: sized, pod: podIn(sized, corev1.PodRunning, true),
			want: LabSessionUsage{Storage: "9Gi"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := usageOf(tt.pod, rb.BuildPVCs(tt.session)); got != tt.want {
				t.Errorf("usage = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// The usage in the status follows what is really there when a session fails.
func TestFailedSessionUsage(t *testing.T) {
	tests := []struct {
		name   string
		status corev1.PodStatus
		since  time.Time
		noPod  bool
		want   LabSessionUsage
	}{
		{
			name:   "not ready for too long: the pod is still there and still holds its requests",
			status: podNotReadyStatus, since: time.Now().Add(-podUnreadyTimeout - time.Minute),
			want: LabSessionUsage{Running: true, CPURequest: "850m", MemoryRequest: "2432Mi", Storage: "15Gi"},
		},
		{
			name:   "the pod was deleted: only the storage is left",
			status: podReadyStatus, since: time.Now(), noPod: true,
			want: LabSessionUsage{Storage: "15Gi"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, pod := runningSession(t, tt.status, tt.since, metav1.ConditionFalse)
			if tt.noPod {
				s.Status.Conditions[0].Status = metav1.ConditionTrue
			}
			objs := []client.Object{s}
			if !tt.noPod {
				objs = append(objs, pod)
			}
			r := newFailureTestReconciler(t, nil, objs...)
			if _, err := r.Reconcile(context.Background(), testRequest); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			got := r.getSession(t)
			if got.Status.Phase != SessionPhaseFailed {
				t.Fatalf("phase = %s, want Failed", got.Status.Phase)
			}
			if got.Status.Usage != tt.want {
				t.Errorf("usage = %+v, want %+v", got.Status.Usage, tt.want)
			}
		})
	}
}

// A session that becomes Running reports what its pod reserves.
func TestRunningSessionReportsUsage(t *testing.T) {
	// A ready pod owned by the session, as runningSession builds it; the session is still Creating
	s, pod := runningSession(t, podReadyStatus, time.Now(), metav1.ConditionFalse)
	s.Status.Phase, s.Status.Conditions = SessionPhaseCreating, nil
	s.Status.StartTime = &metav1.Time{Time: time.Now()}
	pod.Status.PodIP = "10.42.0.9"
	r := newFailureTestReconciler(t, nil, s, pod)

	for i := 0; i < 3 && r.getSession(t).Status.Phase != SessionPhaseRunning; i++ {
		if _, err := r.Reconcile(context.Background(), testRequest); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}
	got := r.getSession(t)
	if got.Status.Phase != SessionPhaseRunning {
		t.Fatalf("phase = %s (%s), want Running", got.Status.Phase, got.Status.Reason)
	}
	want := LabSessionUsage{Running: true, CPURequest: "850m", MemoryRequest: "2432Mi", Storage: "15Gi"}
	if got.Status.Usage != want {
		t.Errorf("usage = %+v, want %+v", got.Status.Usage, want)
	}
}
