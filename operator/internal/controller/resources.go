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

package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	rancherauditv1alpha1 "github.com/zachperkins/rancher-audit-log-sandbox/operator/api/v1alpha1"
)

const (
	finalizerName = "auditlogconfig.rancheraudit.io/finalizer"

	defaultSourceNamespace = "cattle-system"
	defaultSourceContainer = "rancher-audit-log"
	defaultAlloyImage      = "grafana/alloy:v1.17.0"

	// jobLabel is the Loki stream label every audit entry carries. The Grafana
	// dashboard and example LogQL queries key off {job="rancher-audit"}.
	jobLabel = "rancher-audit"
)

// applyDefaults fills unset spec fields. Mirrors the +kubebuilder:default markers
// so the controller behaves correctly even when applied without the CRD defaults
// (e.g. partial objects in tests).
func applyDefaults(cr *rancherauditv1alpha1.AuditLogConfig) {
	s := &cr.Spec.Source
	if s.Namespace == "" {
		s.Namespace = defaultSourceNamespace
	}
	if s.Container == "" {
		s.Container = defaultSourceContainer
	}
	if len(s.PodSelector) == 0 {
		s.PodSelector = map[string]string{"app": "rancher"}
	}
	if cr.Spec.Alloy.Image == "" {
		cr.Spec.Alloy.Image = defaultAlloyImage
	}
}

// resourceName is the deterministic name for all child objects of a CR.
func resourceName(cr *rancherauditv1alpha1.AuditLogConfig) string {
	return "audit-shipper-" + cr.Name
}

// childLabels are applied to every owned object; the selector subset is used by
// the Deployment.
func childLabels(cr *rancherauditv1alpha1.AuditLogConfig) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "rancher-audit-shipper",
		"app.kubernetes.io/instance":   cr.Name,
		"app.kubernetes.io/managed-by": "rancher-audit-log-operator",
	}
}

func selectorLabels(cr *rancherauditv1alpha1.AuditLogConfig) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":     "rancher-audit-shipper",
		"app.kubernetes.io/instance": cr.Name,
	}
}

// buildAlloyConfig renders the Grafana Alloy (River) pipeline that tails the
// Rancher audit-log sidecar via the Kubernetes API, labels the streams, and
// pushes to Loki. Field names match Rancher's audit JSON (method, responseCode);
// high-cardinality fields like user.name are left in the line for `| json` at
// query time rather than promoted to labels.
func buildAlloyConfig(cr *rancherauditv1alpha1.AuditLogConfig) string {
	var b strings.Builder

	fmt.Fprintf(&b, `discovery.kubernetes "rancher" {
  role = "pod"
  namespaces {
    names = [%q]
  }
  selectors {
    role  = "pod"
    label = %q
  }
}

discovery.relabel "audit" {
  targets = discovery.kubernetes.rancher.targets

  // Keep only the audit-log sidecar container.
  rule {
    source_labels = ["__meta_kubernetes_pod_container_name"]
    regex         = %q
    action        = "keep"
  }
  rule {
    source_labels = ["__meta_kubernetes_namespace"]
    target_label  = "namespace"
  }
  rule {
    source_labels = ["__meta_kubernetes_pod_name"]
    target_label  = "pod"
  }
  rule {
    source_labels = ["__meta_kubernetes_pod_container_name"]
    target_label  = "container"
  }
  rule {
    target_label = "job"
    replacement  = %q
  }
}

loki.source.kubernetes "audit" {
  targets    = discovery.relabel.audit.output
  forward_to = [loki.process.audit.receiver]
}

loki.process "audit" {
  forward_to = [loki.write.default.receiver]

  // Parse the Rancher audit JSON. "actor" is the Rancher local/login username
  // (user.extra.username) when present, falling back to the principal (user.name)
  // for system accounts. Only low-cardinality fields are promoted to labels; the
  // full JSON stays in the line for query-time sentence formatting.
  stage.json {
    expressions = {
      method       = "method",
      responseCode = "responseCode",
      login        = "user.extra.username[0]",
      principal    = "user.name",
    }
  }
  stage.template {
    source   = "actor"
    template = "{{ if .login }}{{ .login }}{{ else if .principal }}{{ .principal }}{{ else }}unknown{{ end }}"
  }
  stage.labels {
    values = {
      method       = "",
      responseCode = "",
      actor        = "",
    }
  }
}

`, cr.Spec.Source.Namespace, labelSelectorString(cr.Spec.Source.PodSelector),
		cr.Spec.Source.Container, jobLabel)

	b.WriteString("loki.write \"default\" {\n  endpoint {\n")
	fmt.Fprintf(&b, "    url = %q\n", cr.Spec.Loki.URL)
	if cr.Spec.Loki.Tenant != "" {
		fmt.Fprintf(&b, "    tenant_id = %q\n", cr.Spec.Loki.Tenant)
	}
	if cr.Spec.Loki.BasicAuthSecretRef != "" {
		b.WriteString("    basic_auth {\n")
		b.WriteString("      username = sys.env(\"LOKI_USERNAME\")\n")
		b.WriteString("      password = sys.env(\"LOKI_PASSWORD\")\n")
		b.WriteString("    }\n")
	}
	b.WriteString("  }\n")

	if len(cr.Spec.Loki.ExternalLabels) > 0 {
		b.WriteString("  external_labels = {\n")
		for _, k := range sortedKeys(cr.Spec.Loki.ExternalLabels) {
			fmt.Fprintf(&b, "    %s = %q,\n", k, cr.Spec.Loki.ExternalLabels[k])
		}
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")

	return b.String()
}

func labelSelectorString(sel map[string]string) string {
	parts := make([]string, 0, len(sel))
	for _, k := range sortedKeys(sel) {
		parts = append(parts, fmt.Sprintf("%s=%s", k, sel[k]))
	}
	return strings.Join(parts, ",")
}

// configHash is a short content hash of the Alloy config, used to roll the
// Deployment when the rendered pipeline changes.
func configHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- object builders (names/namespaces only; specs are set in mutate fns) ---

func newConfigMap(cr *rancherauditv1alpha1.AuditLogConfig) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: resourceName(cr), Namespace: cr.Namespace,
	}}
}

func newServiceAccount(cr *rancherauditv1alpha1.AuditLogConfig) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name: resourceName(cr), Namespace: cr.Namespace,
	}}
}

// Role/RoleBinding live in the source namespace (cattle-system) so Alloy can read
// pod logs there. They are managed without owner refs (cross-namespace) and cleaned
// up via the finalizer.
func newRole(cr *rancherauditv1alpha1.AuditLogConfig) *rbacv1.Role {
	return &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{
		Name: resourceName(cr), Namespace: cr.Spec.Source.Namespace,
	}}
}

func newRoleBinding(cr *rancherauditv1alpha1.AuditLogConfig) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{
		Name: resourceName(cr), Namespace: cr.Spec.Source.Namespace,
	}}
}

func newDeployment(cr *rancherauditv1alpha1.AuditLogConfig) *appsv1.Deployment {
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: resourceName(cr), Namespace: cr.Namespace,
	}}
}
