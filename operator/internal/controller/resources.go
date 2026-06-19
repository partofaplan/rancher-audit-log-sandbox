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
	"sort"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	rancherauditv1alpha1 "github.com/zachperkins/rancher-audit-log-sandbox/operator/api/v1alpha1"
)

const (
	finalizerName = "auditlogconfig.rancheraudit.io/finalizer"

	defaultSourceNamespace = "cattle-system"
	defaultSourceContainer = "rancher-audit-log"
	defaultFilebeatImage   = "docker.elastic.co/beats/filebeat:8.17.3"
	defaultIndex           = "rancher-audit"
)

// auditScriptJS runs in Filebeat's javascript processor. It derives, from the
// decoded Rancher audit JSON (under the "rancher" field), an audit.* summary:
//   - actor: the local/login username (user.extra.username) falling back to the
//     principal (user.name), then "unknown";
//   - verb: mapped from the HTTP method (POST->create, DELETE->delete, PUT/PATCH->
//     update, GET->get, POST?action=...->invoke);
//   - resource/target: the kind and namespace/name parsed from requestURI;
//   - summary: the readable "actor verb resource (target)" sentence.
const auditScriptJS = `function process(event) {
    var login = "";
    var arr = event.Get("rancher.user.extra.username");
    if (arr && arr.length > 0) { login = arr[0]; }
    var principal = event.Get("rancher.user.name");
    var actor = login ? login : (principal ? principal : "unknown");

    var method = event.Get("rancher.method") || "";
    var verbs = {POST: "create", DELETE: "delete", PUT: "update", PATCH: "update", GET: "get"};
    var verb = verbs[method] || method.toLowerCase();
    var rawuri = event.Get("rancher.requestURI") || "";
    if (method === "POST" && rawuri.indexOf("action=") >= 0) { verb = "invoke"; }

    var uri = rawuri.split("?")[0];
    var tail = uri.replace(/^.*\/v[0-9]+[a-z0-9]*(-public)?\//, "").replace(/^\//, "");
    var rtype = tail.split("/")[0];
    var dot = rtype.lastIndexOf(".");
    var kind = dot >= 0 ? rtype.substring(dot + 1) : rtype;
    var slash = tail.indexOf("/");
    var target = slash >= 0 ? tail.substring(slash + 1) : "";

    event.Put("audit.actor", actor);
    event.Put("audit.verb", verb);
    event.Put("audit.resource", kind);
    event.Put("audit.target", target);
    event.Put("audit.responseCode", event.Get("rancher.responseCode"));
    event.Put("audit.summary", actor + " " + verb + " " + kind + (target ? " " + target : ""));
}`

// applyDefaults fills unset spec fields. Mirrors the +kubebuilder:default markers.
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
	if cr.Spec.Filebeat.Image == "" {
		cr.Spec.Filebeat.Image = defaultFilebeatImage
	}
	if cr.Spec.Elasticsearch.Index == "" {
		cr.Spec.Elasticsearch.Index = defaultIndex
	}
}

// resourceName is the deterministic name for namespaced child objects.
func resourceName(cr *rancherauditv1alpha1.AuditLogConfig) string {
	return "audit-shipper-" + cr.Name
}

// clusterResourceName is the name for cluster-scoped child objects (ClusterRole/
// Binding); it includes the namespace to stay unique cluster-wide.
func clusterResourceName(cr *rancherauditv1alpha1.AuditLogConfig) string {
	return "audit-shipper-" + cr.Namespace + "-" + cr.Name
}

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

// buildFilebeatConfig renders filebeat.yml: autodiscover the Rancher audit-log
// container on this node, decode its JSON, derive the audit.* sentence fields, and
// ship to Elasticsearch. Built as structured data and YAML-marshaled so the embedded
// JS and dotted keys are escaped correctly.
func buildFilebeatConfig(cr *rancherauditv1alpha1.AuditLogConfig) (string, error) {
	es := cr.Spec.Elasticsearch

	conds := []interface{}{
		map[string]interface{}{"equals": map[string]interface{}{"kubernetes.namespace": cr.Spec.Source.Namespace}},
		map[string]interface{}{"equals": map[string]interface{}{"kubernetes.container.name": cr.Spec.Source.Container}},
	}
	for _, k := range sortedKeys(cr.Spec.Source.PodSelector) {
		conds = append(conds, map[string]interface{}{
			"equals": map[string]interface{}{"kubernetes.labels." + k: cr.Spec.Source.PodSelector[k]},
		})
	}

	esOut := map[string]interface{}{
		"hosts": []string{es.Host},
		"index": es.Index,
	}
	if es.PathPrefix != "" {
		esOut["path"] = es.PathPrefix
	}
	if es.BasicAuthSecretRef != "" {
		esOut["username"] = "${ES_USERNAME}"
		esOut["password"] = "${ES_PASSWORD}"
	}

	cfg := map[string]interface{}{
		"filebeat.autodiscover": map[string]interface{}{
			"providers": []interface{}{
				map[string]interface{}{
					"type": "kubernetes",
					"node": "${NODE_NAME}",
					"templates": []interface{}{
						map[string]interface{}{
							"condition": map[string]interface{}{"and": conds},
							"config": []interface{}{
								map[string]interface{}{
									"type":  "container",
									"paths": []string{"/var/log/containers/*-${data.kubernetes.container.id}.log"},
								},
							},
						},
					},
				},
			},
		},
		"processors": []interface{}{
			map[string]interface{}{"decode_json_fields": map[string]interface{}{
				"fields":         []string{"message"},
				"target":         "rancher",
				"overwrite_keys": true,
				"add_error_key":  true,
			}},
			map[string]interface{}{"script": map[string]interface{}{
				"lang":   "javascript",
				"source": auditScriptJS,
			}},
		},
		"output.elasticsearch": esOut,
		// Write to a plain, auto-created index (no Filebeat-managed template / ILM /
		// data stream) — simplest for a sandbox. ES auto-creates the index on first write.
		"setup.template.enabled": false,
		"setup.ilm.enabled":      false,
		"logging.level":          "info",
	}

	b, err := yaml.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// configHash is a short content hash of the Filebeat config, used to roll the
// DaemonSet when the rendered pipeline changes.
func configHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

// --- object builders (names/namespaces only; specs are set in mutate fns) ---

func newConfigMap(cr *rancherauditv1alpha1.AuditLogConfig) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: resourceName(cr), Namespace: cr.Namespace}}
}

func newServiceAccount(cr *rancherauditv1alpha1.AuditLogConfig) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: resourceName(cr), Namespace: cr.Namespace}}
}

// ClusterRole/Binding are cluster-scoped (Filebeat autodiscover reads pods/namespaces/
// nodes cluster-wide). They carry no owner ref and are cleaned up via the finalizer.
func newClusterRole(cr *rancherauditv1alpha1.AuditLogConfig) *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: clusterResourceName(cr)}}
}

func newClusterRoleBinding(cr *rancherauditv1alpha1.AuditLogConfig) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: clusterResourceName(cr)}}
}

func newDaemonSet(cr *rancherauditv1alpha1.AuditLogConfig) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: resourceName(cr), Namespace: cr.Namespace}}
}

// hostPathType returns a pointer to t (helper for volume sources).
func hostPathType(t corev1.HostPathType) *corev1.HostPathType { return &t }
