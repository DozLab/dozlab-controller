package controller

import (
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// VM network and paths shared by the pod builder and start-firecracker.sh.
// The VM sits on a tap device behind the pod IP; start-firecracker.sh DNATs
// the pod IP to VMIP, except the sidecar ports (POD_LOCAL_PORTS).
const (
	VMGatewayIP     = "172.16.0.1"
	VMIP            = "172.16.0.2"
	VMTapDevice     = "tap0"
	VMSSHPort       = 22
	VMKernelsPath   = "/srv/vm/kernels"
	VMRootfsPath    = VMKernelsPath + "/rootfs.ext4"
	VMKernelPath    = "/find/vmlinux.bin" // baked into the VM image
	ipForwardSysctl = "net.ipv4.ip_forward"

	// Public images are pinned by digest and pulled IfNotPresent, so a session
	// never waits on (or fails at) a registry lookup once the node has them.
	NetworkSetupImage = "busybox:1.38.0@sha256:fd7dc98638c8e305f4dc34e979f1c0fdfdcaeb0fbf8fcff77ae834b6da3d7e6e"
	VSCodeImage       = "codercom/code-server:4.139.1@sha256:0c067c3cf09ed1830ce282387826be8feefef8a5828f462791c2df3f1007ee17"

	// VM size when the session doesn't set one: the size every VM had before sizes came from
	// the lab, so sessions without a size don't change.
	defaultVMCPUs      = 1
	defaultVMMemoryMiB = 1024
	maxVMCPUs          = 8
	minVMMemoryMiB     = 128
	maxVMMemoryMiB     = 16 * 1024

	// firecrackerOverheadMiB is reserved on top of the VM's memory for the Firecracker process
	// and start-firecracker.sh (not yet measured on its own). vmContainerCPURequest is small:
	// idle VMs used 0.2-2.5% of a CPU; the limit lets a busy VM use all of its vCPUs.
	firecrackerOverheadMiB = 128
	vmContainerCPURequest  = "100m"

	// KVMResource and TUNResource are the device plugin resources that give
	// the VM container /dev/kvm and /dev/net/tun without privileged mode.
	KVMResource corev1.ResourceName = "dozlab.io/kvm"
	TUNResource corev1.ResourceName = "dozlab.io/tun"
)

// ResourceBuilder builds Kubernetes resources for lab sessions
type ResourceBuilder struct {
	settings PodSettings
}

// NewResourceBuilder creates a new resource builder
func NewResourceBuilder(settings PodSettings) *ResourceBuilder {
	return &ResourceBuilder{settings: settings}
}

// BuildPod creates a pod specification for a lab session
func (rb *ResourceBuilder) BuildPod(session *LabSession) *corev1.Pod {
	sessionID := session.Spec.SessionID
	podName := fmt.Sprintf("lab-session-%s", sessionID)

	size := rb.vmSize(session)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: session.Namespace,
			Labels: map[string]string{
				"app":        "lab-environment",
				"session-id": sessionID,
				"user-id":    session.Spec.UserID,
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			SecurityContext: &corev1.PodSecurityContext{
				// start-firecracker.sh NATs the VM, and /proc/sys is read-only in the
				// container. The kubelet must allow it (--allowed-unsafe-sysctls).
				Sysctls:        []corev1.Sysctl{{Name: ipForwardSysctl, Value: "1"}},
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			InitContainers: []corev1.Container{
				rb.buildRootfsInitContainer(session, size),
				rb.buildNetworkSetupContainer(),
			},
			Containers: []corev1.Container{
				rb.buildVMContainer(session, size),
				rb.buildTerminalContainer(session),
				rb.buildVSCodeContainer(session),
			},
			Volumes: rb.buildVolumes(sessionID, size),
		},
	}

	return pod
}

// buildRootfsInitContainer writes the VM rootfs into the vm-kernels volume, grows it, and
// writes the session's cloud-init seed (root's SSH key) into it
func (rb *ResourceBuilder) buildRootfsInitContainer(session *LabSession, size vmSize) corev1.Container {
	sessionID := session.Spec.SessionID
	initImage := rb.settings.InitImage
	if session.Spec.CustomImages.InitImage != "" {
		initImage = session.Spec.CustomImages.InitImage
	}
	return corev1.Container{
		Name:  "init-rootfs",
		Image: initImage,
		Env: []corev1.EnvVar{
			{Name: "IMAGE_DOWNLOAD_URL", Value: session.Spec.RootfsURL},
			{Name: "IMAGE_SIZE", Value: resize2fsSize(size.Disk)},
			{Name: "IMAGE_PATH", Value: VMRootfsPath},
			{Name: "SESSION_ID", Value: sessionID},
			sshKeyEnv("SSH_AUTHORIZED_KEY", sessionID, SSHPublicKeyKey),
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "vm-kernels", MountPath: VMKernelsPath},
		},
	}
}

