package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// LabSession represents a lab environment session
type LabSession struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LabSessionSpec   `json:"spec,omitempty"`
	Status LabSessionStatus `json:"status,omitempty"`
}

// LabSessionSpec defines the desired state of a lab session
type LabSessionSpec struct {
	UserID    string                     `json:"userId"`
	SessionID string                     `json:"sessionId"`
	Resources LabSessionResourceRequests `json:"resources,omitempty"`
	Config    LabSessionConfig           `json:"config,omitempty"`
	Timeout   string                     `json:"timeout,omitempty"` // Duration string like "2h"
}

// LabSessionResourceRequests defines resource requirements
type LabSessionResourceRequests struct {
	Memory  string `json:"memory,omitempty"`
	CPU     string `json:"cpu,omitempty"`
	Storage string `json:"storage,omitempty"`
}

// LabSessionConfig defines configuration for the lab session
type LabSessionConfig struct {
	VSCodePassword string `json:"vsCodePassword,omitempty"`
	EnableTerminal bool   `json:"enableTerminal,omitempty"`
	EnableVSCode   bool   `json:"enableVSCode,omitempty"`
	EnableSSH      bool   `json:"enableSSH,omitempty"`
}

// LabSessionStatus defines the observed state of a lab session
type LabSessionStatus struct {
	Phase       LabSessionPhase       `json:"phase,omitempty"`
	Message     string                `json:"message,omitempty"`
	PodName     string                `json:"podName,omitempty"`
	ServiceName string                `json:"serviceName,omitempty"`
	Endpoints   LabSessionEndpoints   `json:"endpoints,omitempty"`
	VMIP        string                `json:"vmIP,omitempty"`
	PodIP       string                `json:"podIP,omitempty"`
	CreatedAt   *metav1.Time          `json:"createdAt,omitempty"`
	ExpiresAt   *metav1.Time          `json:"expiresAt,omitempty"`
	Conditions  []LabSessionCondition `json:"conditions,omitempty"`
}

// LabSessionPhase represents the current phase of the lab session
type LabSessionPhase string

const (
	LabSessionPhasePending     LabSessionPhase = "Pending"
	LabSessionPhaseCreating    LabSessionPhase = "Creating"
	LabSessionPhaseRunning     LabSessionPhase = "Running"
	LabSessionPhaseFailed      LabSessionPhase = "Failed"
	LabSessionPhaseTerminating LabSessionPhase = "Terminating"
	LabSessionPhaseTerminated  LabSessionPhase = "Terminated"
)

// LabSessionEndpoints defines the endpoints for accessing the lab session
type LabSessionEndpoints struct {
	VSCode   string `json:"vscode,omitempty"`
	Terminal string `json:"terminal,omitempty"`
	SSH      string `json:"ssh,omitempty"`
}

// LabSessionCondition defines a condition for the lab session
type LabSessionCondition struct {
	Type               LabSessionConditionType `json:"type"`
	Status             metav1.ConditionStatus  `json:"status"`
	LastTransitionTime metav1.Time             `json:"lastTransitionTime"`
	Reason             string                  `json:"reason,omitempty"`
	Message            string                  `json:"message,omitempty"`
}

// LabSessionConditionType defines the type of condition
type LabSessionConditionType string

const (
	LabSessionConditionReady       LabSessionConditionType = "Ready"
	LabSessionConditionPodReady    LabSessionConditionType = "PodReady"
	LabSessionConditionServiceReady LabSessionConditionType = "ServiceReady"
	LabSessionConditionVMReady     LabSessionConditionType = "VMReady"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// LabSessionList contains a list of LabSession
type LabSessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LabSession `json:"items"`
}