// Package v1alpha1 defines the KoujaVolumePlacement CRD.
// +kubebuilder:object:generate=true
// +groupName=kouja.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KoujaVolumePlacement records the durable host assignment for a CSI volume.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
type KoujaVolumePlacement struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              KoujaVolumePlacementSpec   `json:"spec,omitempty"`
	Status            KoujaVolumePlacementStatus `json:"status,omitempty"`
}

type KoujaVolumePlacementSpec struct {
	PlacementKey   string `json:"placementKey"`
	CSIVolumeName  string `json:"csiVolumeName"`
	StorageClass   string `json:"storageClass"`
	HostUUID       string `json:"hostUuid"`
	VolumeID       string `json:"volumeId"`
	ResourceUUID   string `json:"resourceUuid,omitempty"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type KoujaVolumePlacementStatus struct {
	Phase     string      `json:"phase,omitempty"`
	Error     string      `json:"error,omitempty"`
	UpdatedAt metav1.Time `json:"updatedAt,omitempty"`
}

// +kubebuilder:object:root=true
type KoujaVolumePlacementList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KoujaVolumePlacement `json:"items"`
}
