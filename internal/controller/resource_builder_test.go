package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func newSession(spec LabSessionSpec) *LabSession {
	s := &LabSession{Spec: spec}
	s.Namespace = "labs"
	return s
}

func findContainer(t *testing.T, containers []corev1.Container, name string) corev1.Container {
	t.Helper()
	for _, c := range containers {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("container %q not found", name)
	return corev1.Container{}
}

func envValue(c corev1.Container, name string) (string, bool) {
	for _, e := range c.Env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

func TestBuildPod(t *testing.T) {
	tests := []struct {
		name          string
		spec          LabSessionSpec
		wantVMImage   string
		wantTermImage string
		wantCodeImage string
		wantPassword  string
		wantMemLimit  string
		wantCPULimit  string
		wantMemReq    string
		wantCPUReq    string
	}{
		{
			name:          "defaults",
			spec:          LabSessionSpec{UserID: "u1", SessionID: "s1"},
			wantVMImage:   "your-initrd:latest",
			wantTermImage: "your-terminal-sidecar:latest",
			wantCodeImage: "codercom/code-server:latest",
			wantPassword:  "changeme",
			wantMemLimit:  "4Gi",
			wantCPULimit:  "2",
			wantMemReq:    "3Gi",
			wantCPUReq:    "1",
		},
		{
			name: "custom images, password and resources",
			spec: LabSessionSpec{
				UserID:    "u2",
				SessionID: "s2",
				Resources: ResourceConfig{Memory: "8Gi", CPU: "4"},
				Config:    SessionConfig{VSCodePassword: "secret"},
				CustomImages: ImageConfig{
					InitrdImage:   "vm:1",
					TerminalImage: "term:1",
					VSCodeImage:   "code:1",
				},
			},
			wantVMImage:   "vm:1",
			wantTermImage: "term:1",
			wantCodeImage: "code:1",
			wantPassword:  "secret",
			wantMemLimit:  "8Gi",
			wantCPULimit:  "4",
			wantMemReq:    "6Gi",
			wantCPUReq:    "2",
		},
		{
			name: "resources above maximum are capped",
			spec: LabSessionSpec{
				UserID:    "u3",
				SessionID: "s3",
				Resources: ResourceConfig{Memory: "64Gi", CPU: "32"},
			},
			wantVMImage:   "your-initrd:latest",
			wantTermImage: "your-terminal-sidecar:latest",
			wantCodeImage: "codercom/code-server:latest",
			wantPassword:  "changeme",
			wantMemLimit:  "16Gi",
			wantCPULimit:  "8",
			wantMemReq:    "12Gi",
			wantCPUReq:    "4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := newSession(tt.spec)
			pod := NewResourceBuilder().BuildPod(session)

			if want := "lab-session-" + tt.spec.SessionID; pod.Name != want {
				t.Errorf("pod name = %q, want %q", pod.Name, want)
			}
			if pod.Namespace != "labs" {
				t.Errorf("namespace = %q, want %q", pod.Namespace, "labs")
			}
			wantLabels := map[string]string{
				"app":        "lab-environment",
				"session-id": tt.spec.SessionID,
				"user-id":    tt.spec.UserID,
			}
			for k, v := range wantLabels {
				if pod.Labels[k] != v {
					t.Errorf("label %s = %q, want %q", k, pod.Labels[k], v)
				}
			}
			if pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
				t.Errorf("restart policy = %q, want Never", pod.Spec.RestartPolicy)
			}
			if len(pod.Spec.InitContainers) != 1 || pod.Spec.InitContainers[0].Name != "ip-calculator" {
				t.Errorf("init containers = %v, want [ip-calculator]", pod.Spec.InitContainers)
			}
			if len(pod.Spec.Containers) != 3 {
				t.Fatalf("got %d containers, want 3", len(pod.Spec.Containers))
			}

			vm := findContainer(t, pod.Spec.Containers, "initrd-vm")
			if vm.Image != tt.wantVMImage {
				t.Errorf("vm image = %q, want %q", vm.Image, tt.wantVMImage)
			}
			checkQuantity(t, "vm memory limit", vm.Resources.Limits[corev1.ResourceMemory], tt.wantMemLimit)
			checkQuantity(t, "vm cpu limit", vm.Resources.Limits[corev1.ResourceCPU], tt.wantCPULimit)
			checkQuantity(t, "vm memory request", vm.Resources.Requests[corev1.ResourceMemory], tt.wantMemReq)
			checkQuantity(t, "vm cpu request", vm.Resources.Requests[corev1.ResourceCPU], tt.wantCPUReq)
			if v, _ := envValue(vm, "SESSION_ID"); v != tt.spec.SessionID {
				t.Errorf("vm SESSION_ID = %q, want %q", v, tt.spec.SessionID)
			}

			term := findContainer(t, pod.Spec.Containers, "terminal-sidecar")
			if term.Image != tt.wantTermImage {
				t.Errorf("terminal image = %q, want %q", term.Image, tt.wantTermImage)
			}

			code := findContainer(t, pod.Spec.Containers, "code-server")
			if code.Image != tt.wantCodeImage {
				t.Errorf("code-server image = %q, want %q", code.Image, tt.wantCodeImage)
			}
			if v, _ := envValue(code, "PASSWORD"); v != tt.wantPassword {
				t.Errorf("code-server PASSWORD = %q, want %q", v, tt.wantPassword)
			}

			// Every PVC-backed volume must reference a claim that BuildPVCs creates.
			claims := map[string]bool{}
			for _, pvc := range NewResourceBuilder().BuildPVCs(session) {
				claims[pvc.Name] = true
			}
			for _, v := range pod.Spec.Volumes {
				if v.PersistentVolumeClaim != nil && !claims[v.PersistentVolumeClaim.ClaimName] {
					t.Errorf("volume %q references claim %q that BuildPVCs does not create", v.Name, v.PersistentVolumeClaim.ClaimName)
				}
			}
		})
	}
}

