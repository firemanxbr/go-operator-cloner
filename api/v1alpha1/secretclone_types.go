/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	SecretCloneConditionReady             = "Ready"
	SecretCloneConditionSourceSecretReady = "SourceSecretReady"
	SecretCloneConditionTargetsSynced     = "TargetsSynced"
)

// SecretReference identifies the source Secret that should be cloned.
type SecretReference struct {
	// Namespace is where the source Secret lives.
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`

	// Name is the source Secret name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// SecretCloneSpec defines the desired state of SecretClone.
type SecretCloneSpec struct {
	// SourceSecretRef points at the Secret that should be copied.
	SourceSecretRef SecretReference `json:"sourceSecretRef"`

	// TargetSecretName overrides the name used in target namespaces.
	// When empty, the operator reuses the source Secret name.
	// +optional
	// +kubebuilder:validation:MinLength=1
	TargetSecretName string `json:"targetSecretName,omitempty"`

	// NamespaceSelector restricts which namespaces receive the cloned Secret.
	// When omitted, every namespace except the source namespace is eligible.
	// +optional
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`

	// ExcludedNamespaces are always skipped even if they match the selector.
	// +optional
	// +listType=set
	ExcludedNamespaces []string `json:"excludedNamespaces,omitempty"`
}

// SecretCloneStatus defines the observed state of SecretClone.
type SecretCloneStatus struct {
	// Conditions reflect the most recent reconciliation state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration tracks the latest processed generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ObservedSourceResourceVersion tracks which source Secret revision was copied.
	// +optional
	ObservedSourceResourceVersion string `json:"observedSourceResourceVersion,omitempty"`

	// SyncedNamespaces is the number of namespaces that currently have the managed Secret.
	// +optional
	SyncedNamespaces int32 `json:"syncedNamespaces,omitempty"`

	// LastSyncTime is when the last reconciliation attempt completed.
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=sclone
// +kubebuilder:printcolumn:name="Source",type="string",JSONPath=".spec.sourceSecretRef.name"
// +kubebuilder:printcolumn:name="SourceNamespace",type="string",JSONPath=".spec.sourceSecretRef.namespace"
// +kubebuilder:printcolumn:name="SyncedNamespaces",type="integer",JSONPath=".status.syncedNamespaces"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status"

// SecretClone is the Schema for the secretclones API.
type SecretClone struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SecretCloneSpec   `json:"spec"`
	Status            SecretCloneStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SecretCloneList contains a list of SecretClone.
type SecretCloneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SecretClone `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SecretClone{}, &SecretCloneList{})
}
