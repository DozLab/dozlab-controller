package controllers

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	labcontroller "dozlab-controller/internal/controller"
)

func TestBuildPodDelegatesToSharedBuilder(t *testing.T) {
	r := &LabSessionReconciler{
		ResourceBuilder: labcontroller.NewResourceBuilder(labcontroller.PodSettings{
			VMImage:       "dozlab-firecracker:test",
			InitImage:     "dozlab-init:test",
			TerminalImage: "dozlab-terminal:test",
			SSHUser:       "root",
			VMDiskSize:    "4Gi",
		}),
	}
	labSession := &unstructured.Unstructured{}
	labSession.SetNamespace("labs")

	tests := []struct {
		name         string
		spec         map[string]interface{}
		wantErr      bool
		wantMemLimit string // the VM's memory (the legacy caps apply) plus 128Mi for Firecracker
		wantPassword string
	}{
		{
			name:         "defaults",
			spec:         map[string]interface{}{"userId": "u1", "sessionId": "s1"},
			wantMemLimit: DefaultMemoryLimit,
			wantPassword: "password123",
		},
		{
			name: "legacy caps apply",
			spec: map[string]interface{}{
				"userId":    "u1",
				"sessionId": "s1",
				"resources": map[string]interface{}{"memory": "65536Mi"},
				"config":    map[string]interface{}{"vsCodePassword": "secret"},
			},
			wantMemLimit: MaxMemoryLimit,
			wantPassword: "secret",
		},
		{
			name:    "malformed spec",
			spec:    map[string]interface{}{"userId": "u1", "sessionId": "s1", "resources": "lots"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod, err := r.buildPod(labSession, tt.spec)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("buildPod: %v", err)
			}
			if pod.Name != "lab-session-s1" || pod.Namespace != "labs" {
				t.Errorf("pod = %s/%s, want labs/lab-session-s1", pod.Namespace, pod.Name)
			}

			var vm, code *corev1.Container
			for i := range pod.Spec.Containers {
				switch pod.Spec.Containers[i].Name {
				case "firecracker-vm":
					vm = &pod.Spec.Containers[i]
				case "code-server":
					code = &pod.Spec.Containers[i]
				}
			}
			if vm == nil || code == nil {
				t.Fatalf("missing containers: firecracker-vm=%v code-server=%v", vm != nil, code != nil)
			}
			if vm.Image != "dozlab-firecracker:test" {
				t.Errorf("vm image = %q", vm.Image)
			}
			// The legacy caps set the VM's memory; the container adds 128Mi for Firecracker.
			want := resource.MustParse(tt.wantMemLimit)
			want.Add(resource.MustParse("128Mi"))
			if got := vm.Resources.Limits[corev1.ResourceMemory]; got.Cmp(want) != 0 {
				t.Errorf("vm memory limit = %s, want %s", got.String(), want.String())
			}
			for _, e := range code.Env {
				if e.Name == "PASSWORD" && e.Value != tt.wantPassword {
					t.Errorf("PASSWORD = %q, want %q", e.Value, tt.wantPassword)
				}
			}
		})
	}
}
