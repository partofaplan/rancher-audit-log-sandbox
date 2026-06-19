package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AuditLogConfigSpec defines the desired state of AuditLogConfig.
type AuditLogConfigSpec struct {
	// Loki contains destination settings for Loki.
	Loki LokiSpec `json:"loki"`

	// Forwarder contains settings for the audit webhook forwarder deployment.
	// +optional
	Forwarder ForwarderSpec `json:"forwarder,omitempty"`

	// AuditLevels defines the audit event levels sent to the webhook.
	// Example: ["RequestResponse", "Request"]
	// +optional
	AuditLevels []string `json:"auditLevels,omitempty"`
}

// LokiSpec defines the Loki destination settings.
type LokiSpec struct {
	// URL is the full HTTP endpoint for Loki push, for example https://loki.example.com/loki/api/v1/push.
	URL string `json:"url"`

	// Tenant is an optional Loki tenant ID.
	// +optional
	Tenant string `json:"tenant,omitempty"`

	// BasicAuthSecretRef is the name of an existing secret containing
	// username and password fields for Loki basic auth.
	// +optional
	BasicAuthSecretRef string `json:"basicAuthSecretRef,omitempty"`
}

// ForwarderSpec configures the audit webhook forwarder.
type ForwarderSpec struct {
	// Image is the container image used by the forwarder deployment.
	// +optional
	Image string `json:"image,omitempty"`

	// Replicas is the number of forwarder replicas.
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Resources defines resource requests and limits for the forwarder.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// AuditLogConfigStatus defines the observed state of AuditLogConfig.
type AuditLogConfigStatus struct {
	// Ready indicates whether the audit forwarder and sink are configured.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// Conditions represent the state of the AuditLogConfig.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// AuditLogConfig is the Schema for the audit log configuration API.
type AuditLogConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AuditLogConfigSpec   `json:"spec,omitempty"`
	Status AuditLogConfigStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// AuditLogConfigList contains a list of AuditLogConfig.
type AuditLogConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AuditLogConfig `json:"items"`
}
