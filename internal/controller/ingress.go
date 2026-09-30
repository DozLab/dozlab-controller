package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// The apps a session's Ingress routes to, by path, with their Service port.
// See dozlab-api docs/decision.md, "frontend on GitHub Pages".
var ingressApps = []struct {
	name string // path segment and Service port name
	port int32
}{
	{"vscode", 8080},
	{"terminal", 8081},
}

// traefikMiddlewareAnnotation attaches Traefik Middlewares to the Ingress's routers.
const traefikMiddlewareAnnotation = "traefik.ingress.kubernetes.io/router.middlewares"

// SessionPath is where an app of a session is served: /sessions/<id>/<app>.
// The app itself serves from /; the Middleware strips this prefix.
func SessionPath(sessionID, app string) string {
	return fmt.Sprintf("/sessions/%s/%s", sessionID, app)
}

// BuildIngress routes /sessions/<id>/vscode and /sessions/<id>/terminal to the session's Service.
func (rb *ResourceBuilder) BuildIngress(session *LabSession) *networkingv1.Ingress {
	sessionID := session.Spec.SessionID
	service := rb.BuildService(session)
	pathType := networkingv1.PathTypePrefix

	var paths []networkingv1.HTTPIngressPath
	for _, app := range ingressApps {
		paths = append(paths, networkingv1.HTTPIngressPath{
			Path:     SessionPath(sessionID, app.name),
			PathType: &pathType,
			Backend: networkingv1.IngressBackend{
				Service: &networkingv1.IngressServiceBackend{
					Name: service.Name,
					Port: networkingv1.ServiceBackendPort{Number: app.port},
				},
			},
		})
	}

	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("lab-ingress-%s", sessionID),
			Namespace: session.Namespace,
			Labels: map[string]string{
				"app":        "lab-environment",
				"session-id": sessionID,
				"user-id":    session.Spec.UserID,
			},
		},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{{
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{Paths: paths},
				},
			}},
		},
	}
	if rb.settings.IngressClass != "" {
		ingress.Spec.IngressClassName = &rb.settings.IngressClass
	}
	if rb.settings.IngressMiddleware != "" {
		ingress.Annotations = map[string]string{traefikMiddlewareAnnotation: rb.settings.IngressMiddleware}
	}
	return ingress
}

// SessionEndpoints returns the public URL of each app of a session, on PublicBaseURL.
// The trailing slash matters: code-server's relative links resolve against it.
func (rb *ResourceBuilder) SessionEndpoints(sessionID string) map[string]string {
	base := strings.TrimRight(rb.settings.PublicBaseURL, "/")
	endpoints := make(map[string]string, len(ingressApps))
	for _, app := range ingressApps {
		endpoints[app.name] = base + SessionPath(sessionID, app.name) + "/"
	}
	return endpoints
}

// ensureIngress creates the session's Ingress if it doesn't exist
func (r *LabSessionReconciler) ensureIngress(ctx context.Context, session *LabSession) error {
	logger := log.FromContext(ctx)
	ingress := r.ResourceBuilder.BuildIngress(session)

	if err := controllerutil.SetControllerReference(session, ingress, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on ingress: %w", err)
	}

	found := &networkingv1.Ingress{}
	err := r.Get(ctx, types.NamespacedName{Name: ingress.Name, Namespace: ingress.Namespace}, found)
	if err != nil && errors.IsNotFound(err) {
		logger.Info("Creating Ingress", "name", ingress.Name)
		if err := r.Create(ctx, ingress); errors.IsAlreadyExists(err) {
			// The cache hadn't seen our earlier create yet.
			logger.Info("Ingress already exists", "name", ingress.Name)
		} else if err != nil {
			return fmt.Errorf("failed to create ingress: %w", err)
		} else {
			r.Recorder.Eventf(session, corev1.EventTypeNormal, "IngressCreated", "Created Ingress %s", ingress.Name)
		}
	} else if err != nil {
		return fmt.Errorf("failed to get ingress: %w", err)
	} else if !metav1.IsControlledBy(found, session) {
		return &notOwnedError{kind: "Ingress", name: ingress.Name}
	}
	return nil
}
