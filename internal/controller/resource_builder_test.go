package controller

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

var testSettings = PodSettings{
	VMImage:       "dozlab-firecracker:test",
	InitImage:     "dozlab-init:test",
	TerminalImage: "dozlab-terminal:test",
	SSHUser:       "root",
	VMDiskSize:    "4Gi",
}

func newSession(spec LabSessionSpec) *LabSession {
	s := &LabSession{Spec: spec}
	s.Namespace = "labs"
	return s
}

// secretRef returns the Secret reference of env var name in c, or nil.
func secretRef(c corev1.Container, name string) *corev1.SecretKeySelector {
	for _, e := range c.Env {
		if e.Name == name && e.ValueFrom != nil {
			return e.ValueFrom.SecretKeyRef
		}
	}
	return nil
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
			wantVMImage:   "dozlab-firecracker:test",
			wantTermImage: "dozlab-terminal:test",
			wantCodeImage: VSCodeImage,
			wantPassword:  "changeme",
			wantMemLimit:  "1152Mi", // default VM: 1024 MiB + 128 MiB Firecracker overhead
			wantCPULimit:  "1",
			wantMemReq:    "1152Mi",
			wantCPUReq:    "100m",
		},
		{
			name: "password and resources",
			spec: LabSessionSpec{
				UserID:    "u2",
				SessionID: "s2",
				Resources: ResourceConfig{Memory: "8Gi", CPU: "4"},
				Config:    SessionConfig{VSCodePassword: "secret"},
			},
			wantVMImage:   "dozlab-firecracker:test",
			wantTermImage: "dozlab-terminal:test",
			wantCodeImage: VSCodeImage,
			wantPassword:  "secret",
			wantMemLimit:  "8320Mi",
			wantCPULimit:  "4",
			wantMemReq:    "8320Mi",
			wantCPUReq:    "100m",
		},
		{
			name: "resources above maximum are capped",
			spec: LabSessionSpec{
				UserID:    "u3",
				SessionID: "s3",
				Resources: ResourceConfig{Memory: "64Gi", CPU: "32"},
			},
			wantVMImage:   "dozlab-firecracker:test",
			wantTermImage: "dozlab-terminal:test",
			wantCodeImage: VSCodeImage,
			wantPassword:  "changeme",
			wantMemLimit:  "16512Mi",
			wantCPULimit:  "8",
			wantMemReq:    "16512Mi",
			wantCPUReq:    "100m",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := newSession(tt.spec)
			pod := NewResourceBuilder(testSettings).BuildPod(session)

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
			var initNames []string
			for _, c := range pod.Spec.InitContainers {
				initNames = append(initNames, c.Name)
			}
			if len(initNames) != 2 || initNames[0] != "init-rootfs" || initNames[1] != "network-setup" {
				t.Errorf("init containers = %v, want [init-rootfs network-setup]", initNames)
			}
			if len(pod.Spec.Containers) != 3 {
				t.Fatalf("got %d containers, want 3", len(pod.Spec.Containers))
			}

			vm := findContainer(t, pod.Spec.Containers, "firecracker-vm")
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
			netSetup := findContainer(t, pod.Spec.InitContainers, "network-setup")
			if netSetup.Image != NetworkSetupImage {
				t.Errorf("network-setup image = %q, want %q", netSetup.Image, NetworkSetupImage)
			}
			for _, c := range []corev1.Container{netSetup, code} {
				if c.ImagePullPolicy != corev1.PullIfNotPresent {
					t.Errorf("%s pull policy = %q, want IfNotPresent", c.Name, c.ImagePullPolicy)
				}
			}
			if v, _ := envValue(code, "PASSWORD"); v != tt.wantPassword {
				t.Errorf("code-server PASSWORD = %q, want %q", v, tt.wantPassword)
			}

			// Every PVC-backed volume must reference a claim that BuildPVCs creates.
			claims := map[string]bool{}
			for _, pvc := range NewResourceBuilder(testSettings).BuildPVCs(session) {
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
	pod := NewResourceBuilder(testSettings).BuildPod(newSession(LabSessionSpec{UserID: "u", SessionID: "s"}))

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
	svc := NewResourceBuilder(testSettings).BuildService(session)

	if svc.Name != "lab-service-s1" {
		t.Errorf("service name = %q, want %q", svc.Name, "lab-service-s1")
	}
	if svc.Namespace != "labs" {
		t.Errorf("namespace = %q, want %q", svc.Namespace, "labs")
	}

	// The selector must match the pod's labels.
	pod := NewResourceBuilder(testSettings).BuildPod(session)
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
			pvcs := NewResourceBuilder(testSettings).BuildPVCs(session)
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

func TestBuildPVCsStorageClass(t *testing.T) {
	session := newSession(LabSessionSpec{UserID: "u1", SessionID: "s1"})

	// Unset: nil, so the cluster's default StorageClass is used
	for _, pvc := range NewResourceBuilder(testSettings).BuildPVCs(session) {
		if pvc.Spec.StorageClassName != nil {
			t.Errorf("%s storage class = %q, want nil", pvc.Name, *pvc.Spec.StorageClassName)
		}
	}

	settings := testSettings
	settings.StorageClass = "local-path"
	for _, pvc := range NewResourceBuilder(settings).BuildPVCs(session) {
		if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "local-path" {
			t.Errorf("%s storage class = %v, want local-path", pvc.Name, pvc.Spec.StorageClassName)
		}
	}
}

func checkQuantity(t *testing.T, what string, got resource.Quantity, want string) {
	t.Helper()
	if got.Cmp(resource.MustParse(want)) != 0 {
		t.Errorf("%s = %s, want %s", what, got.String(), want)
	}
}

func TestVMSize(t *testing.T) {
	rb := NewResourceBuilder(testSettings)
	tests := []struct {
		name      string
		resources ResourceConfig
		wantCPUs  int
		wantMiB   int64
		wantDisk  string
	}{
		{"unset keeps the old fixed size", ResourceConfig{}, 1, 1024, testSettings.VMDiskSize},
		{"vm lab baseline", ResourceConfig{CPU: "1", Memory: "512Mi", Storage: "1Gi"}, 1, 512, "1Gi"},
		{"k8s lab", ResourceConfig{CPU: "2", Memory: "2Gi", Storage: "4Gi"}, 2, 2048, "4Gi"},
		{"millicores round up", ResourceConfig{CPU: "1500m"}, 2, 1024, testSettings.VMDiskSize},
		{"above maximum is capped", ResourceConfig{CPU: "32", Memory: "64Gi"}, maxVMCPUs, maxVMMemoryMiB, testSettings.VMDiskSize},
		{"below minimum is raised", ResourceConfig{CPU: "100m", Memory: "16Mi"}, 1, minVMMemoryMiB, testSettings.VMDiskSize},
		{"unparsable falls back", ResourceConfig{CPU: "lots", Memory: "big", Storage: "huge"}, 1, 1024, testSettings.VMDiskSize},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rb.vmSize(newSession(LabSessionSpec{SessionID: "s", Resources: tt.resources}))
			if got.CPUs != tt.wantCPUs || got.MemoryMiB != tt.wantMiB {
				t.Errorf("vmSize(%+v) = %d vCPUs, %d MiB; want %d, %d", tt.resources, got.CPUs, got.MemoryMiB, tt.wantCPUs, tt.wantMiB)
			}
			checkQuantity(t, "disk", got.Disk, tt.wantDisk)
		})
	}
}

