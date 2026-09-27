package controller

import (
	"fmt"
	"strconv"
	"strings"

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
	vmCPUCount      = "1"
	vmMemoryMiB     = "1024"
	ipForwardSysctl = "net.ipv4.ip_forward"

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

	// Get resource limits with defaults
	resourceLimits := rb.getResourceLimits(session.Spec.Resources)

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
				rb.buildRootfsInitContainer(session),
				rb.buildNetworkSetupContainer(),
			},
			Containers: []corev1.Container{
				rb.buildVMContainer(session, resourceLimits),
				rb.buildTerminalContainer(session),
				rb.buildVSCodeContainer(session),
			},
			Volumes: rb.buildVolumes(sessionID),
		},
	}

	return pod
}

// buildRootfsInitContainer writes the VM rootfs into the vm-kernels volume and grows it
func (rb *ResourceBuilder) buildRootfsInitContainer(session *LabSession) corev1.Container {
	return corev1.Container{
		Name:  "init-rootfs",
		Image: rb.settings.InitImage,
		Env: []corev1.EnvVar{
			{Name: "IMAGE_DOWNLOAD_URL", Value: session.Spec.RootfsURL},
			{Name: "IMAGE_SIZE", Value: resize2fsSize(rb.diskSize())},
			{Name: "IMAGE_PATH", Value: VMRootfsPath},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "vm-kernels", MountPath: VMKernelsPath},
		},
	}
}

// buildNetworkSetupContainer writes the VM network settings for the sidecars
func (rb *ResourceBuilder) buildNetworkSetupContainer() corev1.Container {
	return corev1.Container{
		Name:    "network-setup",
		Image:   "busybox:latest",
		Command: []string{"sh", "-c"},
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
func (rb *ResourceBuilder) buildVMContainer(session *LabSession, resourceLimits ResourceConfig) corev1.Container {
	sessionID := session.Spec.SessionID
	vmImage := rb.settings.VMImage
	if session.Spec.CustomImages.InitrdImage != "" {
		vmImage = session.Spec.CustomImages.InitrdImage
	}

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
				corev1.ResourceMemory: resource.MustParse(resourceLimits.Memory),
				corev1.ResourceCPU:    resource.MustParse(resourceLimits.CPU),
				KVMResource:           resource.MustParse("1"),
				TUNResource:           resource.MustParse("1"),
			},
			Requests: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse(getMemoryRequest(resourceLimits.Memory)),
				corev1.ResourceCPU:    resource.MustParse(getCPURequest(resourceLimits.CPU)),
				KVMResource:           resource.MustParse("1"),
				TUNResource:           resource.MustParse("1"),
			},
		},
		Env: []corev1.EnvVar{
			{Name: "SESSION_ID", Value: sessionID},
			{Name: "ROOTFS_PATH", Value: VMRootfsPath},
			{Name: "KERNEL_PATH", Value: VMKernelPath},
			{Name: "CPU_COUNT", Value: vmCPUCount},
			{Name: "MEMORY", Value: vmMemoryMiB},
			{Name: "GATEWAY_IP", Value: VMGatewayIP},
			{Name: "VM_IP", Value: VMIP},
			{Name: "TAP_DEVICE_NAME", Value: VMTapDevice},
		},
		Ports: []corev1.ContainerPort{
			{ContainerPort: VMSSHPort, Name: "vm-ssh"},
		},
		// sshd inside the VM, reached through start-firecracker.sh's DNAT on the pod IP
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{
					Port: intstr.FromInt(VMSSHPort),
				},
			},
			InitialDelaySeconds: 10,
			PeriodSeconds:       5,
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "vm-kernels", MountPath: VMKernelsPath},
			{Name: "vm-data", MountPath: "/vm-data"},
			{Name: "shared-config", MountPath: "/shared", ReadOnly: true},
		},
	}
}

