package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"dozlab-controller/internal/events"
)

// fakePublisher records phase changes and can fail.
type fakePublisher struct {
	published []events.PhaseChange
	err       error
}

func (f *fakePublisher) PublishPhaseChange(ctx context.Context, change events.PhaseChange) error {
	if f.err != nil {
		return f.err
	}
	f.published = append(f.published, change)
	return nil
}

func (f *fakePublisher) phases() []string {
	var out []string
	for _, c := range f.published {
		out = append(out, c.Phase)
	}
	return out
}

func newPhaseTestReconciler(t *testing.T, pub events.Publisher, objs ...*LabSession) *LabSessionReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	builder := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&LabSession{})
	for _, o := range objs {
		builder = builder.WithObjects(o)
	}
	r := &LabSessionReconciler{
		Client:   builder.Build(),
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(100),
	}
	if pub != nil {
		r.Events = pub
	}
	return r
}

func testSession(phase SessionPhase) *LabSession {
	return &LabSession{
		ObjectMeta: metav1.ObjectMeta{
			Name: "lab-session-demo", Namespace: "dozlab-labs", UID: "uid-1",
			Finalizers: []string{LabSessionFinalizer},
		},
		Spec:   LabSessionSpec{UserID: "user-1", SessionID: "session-1"},
		Status: LabSessionStatus{Phase: phase, Message: "Lab session failed", Reason: "pod is no longer ready"},
	}
}

var testRequest = ctrl.Request{NamespacedName: types.NamespacedName{Name: "lab-session-demo", Namespace: "dozlab-labs"}}

func (r *LabSessionReconciler) getSession(t *testing.T) *LabSession {
	t.Helper()
	s := &LabSession{}
	if err := r.Get(context.Background(), testRequest.NamespacedName, s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestReconcilePublishesPhaseOnce(t *testing.T) {
	pub := &fakePublisher{}
	r := newPhaseTestReconciler(t, pub, testSession(SessionPhaseFailed))

	for i := 0; i < 2; i++ {
		if _, err := r.Reconcile(context.Background(), testRequest); err != nil {
			t.Fatalf("Reconcile %d: %v", i, err)
		}
	}

	if got := pub.phases(); len(got) != 1 || got[0] != "Failed" {
		t.Fatalf("published %v, want [Failed] once", got)
	}
	c := pub.published[0]
	if c.UID != "uid-1" || c.UserID != "user-1" || c.SessionID != "session-1" ||
		c.Name != "lab-session-demo" || c.Namespace != "dozlab-labs" || c.Reason != "pod is no longer ready" {
		t.Errorf("unexpected phase change: %+v", c)
	}
	if got := r.getSession(t).Annotations[PublishedPhaseAnnotation]; got != "Failed" {
		t.Errorf("annotation = %q, want Failed", got)
	}
}

// Each stored phase is published as the state machine moves on.
func TestReconcilePublishesEachStoredPhase(t *testing.T) {
	pub := &fakePublisher{}
	r := newPhaseTestReconciler(t, pub, testSession(SessionPhasePending))

	// Pending is published, then the state machine moves the session to Creating;
	// the next reconcile publishes Creating.
	if _, err := r.Reconcile(context.Background(), testRequest); err != nil {
		t.Fatal(err)
	}
	if got := r.getSession(t).Status.Phase; got != SessionPhaseCreating {
		t.Fatalf("phase = %s, want Creating", got)
	}
	// Creating then tries to create PVCs (fine on the fake client) and waits for them to bind.
	if _, err := r.Reconcile(context.Background(), testRequest); err != nil {
		t.Fatal(err)
	}
	if got := pub.phases(); len(got) != 2 || got[0] != "Pending" || got[1] != "Creating" {
		t.Errorf("published %v, want [Pending Creating]", got)
	}
}

func TestReconcileRetriesFailedPublishWithoutBlocking(t *testing.T) {
	pub := &fakePublisher{err: errors.New("broker down")}
	r := newPhaseTestReconciler(t, pub, testSession(SessionPhasePending))

	result, err := r.Reconcile(context.Background(), testRequest)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	s := r.getSession(t)
	if s.Status.Phase != SessionPhaseCreating {
		t.Errorf("state machine blocked: phase = %s, want Creating", s.Status.Phase)
	}
	if _, ok := s.Annotations[PublishedPhaseAnnotation]; ok {
		t.Error("annotation set although publishing failed")
	}
	if !result.Requeue && (result.RequeueAfter == 0 || result.RequeueAfter > publishRetryDelay) {
		t.Errorf("result = %+v, want a requeue within %s", result, publishRetryDelay)
	}

	// Once the broker is back, the stored phase is published.
	pub.err = nil
	if _, err := r.Reconcile(context.Background(), testRequest); err != nil {
		t.Fatal(err)
	}
	if got := pub.phases(); len(got) != 1 || got[0] != "Creating" {
		t.Errorf("published %v, want [Creating]", got)
	}
}

func TestReconcileWithoutPublisherPublishesNothing(t *testing.T) {
	r := newPhaseTestReconciler(t, nil, testSession(SessionPhaseFailed))
	if _, err := r.Reconcile(context.Background(), testRequest); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.getSession(t).Annotations[PublishedPhaseAnnotation]; ok {
		t.Error("annotation set without a publisher")
	}
}

func TestReconcileDeletePublishesTerminatingBestEffort(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{{"broker up", nil}, {"broker down", errors.New("broker down")}} {
		t.Run(tc.name, func(t *testing.T) {
			pub := &fakePublisher{err: tc.err}
			r := newPhaseTestReconciler(t, pub, testSession(SessionPhaseRunning))
			if err := r.Delete(context.Background(), r.getSession(t)); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := r.Reconcile(ctx, testRequest); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			// The finalizer is removed (and the object gone) either way
			if err := r.Get(ctx, testRequest.NamespacedName, &LabSession{}); err == nil {
				t.Error("session still exists; deletion was blocked")
			}
			if tc.err == nil {
				if got := pub.phases(); len(got) != 1 || got[0] != "Terminating" {
					t.Errorf("published %v, want [Terminating]", got)
				}
			}
		})
	}
}