// buildNetworkSetupContainer writes the VM network settings for the sidecars
func (rb *ResourceBuilder) buildNetworkSetupContainer() corev1.Container {
	return corev1.Container{
		Name:            "network-setup",
		Image:           NetworkSetupImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"sh", "-c"},
		Args: []string{fmt.Sprintf(
			`printf "GATEWAY_IP=%s\nVM_IP=%s\nPOD_IP=%%s\nTAP_DEVICE=%s\n" "$(hostname -i)" > /shared/network-config; cat /shared/network-config`,
			VMGatewayIP, VMIP, VMTapDevice)},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "shared-config", MountPath: "/shared"},
		},
	}
}

// buildVMContainer creates the Firecracker VM container. It runs as root with
// the image's own entrypoint: as non-root the added capabilities are
// ineffective and ip tuntap / iptables fail.
// buildVMContainer runs Firecracker with the session's VM size. The container reserves the VM's
// memory plus Firecracker's overhead, and little CPU; its CPU limit is the VM's vCPU count.
func (rb *ResourceBuilder) buildVMContainer(session *LabSession, size vmSize) corev1.Container {
	sessionID := session.Spec.SessionID
	vmImage := rb.settings.VMImage

	return corev1.Container{
		Name:  "firecracker-vm",
		Image: vmImage,
		SecurityContext: &corev1.SecurityContext{
			Capabilities: &corev1.Capabilities{
				Add: []corev1.Capability{"NET_ADMIN", "SYS_ADMIN", "SYS_RESOURCE"},
			},
		},
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: size.containerMemory(),
				corev1.ResourceCPU:    *resource.NewQuantity(int64(size.CPUs), resource.DecimalSI),
				KVMResource:           resource.MustParse("1"),
				TUNResource:           resource.MustParse("1"),
			},
			Requests: corev1.ResourceList{
				corev1.ResourceMemory: size.containerMemory(),
				corev1.ResourceCPU:    resource.MustParse(vmContainerCPURequest),
				KVMResource:           resource.MustParse("1"),
				TUNResource:           resource.MustParse("1"),
			},
		},
		Env: []corev1.EnvVar{
			{Name: "SESSION_ID", Value: sessionID},
			{Name: "ROOTFS_PATH", Value: VMRootfsPath},
			{Name: "KERNEL_PATH", Value: VMKernelPath},
			{Name: "CPU_COUNT", Value: strconv.Itoa(size.CPUs)},
			{Name: "MEMORY", Value: strconv.FormatInt(size.MemoryMiB, 10)},
			{Name: "GATEWAY_IP", Value: VMGatewayIP},
			{Name: "VM_IP", Value: VMIP},
			{Name: "TAP_DEVICE_NAME", Value: VMTapDevice},
		},
		Ports: []corev1.ContainerPort{
			{ContainerPort: VMSSHPort, Name: "vm-ssh"},
		},
		// sshd inside the VM, reached through start-firecracker.sh's DNAT on the pod IP.
		// Readiness stays slow: sshd logs every probe connection.
		StartupProbe: startupProbe(vmSSHProbe()),
		ReadinessProbe: &corev1.Probe{
			ProbeHandler:  vmSSHProbe(),
			PeriodSeconds: 10,
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "vm-kernels", MountPath: VMKernelsPath},
			{Name: "shared-config", MountPath: "/shared", ReadOnly: true},
		},
	}
}

// buildTerminalContainer creates the terminal sidecar container
func (rb *ResourceBuilder) buildTerminalContainer(session *LabSession) corev1.Container {
	sessionID := session.Spec.SessionID
	terminalImage := rb.settings.TerminalImage

	return corev1.Container{
		Name:  "terminal-sidecar",
		Image: terminalImage,
		Ports: []corev1.ContainerPort{
			{ContainerPort: 8081, Name: "terminal"},
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/health",
					Port: intstr.FromInt(8081),
				},
			},
			InitialDelaySeconds: 15,
			PeriodSeconds:       10,
			TimeoutSeconds:      5,
			FailureThreshold:    3,
		},
		StartupProbe: startupProbe(corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/health",
				Port: intstr.FromInt(8081),
			},
		}),
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/health",
					Port: intstr.FromInt(8081),
				},
			},
			PeriodSeconds:    5,
			TimeoutSeconds:   3,
			FailureThreshold: 3,
		},
		Env: []corev1.EnvVar{
			{Name: "SESSION_ID", Value: sessionID},
			{Name: "VM_IP", Value: VMIP},
			{Name: "VM_SSH_PORT", Value: strconv.Itoa(VMSSHPort)},
			{Name: "SSH_USER", Value: rb.settings.SSHUser},
			sshKeyEnv("SSH_PRIVATE_KEY", sessionID, SSHPrivateKeyKey),
		},
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("512Mi"),
				corev1.ResourceCPU:    resource.MustParse("500m"),
			},
			Requests: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("256Mi"),
				corev1.ResourceCPU:    resource.MustParse("250m"),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "shared-config", MountPath: "/shared", ReadOnly: true},
		},
	}
}