func TestBuildPodVolumeMounts(t *testing.T) {
	pod := NewResourceBuilder().BuildPod(newSession(LabSessionSpec{UserID: "u", SessionID: "s"}))

	volumes := map[string]bool{}
	for _, v := range pod.Spec.Volumes {
		volumes[v.Name] = true
	}
	all := append(append([]corev1.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...)
	for _, c := range all {
		for _, m := range c.VolumeMounts {
			if !volumes[m.Name] {
				t.Errorf("container %q mounts undefined volume %q", c.Name, m.Name)
			}
		}
	}
}

func TestBuildService(t *testing.T) {
	session := newSession(LabSessionSpec{UserID: "u1", SessionID: "s1"})
	svc := NewResourceBuilder().BuildService(session)

	if svc.Name != "lab-service-s1" {
		t.Errorf("service name = %q, want %q", svc.Name, "lab-service-s1")
	}
	if svc.Namespace != "labs" {
		t.Errorf("namespace = %q, want %q", svc.Namespace, "labs")
	}

	// The selector must match the pod's labels.
	pod := NewResourceBuilder().BuildPod(session)
	for k, v := range svc.Spec.Selector {
		if pod.Labels[k] != v {
			t.Errorf("selector %s=%q does not match pod label %q", k, v, pod.Labels[k])
		}
	}

	wantPorts := []struct {
		name string
		port int32
	}{
		{"vscode", 8080},
		{"terminal", 8081},
		{"ssh", 22},
	}
	if len(svc.Spec.Ports) != len(wantPorts) {
		t.Fatalf("got %d ports, want %d", len(svc.Spec.Ports), len(wantPorts))
	}
	for i, want := range wantPorts {
		t.Run(want.name, func(t *testing.T) {
			got := svc.Spec.Ports[i]
			if got.Name != want.name || got.Port != want.port || got.TargetPort.IntVal != want.port {
				t.Errorf("port = %s %d->%s, want %s %d->%d", got.Name, got.Port, got.TargetPort.String(), want.name, want.port, want.port)
			}
			if got.Protocol != corev1.ProtocolTCP {
				t.Errorf("protocol = %q, want TCP", got.Protocol)
			}
		})
	}
}

func TestBuildPVCs(t *testing.T) {
	tests := []struct {
		name        string
		storage     string
		wantVMSize  string
		wantVSCSize string
	}{
		{name: "default storage", storage: "", wantVMSize: "10Gi", wantVSCSize: "5Gi"},
		{name: "custom storage", storage: "20Gi", wantVMSize: "20Gi", wantVSCSize: "5Gi"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := newSession(LabSessionSpec{
				UserID:    "u1",
				SessionID: "s1",
				Resources: ResourceConfig{Storage: tt.storage},
			})
			pvcs := NewResourceBuilder().BuildPVCs(session)
			if len(pvcs) != 2 {
				t.Fatalf("got %d PVCs, want 2", len(pvcs))
			}

			want := map[string]string{
				"vm-data-s1":     tt.wantVMSize,
				"vscode-data-s1": tt.wantVSCSize,
			}
			for _, pvc := range pvcs {
				size, ok := want[pvc.Name]
				if !ok {
					t.Errorf("unexpected PVC %q", pvc.Name)
					continue
				}
				if pvc.Namespace != "labs" {
					t.Errorf("%s namespace = %q, want %q", pvc.Name, pvc.Namespace, "labs")
				}
				checkQuantity(t, pvc.Name+" storage", pvc.Spec.Resources.Requests[corev1.ResourceStorage], size)
				if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce {
					t.Errorf("%s access modes = %v, want [ReadWriteOnce]", pvc.Name, pvc.Spec.AccessModes)
				}
			}
		})
	}
}

