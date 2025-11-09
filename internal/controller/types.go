package controller

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupVersion is the group version used to register LabSession CRD
var GroupVersion = schema.GroupVersion{Group: "dozlab.io", Version: "v1"}

// LabSessionSpec defines the desired state of a lab session
type LabSessionSpec struct {
	// UserID is the unique identifier for the user
	UserID string `json:"userId"`
	// SessionID is the unique session identifier
	SessionID string `json:"sessionId"`
	// LabID is the unique lab identifier (optional)
	LabID string `json:"labId,omitempty"`
	// Resources defines the compute resources for the lab
	Resources ResourceConfig `json:"resources,omitempty"`
	// Config defines the lab configuration
	Config SessionConfig `json:"config,omitempty"`
	// Timeout defines the session timeout duration (e.g., "2h", "30m")
	Timeout string `json:"timeout,omitempty"`
	// RootfsURL defines the custom rootfs image URL (optional)
	RootfsURL string `json:"rootfsUrl,omitempty"`
	// CustomImages allows overriding default container images
	CustomImages ImageConfig `json:"customImages,omitempty"`
}

// ResourceConfig defines compute resource requirements
type ResourceConfig struct {
	Memory  string `json:"memory,omitempty"`  // e.g., "4Gi"
	CPU     string `json:"cpu,omitempty"`     // e.g., "2" or "2000m"
	Storage string `json:"storage,omitempty"` // e.g., "10Gi"
}

// SessionConfig defines session-specific configuration
type SessionConfig struct {
	VSCodePassword string `json:"vsCodePassword,omitempty"`
	EnableTerminal bool   `json:"enableTerminal,omitempty"`
	EnableVSCode   bool   `json:"enableVSCode,omitempty"`
	EnableSSH      bool   `json:"enableSSH,omitempty"`
}

// ImageConfig allows customization of container images
type ImageConfig struct {
	InitrdImage   string `json:"initrdImage,omitempty"`
	TerminalImage string `json:"terminalImage,omitempty"`
	VSCodeImage   string `json:"vscodeImage,omitempty"`
}

// LabSessionStatus defines the observed state of a lab session
type LabSessionStatus struct {
	// Phase represents the current phase of the session
	// +kubebuilder:validation:Enum=Pending;Creating;Running;Failed;Terminating;Terminated
	Phase SessionPhase `json:"phase,omitempty"`
	// Message provides human-readable status information
	Message string `json:"message,omitempty"`
	// Reason provides a machine-readable status reason
	Reason string `json:"reason,omitempty"`
	// PodName is the name of the created pod
	PodName string `json:"podName,omitempty"`
	// ServiceName is the name of the created service
	ServiceName string `json:"serviceName,omitempty"`
	// PodIP is the internal IP address of the pod
	PodIP string `json:"podIP,omitempty"`
	// VMIP is the calculated IP address of the VM
	VMIP string `json:"vmIP,omitempty"`
	// Endpoints contains the accessible service endpoints
	Endpoints map[string]string `json:"endpoints,omitempty"`
	// Conditions represent the latest observations of the session's state
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// StartTime is when the session started
	StartTime *metav1.Time `json:"startTime,omitempty"`
	// LastTransitionTime is when the phase last changed
	LastTransitionTime *metav1.Time `json:"lastTransitionTime,omitempty"`
	// ObservedGeneration reflects the generation observed by the controller
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// SessionPhase defines the phase of a lab session
type SessionPhase string

const (
	// SessionPhasePending indicates the session is waiting to be scheduled
	SessionPhasePending SessionPhase = "Pending"
	// SessionPhaseCreating indicates resources are being created
	SessionPhaseCreating SessionPhase = "Creating"
	// SessionPhaseRunning indicates the session is running
	SessionPhaseRunning SessionPhase = "Running"
	// SessionPhaseFailed indicates the session failed to start
	SessionPhaseFailed SessionPhase = "Failed"
	// SessionPhaseTerminating indicates the session is being terminated
	SessionPhaseTerminating SessionPhase = "Terminating"
	// SessionPhaseTerminated indicates the session has been terminated
	SessionPhaseTerminated SessionPhase = "Terminated"
)

// Condition types
const (
	// ConditionTypeReady indicates the session is ready
	ConditionTypeReady = "Ready"
	// ConditionTypePodReady indicates the pod is ready
	ConditionTypePodReady = "PodReady"
	// ConditionTypeServiceReady indicates the service is ready
	ConditionTypeServiceReady = "ServiceReady"
	// ConditionTypeVMReady indicates the VM is ready
	ConditionTypeVMReady = "VMReady"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=labsession;ls
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="User",type=string,JSONPath=`.spec.userId`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// LabSession is the Schema for the labsessions API
type LabSession struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LabSessionSpec   `json:"spec,omitempty"`
	Status LabSessionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// LabSessionList contains a list of LabSession
type LabSessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LabSession `json:"items"`
}

// DeepCopyInto copies all properties of this object into another object of the same type
func (in *LabSession) DeepCopyInto(out *LabSession) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

// DeepCopy creates a deep copy of the LabSession
func (in *LabSession) DeepCopy() *LabSession {
	if in == nil {
		return nil
	}
	out := new(LabSession)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject creates a deep copy that implements runtime.Object
func (in *LabSession) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto copies all properties of LabSessionSpec
func (in *LabSessionSpec) DeepCopyInto(out *LabSessionSpec) {
	*out = *in
	out.Resources = in.Resources
	out.Config = in.Config
	out.CustomImages = in.CustomImages
}

// DeepCopyInto copies all properties of this object into another object of the same type
func (in *LabSessionList) DeepCopyInto(out *LabSessionList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]LabSession, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy creates a deep copy of the LabSessionList
func (in *LabSessionList) DeepCopy() *LabSessionList {
	if in == nil {
		return nil
	}
	out := new(LabSessionList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject creates a deep copy that implements runtime.Object
func (in *LabSessionList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto for LabSessionStatus
func (in *LabSessionStatus) DeepCopyInto(out *LabSessionStatus) {
	*out = *in
	if in.Endpoints != nil {
		in, out := &in.Endpoints, &out.Endpoints
		*out = make(map[string]string, len(*in))
		for key, val := range *in {
			(*out)[key] = val
		}
	}
	if in.Conditions != nil {
		in, out := &in.Conditions, &out.Conditions
		*out = make([]metav1.Condition, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	if in.StartTime != nil {
		in, out := &in.StartTime, &out.StartTime
		*out = (*in).DeepCopy()
	}
	if in.LastTransitionTime != nil {
		in, out := &in.LastTransitionTime, &out.LastTransitionTime
		*out = (*in).DeepCopy()
	}
}

// GetObjectKind returns the object kind
func (in *LabSession) GetObjectKind() schema.ObjectKind {
	return &in.TypeMeta
}

// GetObjectKind returns the object kind for list
func (in *LabSessionList) GetObjectKind() schema.ObjectKind {
	return &in.TypeMeta
}

// SchemeBuilder is used to add go types to the GroupVersionKind scheme
var SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

// AddToScheme adds the types in this group-version to the given scheme.
var AddToScheme = SchemeBuilder.AddToScheme

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&LabSession{},
		&LabSessionList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}
