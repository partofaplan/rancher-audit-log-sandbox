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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AuditLogConfigSpec defines the desired state of AuditLogConfig.
//
// An AuditLogConfig describes one export pipeline: scrape the Rancher audit-log
// sidecar's stdout and ship the parsed entries to a Loki instance. The operator
// reconciles a Grafana Alloy deployment (+ConfigMap +RBAC) to fulfill it.
type AuditLogConfigSpec struct {
	// Loki is the destination to push audit logs to.
	// +required
	Loki LokiSpec `json:"loki"`

	// Source selects which pods/container produce the Rancher audit JSON.
	// Defaults target a standard Rancher Manager install.
	// +optional
	Source SourceSpec `json:"source,omitempty"`

	// Alloy tunes the Grafana Alloy log-shipper deployment.
	// +optional
	Alloy AlloySpec `json:"alloy,omitempty"`
}

// LokiSpec describes the Loki push destination.
type LokiSpec struct {
	// URL is the Loki push endpoint, e.g. http://192.168.64.1/loki/api/v1/push
	// +required
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`

	// Tenant sets the X-Scope-OrgID header for multi-tenant Loki. Optional.
	// +optional
	Tenant string `json:"tenant,omitempty"`

	// BasicAuthSecretRef names a Secret (same namespace as the CR) with keys
	// "username" and "password" for Loki basic auth. Optional.
	// +optional
	BasicAuthSecretRef string `json:"basicAuthSecretRef,omitempty"`

	// ExternalLabels are static labels added to every shipped stream,
	// e.g. {cluster: rancher-desktop}. Keep these low-cardinality.
	// +optional
	ExternalLabels map[string]string `json:"externalLabels,omitempty"`
}

// SourceSpec selects the audit-log source pods/container.
type SourceSpec struct {
	// Namespace of the Rancher pods. Default: cattle-system.
	// +optional
	// +kubebuilder:default=cattle-system
	Namespace string `json:"namespace,omitempty"`

	// PodSelector matches the Rancher pods. Default: {app: rancher}.
	// +optional
	PodSelector map[string]string `json:"podSelector,omitempty"`

	// Container is the audit-log sidecar name. Default: rancher-audit-log.
	// +optional
	// +kubebuilder:default=rancher-audit-log
	Container string `json:"container,omitempty"`
}

// AlloySpec tunes the shipper deployment.
type AlloySpec struct {
	// Image is the Grafana Alloy image. Default: grafana/alloy:v1.17.0.
	// +optional
	// +kubebuilder:default="grafana/alloy:v1.17.0"
	Image string `json:"image,omitempty"`

	// Resources for the Alloy container. Optional.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// AuditLogConfigStatus defines the observed state of AuditLogConfig.
type AuditLogConfigStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the AuditLogConfig resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Ready is true when the Alloy shipper has been reconciled successfully.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// ObservedGeneration is the spec generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=alc
// +kubebuilder:printcolumn:name="Loki",type=string,JSONPath=`.spec.loki.url`
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AuditLogConfig is the Schema for the auditlogconfigs API
type AuditLogConfig struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of AuditLogConfig
	// +required
	Spec AuditLogConfigSpec `json:"spec"`

	// status defines the observed state of AuditLogConfig
	// +optional
	Status AuditLogConfigStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// AuditLogConfigList contains a list of AuditLogConfig
type AuditLogConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AuditLogConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AuditLogConfig{}, &AuditLogConfigList{})
}
