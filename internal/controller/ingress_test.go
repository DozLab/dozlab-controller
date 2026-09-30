package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestBuildIngress(t *testing.T) {
	session := newSession(LabSessionSpec{UserID: "u1", SessionID: "s1"})
	settings := testSettings
	settings.IngressClass = "traefik"
	settings.IngressMiddleware = "labs-dozlab-strip-session-prefix@kubernetescrd"
	rb := NewResourceBuilder(settings)
	ing := rb.BuildIngress(session)
	svc := rb.BuildService(session)

	if ing.Name != "lab-ingress-s1" || ing.Namespace != "labs" {
		t.Errorf("ingress = %s/%s, want labs/lab-ingress-s1", ing.Namespace, ing.Name)
	}
	if ing.Spec.IngressClassName == nil || *ing.Spec.IngressClassName != "traefik" {
		t.Errorf("ingressClassName = %v, want traefik", ing.Spec.IngressClassName)
	}
	if got := ing.Annotations[traefikMiddlewareAnnotation]; got != settings.IngressMiddleware {
		t.Errorf("middleware annotation = %q, want %q", got, settings.IngressMiddleware)
	}
	if len(ing.Spec.Rules) != 1 || ing.Spec.Rules[0].Host != "" {
		t.Fatalf("want one rule with no host, got %+v", ing.Spec.Rules)
	}

	// Each path goes to the session Service, on a port the Service exposes.
	svcPorts := map[int32]string{}
	for _, p := range svc.Spec.Ports {
		svcPorts[p.Port] = p.Name
	}
	want := map[string]int32{"/sessions/s1/vscode": 8080, "/sessions/s1/terminal": 8081}
	paths := ing.Spec.Rules[0].HTTP.Paths
	if len(paths) != len(want) {
		t.Fatalf("got %d paths, want %d", len(paths), len(want))
	}
	for _, p := range paths {
		port, ok := want[p.Path]
		if !ok {
			t.Errorf("unexpected path %q", p.Path)
			continue
		}
		if p.PathType == nil || *p.PathType != networkingv1.PathTypePrefix {
			t.Errorf("%s: pathType = %v, want Prefix", p.Path, p.PathType)
		}
		b := p.Backend.Service
		if b == nil || b.Name != svc.Name || b.Port.Number != port {
			t.Errorf("%s: backend = %+v, want %s:%d", p.Path, b, svc.Name, port)
		}
		if _, ok := svcPorts[port]; !ok {
			t.Errorf("%s: port %d is not on the Service", p.Path, port)
		}
	}
}

func TestBuildIngressDefaults(t *testing.T) {
	ing := NewResourceBuilder(testSettings).BuildIngress(newSession(LabSessionSpec{SessionID: "s1"}))
	if ing.Spec.IngressClassName != nil {
		t.Errorf("ingressClassName = %q, want unset", *ing.Spec.IngressClassName)
	}
	if _, ok := ing.Annotations[traefikMiddlewareAnnotation]; ok {
		t.Error("middleware annotation set without IngressMiddleware")
	}
}

func TestSessionEndpoints(t *testing.T) {
	tests := []struct {
		base, wantVSCode, wantTerminal string
	}{
		{"", "/sessions/s1/vscode/", "/sessions/s1/terminal/"},
		{"https://lab.example.ts.net", "https://lab.example.ts.net/sessions/s1/vscode/", "https://lab.example.ts.net/sessions/s1/terminal/"},
		{"https://lab.example.ts.net/", "https://lab.example.ts.net/sessions/s1/vscode/", "https://lab.example.ts.net/sessions/s1/terminal/"},
	}
	for _, tt := range tests {
		settings := testSettings
		settings.PublicBaseURL = tt.base
		got := NewResourceBuilder(settings).SessionEndpoints("s1")
		if got["vscode"] != tt.wantVSCode || got["terminal"] != tt.wantTerminal {
			t.Errorf("base %q: endpoints = %v, want vscode %q, terminal %q", tt.base, got, tt.wantVSCode, tt.wantTerminal)
		}
	}
}

func TestReconcileCreatesOwnedIngress(t *testing.T) {
	session := creatingSession(time.Now())
	r := newFailureTestReconciler(t, nil, session)
	if _, err := r.Reconcile(context.Background(), testRequest); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	ing := &networkingv1.Ingress{}
	key := types.NamespacedName{Namespace: session.Namespace, Name: "lab-ingress-" + session.Spec.SessionID}
	if err := r.Get(context.Background(), key, ing); err != nil {
		t.Fatalf("get ingress: %v", err)
	}
	if !metav1.IsControlledBy(ing, r.getSession(t)) {
		t.Error("ingress is not controlled by the session, so it won't be deleted with it")
	}
}

func TestExistingIngressNotOwnedFails(t *testing.T) {
	session := creatingSession(time.Now())
	foreign := NewResourceBuilder(testSettings).BuildIngress(session) // same name, no owner
	r := newFailureTestReconciler(t, nil, session, foreign)
	if _, err := r.Reconcile(context.Background(), testRequest); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	s := r.getSession(t)
	if s.Status.Phase != SessionPhaseFailed || !strings.Contains(s.Status.Reason, "not owned") {
		t.Errorf("phase = %s, reason = %q; want Failed, not owned", s.Status.Phase, s.Status.Reason)
	}
}

func TestUpdateEndpoints(t *testing.T) {
	session := testSession(SessionPhaseCreating)
	settings := testSettings
	settings.PublicBaseURL = "https://lab.example.ts.net"

	svc := NewResourceBuilder(settings).BuildService(session)
	svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "192.168.1.91"}}
	r := newFailureTestReconciler(t, nil, session, svc)
	r.ResourceBuilder = NewResourceBuilder(settings)
	session.Status.ServiceName = svc.Name

	if err := r.updateEndpoints(context.Background(), session); err != nil {
		t.Fatalf("updateEndpoints: %v", err)
	}
	id := session.Spec.SessionID
	want := map[string]string{
		"vscode":   "https://lab.example.ts.net/sessions/" + id + "/vscode/",
		"terminal": "https://lab.example.ts.net/sessions/" + id + "/terminal/",
		"ssh":      "ssh://192.168.1.91:22",
	}
	for k, v := range want {
		if session.Status.Endpoints[k] != v {
			t.Errorf("endpoint %s = %q, want %q", k, session.Status.Endpoints[k], v)
		}
	}
}
