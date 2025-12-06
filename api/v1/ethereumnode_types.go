package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NodeType defines the role and purpose of the Ethereum node.
// The operator infers specific configurations (like syncMode, sidecars, and API limits) based on this type.
// The possible values are:
// - full: A standard node that maintains current state and verifies blocks (implies syncMode: snap).
// - archive: A node that retains all historical state data for querying the past (implies syncMode: archive).
// - validator: A node that participates in Proof-of-Stake consensus (auto-injects the Validator Client sidecar).
// - gateway: A node optimized for high-volume JSON-RPC traffic (implies tuned API limits for dApps).
// +kubebuilder:validation:Enum=full;archive;validator;gateway
type NodeType string

const (
	NodeTypeFull      NodeType = "full"
	NodeTypeArchive   NodeType = "archive"
	NodeTypeValidator NodeType = "validator"
	NodeTypeGateway   NodeType = "gateway"
)

// EthereumNodeSpec defines the desired state of EthereumNode
type EthereumNodeSpec struct {
	// NodeType defines the type of Ethereum node.
	// It determines the node's role and configuration.
	// Possible values: full, archive, validator, gateway.
	// +kubebuilder:validation:Enum=full;archive;validator;gateway
	// +kubebuilder:default=full
	NodeType NodeType `json:"nodeType"`

	// Network defines the blockchain network (e.g., mainnet, sepolia).
	// +kubebuilder:validation:Enum=mainnet;sepolia;holesky
	Network string `json:"network"`

	// SyncMode is optional. If it's not set, the operator infers the synchronization strategy based on the NodeType.
	// +optional
	SyncMode string `json:"syncMode,omitempty"`

	// Replicas defines how many nodes of this type we want.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	Replicas int32 `json:"replicas"`

	// JWTSecretName refers to the k8s Secret used for Engine API authentication.
	JWTSecretName string `json:"jwtSecretName,omitempty"`

	// StorageSize defines the disk size. E.g.: "500Gi"
	// +kubebuilder:default="20Gi"
	StorageSize string `json:"storageSize,omitempty"`

	// StorageClassName defines the disk type (e.g., gp3, local-path, standard).
	// If empty, the cluster default is used.
	StorageClassName string `json:"storageClassName,omitempty"`

	// CheckpointSyncURL defines the URL of a trusted beacon node for fast synchronization.
	// E.g.: "https://beaconstate-sepolia.chainsafe.io"
	// +optional
	CheckpointSyncURL string `json:"checkpointSyncURL,omitempty"`

	// Resources defines CPU and RAM limits for the containers.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// EthereumNodeStatus defines the observed state of EthereumNode
type EthereumNodeStatus struct {
	// ActiveReplicas counts how many Pods are 'Ready'.
	ActiveReplicas int32 `json:"activeReplicas"`

	// Conditions represents the current state of the resource.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// EthereumNode is the Schema for the ethereumnodes API
type EthereumNode struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EthereumNodeSpec   `json:"spec,omitempty"`
	Status EthereumNodeStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// EthereumNodeList contains a list of EthereumNode
type EthereumNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EthereumNode `json:"items"`
}

func init() {
	SchemeBuilder.Register(&EthereumNode{}, &EthereumNodeList{})
}
