package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func newFailureTestReconciler(t *testing.T, funcs *interceptor.Funcs, objs ...client.Object) *LabSessionReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	builder := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&LabSession{}).WithObjects(objs...)
	if funcs != nil {
		builder = builder.WithInterceptorFuncs(*funcs)
	}
	return &LabSessionReconciler{
		Client:          builder.Build(),
		Scheme:          scheme,
		Recorder:        record.NewFakeRecorder(100),
		ResourceBuilder: NewResourceBuilder(testSettings),
	}
}

func creatingSession(started time.Time) *LabSession {
	s := testSession(SessionPhaseCreating)
	s.Status.Message, s.Status.Reason = "", ""
	s.Status.StartTime = &metav1.Time{Time: started}
	return s
}

// failServiceCreate makes every Service create fail with err.
func failServiceCreate(err error) *interceptor.Funcs {
	return &interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*corev1.Service); ok {
				return err
			}
			return cl.Create(ctx, obj, opts...)
		},
	}
}

func TestCreateTransientErrorIsRetried(t *testing.T) {
	r := newFailureTestReconciler(t, failServiceCreate(apierrors.NewServiceUnavailable("apiserver busy")),
		creatingSession(time.Now()))
	if _, err := r.Reconcile(context.Background(), testRequest); err == nil {
		t.Error("Reconcile returned nil, want the error so the create is retried")
	}
	if got := r.getSession(t).Status.Phase; got != SessionPhaseCreating {
		t.Errorf("phase = %s, want Creating", got)
	}
}

func TestCreateTransientErrorFailsAfterTimeout(t *testing.T) {
	r := newFailureTestReconciler(t, failServiceCreate(apierrors.NewServiceUnavailable("apiserver busy")),
		creatingSession(time.Now().Add(-creatingTimeout-time.Minute)))
	if _, err := r.Reconcile(context.Background(), testRequest); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := r.getSession(t).Status.Phase; got != SessionPhaseFailed {
		t.Errorf("phase = %s, want Failed", got)
	}
}

func TestCreatePermanentErrorFails(t *testing.T) {
	r := newFailureTestReconciler(t, failServiceCreate(apierrors.NewBadRequest("bad spec")),
		creatingSession(time.Now()))
	if _, err := r.Reconcile(context.Background(), testRequest); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := r.getSession(t).Status.Phase; got != SessionPhaseFailed {
		t.Errorf("phase = %s, want Failed", got)
	}
}

func TestExistingObjectNotOwnedFails(t *testing.T) {
	session := creatingSession(time.Now())
	foreign := NewResourceBuilder(testSettings).BuildService(session) // same name, no owner
	r := newFailureTestReconciler(t, nil, session, foreign)
	if _, err := r.Reconcile(context.Background(), testRequest); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	s := r.getSession(t)
	if s.Status.Phase != SessionPhaseFailed || !strings.Contains(s.Status.Reason, "not owned") {
		t.Errorf("phase = %s, reason = %q; want Failed, not owned", s.Status.Phase, s.Status.Reason)
	}
}

// runningSession returns a Running session and its pod, owned by the session,
// with the given pod status.
func runningSession(t *testing.T, podStatus corev1.PodStatus, readySince time.Time, ready metav1.ConditionStatus) (*LabSession, *corev1.Pod) {
	t.Helper()
	s := testSession(SessionPhaseRunning)
	s.Status.Message, s.Status.Reason = "Lab session is running", ""
	pod := NewResourceBuilder(testSettings).BuildPod(s)
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = AddToScheme(scheme)
	if err := controllerutil.SetControllerReference(s, pod, scheme); err != nil {
		t.Fatal(err)
	}
	pod.Status = podStatus
	s.Status.PodName = pod.Name
	s.Status.Conditions = []metav1.Condition{{
		Type: ConditionTypePodReady, Status: ready, Reason: "Test",
		LastTransitionTime: metav1.Time{Time: readySince},
	}}
	return s, pod
}

var (
	podReadyStatus = corev1.PodStatus{
		Phase:      corev1.PodRunning,
		Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
	}
	podNotReadyStatus = corev1.PodStatus{
		Phase:      corev1.PodRunning,
		Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
	}
	podExitedStatus = corev1.PodStatus{
		Phase: corev1.PodRunning,
		ContainerStatuses: []corev1.ContainerStatus{{
			Name:  "firecracker-vm",
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}},
		}},
	}
)

func TestRunningPodHealth(t *testing.T) {
	tests := []struct {
		name      string
		status    corev1.PodStatus
		since     time.Time
		ready     metav1.ConditionStatus
		noPod     bool
		wantPhase SessionPhase
		wantReady metav1.ConditionStatus
	}{
		{"ready pod stays running", podReadyStatus, time.Now(), metav1.ConditionTrue, false, SessionPhaseRunning, metav1.ConditionTrue},
		{"not-ready blip keeps running", podNotReadyStatus, time.Now(), metav1.ConditionTrue, false, SessionPhaseRunning, metav1.ConditionFalse},
		{"recovered pod is ready again", podReadyStatus, time.Now(), metav1.ConditionFalse, false, SessionPhaseRunning, metav1.ConditionTrue},
		{"not ready past timeout fails", podNotReadyStatus, time.Now().Add(-podUnreadyTimeout - time.Minute), metav1.ConditionFalse, false, SessionPhaseFailed, ""},
		{"exited container fails", podExitedStatus, time.Now(), metav1.ConditionTrue, false, SessionPhaseFailed, ""},
		{"deleted pod fails", podReadyStatus, time.Now(), metav1.ConditionTrue, true, SessionPhaseFailed, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, pod := runningSession(t, tt.status, tt.since, tt.ready)
			objs := []client.Object{s}
			if !tt.noPod {
				objs = append(objs, pod)
			}
			r := newFailureTestReconciler(t, nil, objs...)
			if _, err := r.Reconcile(context.Background(), testRequest); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			got := r.getSession(t)
			if got.Status.Phase != tt.wantPhase {
				t.Fatalf("phase = %s (%s), want %s", got.Status.Phase, got.Status.Reason, tt.wantPhase)
			}
			if tt.wantReady != "" {
				if c := meta.FindStatusCondition(got.Status.Conditions, ConditionTypePodReady); c == nil || c.Status != tt.wantReady {
					t.Errorf("PodReady condition = %+v, want %s", c, tt.wantReady)
				}
			}
		})
	}
}

// A queued delete reconcile that sees the session from a stale cache, after it
// is already gone, must not report an error.
func TestReconcileDeleteAlreadyGone(t *testing.T) {
	gone := func() error { return apierrors.NewNotFound(corev1.Resource("labsessions"), "lab-session-demo") }
	r := newFailureTestReconciler(t, &interceptor.Funcs{
		Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			return gone()
		},
		SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			return gone()
		},
	}, testSession(SessionPhaseRunning))
	if err := r.Delete(context.Background(), r.getSession(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), testRequest); err != nil {
		t.Errorf("Reconcile: %v, want nil for an already-deleted session", err)
	}
}

func TestCreatingFailsWhenPodFails(t *testing.T) {
	session := creatingSession(time.Now())
	pod := NewResourceBuilder(testSettings).BuildPod(session)
	r := newFailureTestReconciler(t, nil, session)
	if err := controllerutil.SetControllerReference(session, pod, r.Scheme); err != nil {
		t.Fatal(err)
	}
	pod.Status.Phase = corev1.PodFailed // e.g. init-rootfs exited non-zero
	if err := r.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), testRequest); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := r.getSession(t).Status.Phase; got != SessionPhaseFailed {
		t.Errorf("phase = %s, want Failed", got)
	}
}