// buildVSCodeContainer creates the VS Code sidecar container
func (rb *ResourceBuilder) buildVSCodeContainer(session *LabSession) corev1.Container {
	sessionID := session.Spec.SessionID
	vscodeImage := VSCodeImage

	password := "changeme"
	if session.Spec.Config.VSCodePassword != "" {
		password = session.Spec.Config.VSCodePassword
	}

	return corev1.Container{
		Name:            "code-server",
		Image:           vscodeImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Ports: []corev1.ContainerPort{
			{ContainerPort: 8080, Name: "vscode"},
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/healthz",
					Port: intstr.FromInt(8080),
				},
			},
			InitialDelaySeconds: 15,
			PeriodSeconds:       10,
			TimeoutSeconds:      5,
			FailureThreshold:    3,
		},
		StartupProbe: startupProbe(corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/healthz",
				Port: intstr.FromInt(8080),
			},
		}),
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/healthz",
					Port: intstr.FromInt(8080),
				},
			},
			PeriodSeconds:    5,
			TimeoutSeconds:   3,
			FailureThreshold: 3,
		},
		Env: []corev1.EnvVar{
			{Name: "SESSION_ID", Value: sessionID},
			{Name: "PASSWORD", Value: password},
		},
		Args: []string{
			"--bind-addr", "0.0.0.0:8080",
			"--auth", "password",
			"/workspace",
		},
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("2Gi"),
				corev1.ResourceCPU:    resource.MustParse("1"),
			},
			Requests: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourceCPU:    resource.MustParse("500m"),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "vscode-data", MountPath: "/home/coder"},
			{Name: "vm-data", MountPath: "/workspace"},
		},
	}
}

// buildVolumes creates the volumes for the pod
func (rb *ResourceBuilder) buildVolumes(sessionID string, size vmSize) []corev1.Volume {
	return []corev1.Volume{
		{
			Name: "vm-data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: fmt.Sprintf("vm-data-%s", sessionID),
				},
			},
		},
		{
			Name: "vscode-data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: fmt.Sprintf("vscode-data-%s", sessionID),
				},
			},
		},
		{
			Name: "shared-config",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: resourcePtr(resource.MustParse("10Mi"))},
			},
		},
		{
			// Holds the rootfs; must be larger than the disk it is grown to.
			Name: "vm-kernels",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: resourcePtr(vmKernelsSizeLimit(size.Disk))},
			},
		},
	}
}

// BuildService creates a service for a lab session
func (rb *ResourceBuilder) BuildService(session *LabSession) *corev1.Service {
	sessionID := session.Spec.SessionID
	serviceName := fmt.Sprintf("lab-service-%s", sessionID)

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: session.Namespace,
			Labels: map[string]string{
				"app":        "lab-environment",
				"session-id": sessionID,
				"user-id":    session.Spec.UserID,
			},
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeLoadBalancer,
			Selector: map[string]string{
				"app":        "lab-environment",
				"session-id": sessionID,
			},
			Ports: []corev1.ServicePort{
				{
					Name:       "vscode",
					Port:       8080,
					TargetPort: intstr.FromInt(8080),
					Protocol:   corev1.ProtocolTCP,
				},
				{
					Name:       "terminal",
					Port:       8081,
					TargetPort: intstr.FromInt(8081),
					Protocol:   corev1.ProtocolTCP,
				},
				{
					Name:       "ssh",
					Port:       22,
					TargetPort: intstr.FromInt(22),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}

	return service
}

// BuildPVCs creates persistent volume claims for a lab session
func (rb *ResourceBuilder) BuildPVCs(session *LabSession) []*corev1.PersistentVolumeClaim {
	sessionID := session.Spec.SessionID
	storageSize := "10Gi"
	if session.Spec.Resources.Storage != "" {
		storageSize = session.Spec.Resources.Storage
	}

	pvcs := []*corev1.PersistentVolumeClaim{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("vm-data-%s", sessionID),
				Namespace: session.Namespace,
				Labels: map[string]string{
					"app":        "lab-environment",
					"session-id": sessionID,
				},
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{
					corev1.ReadWriteOnce,
				},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse(storageSize),
					},
				},
				StorageClassName: rb.storageClassName(),
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("vscode-data-%s", sessionID),
				Namespace: session.Namespace,
				Labels: map[string]string{
					"app":        "lab-environment",
					"session-id": sessionID,
				},
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{
					corev1.ReadWriteOnce,
				},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("5Gi"),
					},
				},
				StorageClassName: rb.storageClassName(),
			},
		},
	}

	return pvcs
}

