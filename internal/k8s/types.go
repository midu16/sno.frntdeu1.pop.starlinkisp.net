// Package k8s holds minimal Kubernetes object projections used by the
// installer's machine-config remediation logic. Only the fields the logic
// actually reads are modeled; everything else is ignored on decode.
package k8s

// Condition is a generic status condition.
type Condition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// ObjectMeta is the metadata projection.
type ObjectMeta struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
}

// ClusterOperator is the config.openshift.io/v1 ClusterOperator projection.
type ClusterOperator struct {
	ObjectMeta ObjectMeta `json:"metadata"`
	Status     struct {
		Conditions []Condition `json:"conditions"`
	} `json:"status"`
}

// MachineConfigPool is the machineconfiguration.openshift.io/v1 MachineConfigPool projection.
type MachineConfigPool struct {
	ObjectMeta ObjectMeta `json:"metadata"`
	Spec       struct {
		Configuration struct {
			Name string `json:"name"`
		} `json:"configuration"`
	} `json:"spec"`
	Status struct {
		MachineCount            *int32      `json:"machineCount"`
		UpdatedMachineCount     *int32      `json:"updatedMachineCount"`
		UnavailableMachineCount *int32      `json:"unavailableMachineCount"`
		DegradedMachineCount    *int32      `json:"degradedMachineCount"`
		Conditions              []Condition `json:"conditions"`
	} `json:"status"`
}

// K8sNode is the core/v1 Node projection.
type K8sNode struct {
	ObjectMeta ObjectMeta `json:"metadata"`
}

// K8sPod is the core/v1 Pod projection.
type K8sPod struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Status struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

// FileSpec is a machine-config storage file entry.
type FileSpec struct {
	Path     string `json:"path"`
	Contents struct {
		Source string `json:"source"`
	} `json:"contents"`
	Mode *int32 `json:"mode"`
}

// MachineConfig is the machineconfiguration.openshift.io/v1 MachineConfig projection.
type MachineConfig struct {
	ObjectMeta ObjectMeta `json:"metadata"`
	Spec       struct {
		Config struct {
			Storage struct {
				Files []FileSpec `json:"files"`
			} `json:"storage"`
		} `json:"config"`
	} `json:"spec"`
}
