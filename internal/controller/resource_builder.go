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

// ResourceBuilder builds Kubernetes resources for lab sessions
type ResourceBuilder struct{}

// NewResourceBuilder creates a new resource builder
func NewResourceBuilder() *ResourceBuilder {
	return &ResourceBuilder{}
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
			InitContainers: []corev1.Container{
				rb.buildIPCalculatorContainer(),
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

// buildIPCalculatorContainer creates the init container for IP calculation
func (rb *ResourceBuilder) buildIPCalculatorContainer() corev1.Container {
	return corev1.Container{
		Name:  "ip-calculator",
		Image: "busybox",
		Command: []string{"sh", "-c"},
		Args: []string{`
			echo "Calculating VM IP from pod IP..."
			POD_IP=$(hostname -i)
			echo "Pod IP: $POD_IP"

			# Calculate VM IP (pod IP + 1)
			VM_IP=$(echo $POD_IP | awk -F. '{$4=$4+1; print $1"."$2"."$3"."$4}')
			echo "VM IP will be: $VM_IP"

			# Write to shared config
			echo "POD_IP=$POD_IP" > /shared/network-config
			echo "VM_IP=$VM_IP" >> /shared/network-config

			echo "Network configuration written to /shared/network-config"
			cat /shared/network-config
		`},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "shared-config",
				MountPath: "/shared",
			},
		},
	}
}

// buildVMContainer creates the VM container
func (rb *ResourceBuilder) buildVMContainer(session *LabSession, resourceLimits ResourceConfig) corev1.Container {
	sessionID := session.Spec.SessionID
	vmImage := "your-initrd:latest"
	if session.Spec.CustomImages.InitrdImage != "" {
		vmImage = session.Spec.CustomImages.InitrdImage
	}

	return corev1.Container{
		Name:  "initrd-vm",
		Image: vmImage,
		SecurityContext: &corev1.SecurityContext{
			Privileged:               boolPtr(false),
			AllowPrivilegeEscalation: boolPtr(false),
			RunAsNonRoot:             boolPtr(true),
			RunAsUser:                int64Ptr(1000),
			ReadOnlyRootFilesystem:   boolPtr(true),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
				Add:  []corev1.Capability{"NET_ADMIN", "SYS_ADMIN"},
			},
		},
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse(resourceLimits.Memory),
				corev1.ResourceCPU:    resource.MustParse(resourceLimits.CPU),
			},
			Requests: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse(getMemoryRequest(resourceLimits.Memory)),
				corev1.ResourceCPU:    resource.MustParse(getCPURequest(resourceLimits.CPU)),
			},
		},
		Env: []corev1.EnvVar{
			{Name: "SESSION_ID", Value: sessionID},
			{
				Name: "POD_IP",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "status.podIP",
					},
				},
			},
		},
		Command: []string{"sh", "-c"},
		Args: []string{`
			source /shared/network-config
			export VM_IP=$VM_IP
			echo "VM will use IP: $VM_IP"
			# Start your initrd/firecracker process here
			exec your-initrd-startup-script
		`},
		Ports: []corev1.ContainerPort{
			{ContainerPort: 22, Name: "vm-ssh"},
			{ContainerPort: 9090, Name: "metrics"},
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{
					Port: intstr.FromInt(22),
				},
			},
			InitialDelaySeconds: 30,
			PeriodSeconds:       10,
			TimeoutSeconds:      5,
			FailureThreshold:    3,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{
					Port: intstr.FromInt(22),
				},
			},
			InitialDelaySeconds: 5,
			PeriodSeconds:       5,
			TimeoutSeconds:      3,
			FailureThreshold:    3,
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "vm-data", MountPath: "/vm-data"},
			{Name: "shared-config", MountPath: "/shared"},
		},
	}
}

// buildTerminalContainer creates the terminal sidecar container
func (rb *ResourceBuilder) buildTerminalContainer(session *LabSession) corev1.Container {
	sessionID := session.Spec.SessionID
	terminalImage := "your-terminal-sidecar:latest"
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
			{
				Name: "POD_IP",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "status.podIP",
					},
				},
			},
		},
		Command: []string{"sh", "-c"},
		Args: []string{`
			source /shared/network-config
			export VM_IP=$VM_IP
			echo "Terminal sidecar will connect to VM at: $VM_IP"
			exec ./terminal-sidecar
		`},
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
			{Name: "shared-config", MountPath: "/shared"},
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
				EmptyDir: &corev1.EmptyDirVolumeSource{},
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

func boolPtr(b bool) *bool {
	return &b
}

func int64Ptr(i int64) *int64 {
	return &i
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