// TestBuildPodVMSize checks that the session's size reaches Firecracker, the init container
// (the disk the rootfs is grown to) and the vm-kernels volume.
func TestBuildPodVMSize(t *testing.T) {
	pod := NewResourceBuilder(testSettings).BuildPod(newSession(LabSessionSpec{
		SessionID: "k8s",
		Resources: ResourceConfig{CPU: "2", Memory: "2Gi", Storage: "4Gi"},
	}))
	vm := findContainer(t, pod.Spec.Containers, "firecracker-vm")
	for k, want := range map[string]string{"CPU_COUNT": "2", "MEMORY": "2048"} {
		if got, _ := envValue(vm, k); got != want {
			t.Errorf("vm %s = %q, want %q", k, got, want)
		}
	}
	checkQuantity(t, "vm memory request", vm.Resources.Requests[corev1.ResourceMemory], "2176Mi")
	checkQuantity(t, "vm cpu limit", vm.Resources.Limits[corev1.ResourceCPU], "2")
	init := findContainer(t, pod.Spec.InitContainers, "init-rootfs")
	if got, _ := envValue(init, "IMAGE_SIZE"); got != "4096M" {
		t.Errorf("init IMAGE_SIZE = %q, want %q", got, "4096M")
	}
	for _, v := range pod.Spec.Volumes {
		if v.Name == "vm-kernels" {
			checkQuantity(t, "vm-kernels size limit", *v.EmptyDir.SizeLimit, "8Gi")
		}
	}
}

