package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// A reconcile that runs before the cache has seen the objects an earlier
// reconcile created gets NotFound, then AlreadyExists on create. That must not
// fail the session.
func TestReconcileCreatingToleratesStaleCache(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	session := testSession(SessionPhaseCreating)
	rb := NewResourceBuilder(testSettings)
	objs := []client.Object{session, rb.BuildPod(session), rb.BuildService(session)}
	for _, pvc := range rb.BuildPVCs(session) {
		objs = append(objs, pvc)
	}

	// The first Get of each pod, service and PVC misses, like a stale cache.
	seen := map[client.ObjectKey]bool{}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&LabSession{}).
		WithObjects(objs...).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				switch obj.(type) {
				case *corev1.Pod, *corev1.Service, *corev1.PersistentVolumeClaim:
					if !seen[key] {
						seen[key] = true
						return apierrors.NewNotFound(corev1.Resource("object"), key.Name)
					}
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).Build()

	r := &LabSessionReconciler{
		Client:          c,
		Scheme:          scheme,
		Recorder:        record.NewFakeRecorder(100),
		ResourceBuilder: rb,
	}
	if _, err := r.Reconcile(context.Background(), testRequest); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := r.getSession(t).Status.Phase; got != SessionPhaseCreating {
		t.Errorf("phase = %s, want Creating", got)
	}
}