// buildTerminalContainer creates the terminal sidecar container
func (rb *ResourceBuilder) buildTerminalContainer(session *LabSession) corev1.Container {
	sessionID := session.Spec.SessionID
	terminalImage := rb.settings.TerminalImage
	if session.Spec.CustomImages.TerminalImage != "" {
		terminalImage = session.Spec.CustomImages.TerminalImage
	}

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
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/health",
					Port: intstr.FromInt(8081),
				},
			},
			InitialDelaySeconds: 5,
			PeriodSeconds:       5,
			TimeoutSeconds:      3,
			FailureThreshold:    3,
		},
		Env: []corev1.EnvVar{
			{Name: "SESSION_ID", Value: sessionID},
			{Name: "VM_IP", Value: VMIP},
			{Name: "VM_SSH_PORT", Value: strconv.Itoa(VMSSHPort)},
			{Name: "SSH_USER", Value: rb.settings.SSHUser},
			{
				Name: "SSH_PRIVATE_KEY",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: rb.settings.SSHKeySecret},
						Key:                  rb.settings.SSHKeySecretKey,
					},
				},
			},
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
			{Name: "vm-data", MountPath: "/vm-data", ReadOnly: true},
			{Name: "shared-config", MountPath: "/shared", ReadOnly: true},
		},
	}
}

// buildVSCodeContainer creates the VS Code sidecar container
func (rb *ResourceBuilder) buildVSCodeContainer(session *LabSession) corev1.Container {
	sessionID := session.Spec.SessionID
	vscodeImage := "codercom/code-server:latest"
	if session.Spec.CustomImages.VSCodeImage != "" {
		vscodeImage = session.Spec.CustomImages.VSCodeImage
	}

	password := "changeme"
	if session.Spec.Config.VSCodePassword != "" {
		password = session.Spec.Config.VSCodePassword
	}

	return corev1.Container{
		Name:  "code-server",
		Image: vscodeImage,
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
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/healthz",
					Port: intstr.FromInt(8080),
				},
			},
			InitialDelaySeconds: 5,
			PeriodSeconds:       5,
			TimeoutSeconds:      3,
			FailureThreshold:    3,
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
func (rb *ResourceBuilder) buildVolumes(sessionID string) []corev1.Volume {
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
				EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: resourcePtr(vmKernelsSizeLimit(rb.diskSize()))},
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
				StorageClassName: stringPtr("default"),
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
				StorageClassName: stringPtr("default"),
			},
		},
	}

	return pvcs
}

// getResourceLimits returns resource limits with defaults and max enforcement
func (rb *ResourceBuilder) getResourceLimits(requested ResourceConfig) ResourceConfig {
	limits := ResourceConfig{
		Memory: "4Gi",
		CPU:    "2",
	}

	// Apply requested limits
	if requested.Memory != "" {
		limits.Memory = requested.Memory
	}
	if requested.CPU != "" {
		limits.CPU = requested.CPU
	}

	// Enforce maximum limits
	if memoryGB := parseMemoryGi(limits.Memory); memoryGB > 16 {
		limits.Memory = "16Gi"
	}
	if cpuCores := parseCPU(limits.CPU); cpuCores > 8 {
		limits.CPU = "8"
	}

	return limits
}

// Helper functions

// diskSize returns the configured VM disk size. PodSettings.Validate
// rejects malformed values, so the parse cannot fail for a validated config.
func (rb *ResourceBuilder) diskSize() resource.Quantity {
	return resource.MustParse(rb.settings.VMDiskSize)
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

func resourcePtr(q resource.Quantity) *resource.Quantity {
	return &q
}

func stringPtr(s string) *string {
	return &s
}

func getMemoryRequest(limit string) string {
	// Return 75% of limit as request
	if strings.HasSuffix(limit, "Gi") {
		if val, err := strconv.Atoi(strings.TrimSuffix(limit, "Gi")); err == nil {
			return fmt.Sprintf("%dGi", val*3/4)
		}
	}
	return "3Gi" // default
}

func getCPURequest(limit string) string {
	// Return 50% of limit as request
	if val, err := strconv.Atoi(limit); err == nil {
		return fmt.Sprintf("%d", val/2)
	}
	return "1" // default
}

func parseMemoryGi(memory string) int {
	if strings.HasSuffix(memory, "Gi") {
		if val, err := strconv.Atoi(strings.TrimSuffix(memory, "Gi")); err == nil {
			return val
		}
	}
	return 4 // default
}

func parseCPU(cpu string) int {
	if val, err := strconv.Atoi(cpu); err == nil {
		return val
	}
	return 2 // default
}