func TestBuildPodMatchesReference(t *testing.T) {
	pod := NewResourceBuilder(testSettings).BuildPod(newSession(LabSessionSpec{
		UserID:    "u1",
		SessionID: "demo",
		RootfsURL: "https://example.test/rootfs.ext4",
	}))

	t.Run("ip_forward sysctl", func(t *testing.T) {
		sc := pod.Spec.SecurityContext
		if sc == nil {
			t.Fatal("pod security context is nil")
		}
		found := false
		for _, s := range sc.Sysctls {
			if s.Name == "net.ipv4.ip_forward" && s.Value == "1" {
				found = true
			}
		}
		if !found {
			t.Errorf("sysctls = %v, want net.ipv4.ip_forward=1", sc.Sysctls)
		}
	})

	vm := findContainer(t, pod.Spec.Containers, "firecracker-vm")

	t.Run("vm requests kvm and tun", func(t *testing.T) {
		for _, name := range []corev1.ResourceName{"dozlab.io/kvm", "dozlab.io/tun"} {
			checkQuantity(t, string(name)+" request", vm.Resources.Requests[name], "1")
			checkQuantity(t, string(name)+" limit", vm.Resources.Limits[name], "1")
		}
	})

	t.Run("vm runs as root with caps", func(t *testing.T) {
		sc := vm.SecurityContext
		if sc == nil {
			t.Fatal("vm security context is nil")
		}
		if sc.RunAsNonRoot != nil || sc.RunAsUser != nil || sc.AllowPrivilegeEscalation != nil {
			t.Errorf("vm must run as root: runAsNonRoot=%v runAsUser=%v allowPrivilegeEscalation=%v",
				sc.RunAsNonRoot, sc.RunAsUser, sc.AllowPrivilegeEscalation)
		}
		if sc.Privileged != nil && *sc.Privileged {
			t.Error("vm must not be privileged")
		}
		if sc.Capabilities == nil {
			t.Fatal("vm capabilities are nil")
		}
		want := map[corev1.Capability]bool{"NET_ADMIN": true, "SYS_ADMIN": true, "SYS_RESOURCE": true}
		for _, c := range sc.Capabilities.Add {
			delete(want, c)
		}
		if len(want) != 0 {
			t.Errorf("capabilities %v missing from %v", want, sc.Capabilities.Add)
		}
		if len(sc.Capabilities.Drop) != 0 {
			t.Errorf("capabilities drop = %v, want none", sc.Capabilities.Drop)
		}
	})

	t.Run("vm uses image entrypoint", func(t *testing.T) {
		if len(vm.Command) != 0 || len(vm.Args) != 0 {
			t.Errorf("vm command/args = %v %v, want image entrypoint", vm.Command, vm.Args)
		}
	})

	t.Run("vm env", func(t *testing.T) {
		want := map[string]string{
			"SESSION_ID":  "demo",
			"ROOTFS_PATH": "/srv/vm/kernels/rootfs.ext4",
			"KERNEL_PATH": "/find/vmlinux.bin",
			"VM_IP":       "172.16.0.2",
			"GATEWAY_IP":  "172.16.0.1",
		}
		for k, v := range want {
			if got, ok := envValue(vm, k); !ok || got != v {
				t.Errorf("vm %s = %q, want %q", k, got, v)
			}
		}
	})

	t.Run("init-rootfs writes the rootfs the vm reads", func(t *testing.T) {
		init := findContainer(t, pod.Spec.InitContainers, "init-rootfs")
		if init.Image != testSettings.InitImage {
			t.Errorf("init image = %q, want %q", init.Image, testSettings.InitImage)
		}
		rootfs, _ := envValue(vm, "ROOTFS_PATH")
		if got, _ := envValue(init, "IMAGE_PATH"); got != rootfs {
			t.Errorf("init IMAGE_PATH = %q, want vm ROOTFS_PATH %q", got, rootfs)
		}
		if got, _ := envValue(init, "IMAGE_SIZE"); got != "4096M" {
			t.Errorf("init IMAGE_SIZE = %q, want %q", got, "4096M")
		}
		if got, _ := envValue(init, "IMAGE_DOWNLOAD_URL"); got != "https://example.test/rootfs.ext4" {
			t.Errorf("init IMAGE_DOWNLOAD_URL = %q, want the session rootfsUrl", got)
		}
	})

	t.Run("vm-kernels is larger than the disk", func(t *testing.T) {
		for _, v := range pod.Spec.Volumes {
			if v.Name != "vm-kernels" {
				continue
			}
			if v.EmptyDir == nil || v.EmptyDir.SizeLimit == nil {
				t.Fatal("vm-kernels must be an emptyDir with a sizeLimit")
			}
			if v.EmptyDir.SizeLimit.Cmp(resource.MustParse(testSettings.VMDiskSize)) <= 0 {
				t.Errorf("vm-kernels sizeLimit %s must exceed disk size %s", v.EmptyDir.SizeLimit.String(), testSettings.VMDiskSize)
			}
			return
		}
		t.Error("vm-kernels volume not found")
	})

	t.Run("terminal gets ssh user and the session's private key", func(t *testing.T) {
		term := findContainer(t, pod.Spec.Containers, "terminal-sidecar")
		if got, _ := envValue(term, "SSH_USER"); got != "root" {
			t.Errorf("SSH_USER = %q, want root", got)
		}
		if got, _ := envValue(term, "VM_IP"); got != "172.16.0.2" {
			t.Errorf("terminal VM_IP = %q, want 172.16.0.2", got)
		}
		if ref := secretRef(term, "SSH_PRIVATE_KEY"); ref == nil || ref.Name != "lab-session-demo-ssh" || ref.Key != "id_ed25519" {
			t.Errorf("SSH_PRIVATE_KEY secretKeyRef = %+v, want lab-session-demo-ssh/id_ed25519", ref)
		}
	})

	t.Run("init-rootfs gets the session's public key for the cloud-init seed", func(t *testing.T) {
		init := findContainer(t, pod.Spec.InitContainers, "init-rootfs")
		if ref := secretRef(init, "SSH_AUTHORIZED_KEY"); ref == nil || ref.Name != "lab-session-demo-ssh" || ref.Key != "id_ed25519.pub" {
			t.Errorf("SSH_AUTHORIZED_KEY secretKeyRef = %+v, want lab-session-demo-ssh/id_ed25519.pub", ref)
		}
		if got, _ := envValue(init, "SESSION_ID"); got != "demo" {
			t.Errorf("init SESSION_ID = %q, want demo", got)
		}
	})

	t.Run("no placeholder images", func(t *testing.T) {
		all := append(append([]corev1.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...)
		for _, c := range all {
			if strings.HasPrefix(c.Image, "your-") || c.Image == "" {
				t.Errorf("container %q has placeholder image %q", c.Name, c.Image)
			}
			for _, a := range append(append([]string{}, c.Command...), c.Args...) {
				if strings.Contains(a, "your-") {
					t.Errorf("container %q still runs a placeholder: %q", c.Name, a)
				}
			}
		}
	})
}

func TestResize2fsSize(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"4Gi", "4096M"},
		{"512Mi", "512M"},
		{"10G", "9536M"}, // decimal G rounds down to whole MiB
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := resize2fsSize(resource.MustParse(tt.in)); got != tt.want {
				t.Errorf("resize2fsSize(%s) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestVMKernelsSizeLimit(t *testing.T) {
	got := vmKernelsSizeLimit(resource.MustParse("4Gi"))
	if got.Cmp(resource.MustParse("8Gi")) != 0 {
		t.Errorf("vmKernelsSizeLimit(4Gi) = %s, want 8Gi", got.String())
	}
}

func TestInitImagePerLab(t *testing.T) {
	rb := NewResourceBuilder(testSettings)
	for _, tc := range []struct {
		name, initImage, want string
	}{
		{"no lab image uses the controller default", "", testSettings.InitImage},
		{"the lab's image wins", "dozlab-init-k8s:1", "dozlab-init-k8s:1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pod := rb.BuildPod(newSession(LabSessionSpec{
				SessionID:    "demo",
				CustomImages: ImageConfig{InitImage: tc.initImage},
			}))
			if got := findContainer(t, pod.Spec.InitContainers, "init-rootfs").Image; got != tc.want {
				t.Errorf("init-rootfs image = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildPodProbes(t *testing.T) {
	pod := NewResourceBuilder(testSettings).BuildPod(newSession(LabSessionSpec{UserID: "u1", SessionID: "demo"}))

	tests := []struct {
		container           string
		wantTCPPort         int
		wantHTTPPath        string
		wantReadinessPeriod int32
	}{
		{container: "firecracker-vm", wantTCPPort: 22, wantReadinessPeriod: 10},
		{container: "terminal-sidecar", wantHTTPPath: "/health", wantReadinessPeriod: 5},
		{container: "code-server", wantHTTPPath: "/healthz", wantReadinessPeriod: 5},
	}

	for _, tt := range tests {
		t.Run(tt.container, func(t *testing.T) {
			c := findContainer(t, pod.Spec.Containers, tt.container)

			if c.StartupProbe == nil {
				t.Fatal("startup probe is nil")
			}
			if got := c.StartupProbe.PeriodSeconds; got != 1 {
				t.Errorf("startup probe period = %d, want 1", got)
			}
			if got := c.StartupProbe.FailureThreshold; got != startupProbeBudgetSeconds {
				t.Errorf("startup probe failure threshold = %d, want %d", got, startupProbeBudgetSeconds)
			}
			if tt.wantTCPPort != 0 {
				if c.StartupProbe.TCPSocket == nil {
					t.Fatal("startup probe has no TCP check")
				}
				if got := c.StartupProbe.TCPSocket.Port.IntValue(); got != tt.wantTCPPort {
					t.Errorf("startup probe port = %d, want %d", got, tt.wantTCPPort)
				}
			} else {
				if c.StartupProbe.HTTPGet == nil {
					t.Fatal("startup probe has no HTTP check")
				}
				if got := c.StartupProbe.HTTPGet.Path; got != tt.wantHTTPPath {
					t.Errorf("startup probe path = %q, want %q", got, tt.wantHTTPPath)
				}
			}

			// The startup probe gates readiness, so readiness needs no initial delay.
			if c.ReadinessProbe == nil {
				t.Fatal("readiness probe is nil")
			}
			if got := c.ReadinessProbe.InitialDelaySeconds; got != 0 {
				t.Errorf("readiness initial delay = %d, want 0", got)
			}
			if got := c.ReadinessProbe.PeriodSeconds; got != tt.wantReadinessPeriod {
				t.Errorf("readiness period = %d, want %d", got, tt.wantReadinessPeriod)
			}
		})
	}
}