// storageClassName returns the configured StorageClass, or nil so the cluster
// default is used
func (rb *ResourceBuilder) storageClassName() *string {
	if rb.settings.StorageClass == "" {
		return nil
	}
	return stringPtr(rb.settings.StorageClass)
}

// vmSize is the VM a session runs: vCPUs and memory for Firecracker, and the disk the rootfs is
// grown to.
type vmSize struct {
	CPUs      int
	MemoryMiB int64
	Disk      resource.Quantity
}

// containerMemory is what the VM container reserves and is limited to: the VM's memory plus
// Firecracker's own overhead.
func (s vmSize) containerMemory() resource.Quantity {
	return *resource.NewQuantity((s.MemoryMiB+firecrackerOverheadMiB)<<20, resource.BinarySI)
}

// vmSize reads the session's VM size from spec.resources (set from the lab by the API): cpu is
// the vCPU count (rounded up), memory the VM's memory, storage its disk. Missing or unparsable
// values fall back to the defaults (the disk to the controller's --vm-disk-size); CPU and
// memory are kept within [1, maxVMCPUs] and [minVMMemoryMiB, maxVMMemoryMiB].
func (rb *ResourceBuilder) vmSize(session *LabSession) vmSize {
	size := vmSize{CPUs: defaultVMCPUs, MemoryMiB: defaultVMMemoryMiB, Disk: rb.diskSize()}
	r := session.Spec.Resources
	if q, err := resource.ParseQuantity(r.CPU); err == nil && q.Sign() > 0 {
		size.CPUs = int((q.MilliValue() + 999) / 1000)
	}
	if q, err := resource.ParseQuantity(r.Memory); err == nil && q.Sign() > 0 {
		size.MemoryMiB = q.Value() >> 20
	}
	if q, err := resource.ParseQuantity(r.Storage); err == nil && q.Sign() > 0 {
		size.Disk = q
	}
	size.CPUs = min(max(size.CPUs, 1), maxVMCPUs)
	size.MemoryMiB = min(max(size.MemoryMiB, minVMMemoryMiB), maxVMMemoryMiB)
	return size
}

// Helper functions

// diskSize returns the configured VM disk size. PodSettings.Validate
// rejects malformed values, so the parse cannot fail for a validated config.
func (rb *ResourceBuilder) diskSize() resource.Quantity {
	return resource.MustParse(rb.settings.VMDiskSize)
}

// startupProbeBudgetSeconds is how long a lab container may take to first answer its
// startup probe before the kubelet restarts it.
const startupProbeBudgetSeconds = 120

// startupProbe checks every second until the container first answers, so the pod turns
// Ready as soon as the VM or sidecar is up. The kubelet runs the readiness probe as soon as
// the startup probe passes, so readiness probes can keep a slow period.
func startupProbe(handler corev1.ProbeHandler) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler:     handler,
		PeriodSeconds:    1,
		TimeoutSeconds:   1,
		FailureThreshold: startupProbeBudgetSeconds,
	}
}

// vmSSHProbe connects to sshd inside the VM
func vmSSHProbe() corev1.ProbeHandler {
	return corev1.ProbeHandler{
		TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(VMSSHPort)},
	}
}

// vmKernelsSizeLimit sizes the vm-kernels volume at twice the disk, which
// leaves room for the image while it is copied and resized.
func vmKernelsSizeLimit(disk resource.Quantity) resource.Quantity {
	return *resource.NewQuantity(disk.Value()*2, resource.BinarySI)
}

// resize2fsSize formats a size for resize2fs, which takes K/M/G suffixes
// rather than Kubernetes quantities. It rounds down to whole MiB.
func resize2fsSize(q resource.Quantity) string {
	return fmt.Sprintf("%dM", q.Value()>>20)
}

// sshKeyEnv sets env var name from one key of the session's SSH key Secret
func sshKeyEnv(name, sessionID, key string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: name,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: SSHKeySecretName(sessionID)},
				Key:                  key,
			},
		},
	}
}

func resourcePtr(q resource.Quantity) *resource.Quantity {
	return &q
}

func stringPtr(s string) *string {
	return &s
}