func checkQuantity(t *testing.T, what string, got resource.Quantity, want string) {
	t.Helper()
	if got.Cmp(resource.MustParse(want)) != 0 {
		t.Errorf("%s = %s, want %s", what, got.String(), want)
	}
}

func TestGetResourceLimits(t *testing.T) {
	tests := []struct {
		name     string
		input    ResourceConfig
		expected ResourceConfig
	}{
		{"defaults", ResourceConfig{}, ResourceConfig{Memory: "4Gi", CPU: "2"}},
		{"override memory", ResourceConfig{Memory: "8Gi"}, ResourceConfig{Memory: "8Gi", CPU: "2"}},
		{"override cpu", ResourceConfig{CPU: "4"}, ResourceConfig{Memory: "4Gi", CPU: "4"}},
		{"at maximum", ResourceConfig{Memory: "16Gi", CPU: "8"}, ResourceConfig{Memory: "16Gi", CPU: "8"}},
		{"memory capped", ResourceConfig{Memory: "20Gi"}, ResourceConfig{Memory: "16Gi", CPU: "2"}},
		{"cpu capped", ResourceConfig{CPU: "10"}, ResourceConfig{Memory: "4Gi", CPU: "8"}},
	}

	rb := NewResourceBuilder()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := rb.getResourceLimits(tt.input)
			if result.Memory != tt.expected.Memory || result.CPU != tt.expected.CPU {
				t.Errorf("getResourceLimits(%v) = %v; want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestGetMemoryRequest(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"4Gi", "4Gi", "3Gi"},
		{"8Gi", "8Gi", "6Gi"},
		{"non-Gi falls back", "100Mi", "3Gi"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getMemoryRequest(tt.input)
			if result != tt.expected {
				t.Errorf("getMemoryRequest(%v) = %v; want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestGetCPURequest(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"2 cores", "2", "1"},
		{"8 cores", "8", "4"},
		{"millicores fall back", "500m", "1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getCPURequest(tt.input)
			if result != tt.expected {
				t.Errorf("getCPURequest(%v) = %v; want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestParseMemoryGi(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{"4Gi", "4Gi", 4},
		{"16Gi", "16Gi", 16},
		{"non-Gi falls back", "512Mi", 4},
		{"empty falls back", "", 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseMemoryGi(tt.input)
			if result != tt.expected {
				t.Errorf("parseMemoryGi(%v) = %v; want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestParseCPU(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{"2 cores", "2", 2},
		{"millicores fall back", "500m", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseCPU(tt.input)
			if result != tt.expected {
				t.Errorf("parseCPU(%v) = %v; want %v", tt.input, result, tt.expected)
			}
		})
	}
}
