package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EthereumNodeSpec defines the desired state of EthereumNode
type EthereumNodeSpec struct {
	// Network defines the blockchain network (e.g., mainnet, sepolia).
	// +kubebuilder:validation:Enum=mainnet;sepolia;holesky
	Network string `json:"network"`

	// SyncMode defines the synchronization strategy.
	// +kubebuilder:validation:Enum=snap;full;archive
	// +kubebuilder:default=snap
	SyncMode string `json:"syncMode"`

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
