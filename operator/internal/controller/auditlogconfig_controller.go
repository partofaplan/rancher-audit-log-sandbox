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
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	rancherauditv1alpha1 "github.com/zachperkins/rancher-audit-log-sandbox/operator/api/v1alpha1"
)

// AuditLogConfigReconciler reconciles a AuditLogConfig object
type AuditLogConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=rancheraudit.io,resources=auditlogconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rancheraudit.io,resources=auditlogconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=rancheraudit.io,resources=auditlogconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=serviceaccounts;configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
// Filebeat autodiscover reads these cluster-wide; the operator must hold them to grant them.
// +kubebuilder:rbac:groups=core,resources=pods;namespaces;nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list;watch

// Reconcile drives the actual cluster state toward the AuditLogConfig spec by
// reconciling a Filebeat shipper (ConfigMap + ServiceAccount + ClusterRole/Binding +
// DaemonSet) that tails the Rancher audit-log sidecar and ships to Elasticsearch.
func (r *AuditLogConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	cr := &rancherauditv1alpha1.AuditLogConfig{}
	if err := r.Get(ctx, req.NamespacedName, cr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	applyDefaults(cr)

	// Handle deletion: tear down the cluster-scoped RBAC, then drop the finalizer.
	if !cr.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(cr, finalizerName) {
			if err := r.cleanupClusterRBAC(ctx, cr); err != nil {
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(cr, finalizerName)
			if err := r.Update(ctx, cr); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(cr, finalizerName) {
		if err := r.Update(ctx, cr); err != nil {
			return ctrl.Result{}, err
		}
		// Re-queue with the updated object on the next pass.
		return ctrl.Result{Requeue: true}, nil
	}

	if err := r.reconcileShipper(ctx, cr); err != nil {
		// Conflicts are expected when the Owns watches fire mid-reconcile during the
		// initial create-storm; requeue quietly rather than logging a stack trace.
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		log.Error(err, "reconcile failed")
		r.setReady(ctx, cr, false, "ReconcileFailed", err.Error())
		return ctrl.Result{}, err
	}

	r.setReady(ctx, cr, true, "Reconciled", "Filebeat shipper is configured")
	return ctrl.Result{}, nil
}

// reconcileShipper creates/updates every child object.
func (r *AuditLogConfigReconciler) reconcileShipper(ctx context.Context, cr *rancherauditv1alpha1.AuditLogConfig) error {
	// Render the Filebeat config once; its hash is stamped on the DaemonSet so a
	// config change rolls the pods (mounted ConfigMaps don't trigger restarts).
	cfg, err := buildFilebeatConfig(cr)
	if err != nil {
		return fmt.Errorf("render config: %w", err)
	}
	cfgHash := configHash(cfg)

	// ConfigMap (CR namespace, owned).
	cm := newConfigMap(cr)
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		cm.Labels = childLabels(cr)
		cm.Data = map[string]string{"filebeat.yml": cfg}
		return controllerutil.SetControllerReference(cr, cm, r.Scheme)
	}); err != nil {
		return fmt.Errorf("configmap: %w", err)
	}

	// ServiceAccount (CR namespace, owned).
	sa := newServiceAccount(cr)
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, sa, func() error {
		sa.Labels = childLabels(cr)
		return controllerutil.SetControllerReference(cr, sa, r.Scheme)
	}); err != nil {
		return fmt.Errorf("serviceaccount: %w", err)
	}

	// ClusterRole + ClusterRoleBinding (cluster-scoped, no owner ref → finalizer cleanup).
	clusterRole := newClusterRole(cr)
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, clusterRole, func() error {
		clusterRole.Labels = childLabels(cr)
		clusterRole.Rules = []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"pods", "namespaces", "nodes"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"apps"}, Resources: []string{"replicasets"}, Verbs: []string{"get", "list", "watch"}},
		}
		return nil
	}); err != nil {
		return fmt.Errorf("clusterrole: %w", err)
	}

	crb := newClusterRoleBinding(cr)
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, crb, func() error {
		crb.Labels = childLabels(cr)
		crb.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: clusterRole.Name}
		crb.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Name: sa.Name, Namespace: cr.Namespace}}
		return nil
	}); err != nil {
		return fmt.Errorf("clusterrolebinding: %w", err)
	}

	// DaemonSet (CR namespace, owned).
	ds := newDaemonSet(cr)
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, ds, func() error {
		mutateDaemonSet(cr, sa.Name, cm.Name, cfgHash, ds)
		return controllerutil.SetControllerReference(cr, ds, r.Scheme)
	}); err != nil {
		return fmt.Errorf("daemonset: %w", err)
	}

	return nil
}

func mutateDaemonSet(cr *rancherauditv1alpha1.AuditLogConfig, saName, cmName, cfgHash string, ds *appsv1.DaemonSet) {
	ds.Labels = childLabels(cr)
	rootUID := int64(0)

	container := corev1.Container{
		Name:  "filebeat",
		Image: cr.Spec.Filebeat.Image,
		Args:  []string{"-e", "-c", "/etc/filebeat/filebeat.yml", "--strict.perms=false"},
		Env: []corev1.EnvVar{
			{Name: "NODE_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"}}},
		},
		Resources:       cr.Spec.Filebeat.Resources,
		SecurityContext: &corev1.SecurityContext{RunAsUser: &rootUID},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "config", MountPath: "/etc/filebeat", ReadOnly: true},
			{Name: "data", MountPath: "/usr/share/filebeat/data"},
			{Name: "varlogcontainers", MountPath: "/var/log/containers", ReadOnly: true},
			{Name: "varlogpods", MountPath: "/var/log/pods", ReadOnly: true},
			// On dockerd nodes (rancher-desktop), /var/log/pods/.../0.log symlinks into
			// here; on containerd nodes this is empty and the pod logs are read directly.
			{Name: "varlibdockercontainers", MountPath: "/var/lib/docker/containers", ReadOnly: true},
		},
	}
	if cr.Spec.Elasticsearch.BasicAuthSecretRef != "" {
		ref := cr.Spec.Elasticsearch.BasicAuthSecretRef
		container.Env = append(container.Env,
			corev1.EnvVar{Name: "ES_USERNAME", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: ref}, Key: "username"}}},
			corev1.EnvVar{Name: "ES_PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: ref}, Key: "password"}}},
		)
	}

	ds.Spec.Selector = &metav1.LabelSelector{MatchLabels: selectorLabels(cr)}
	ds.Spec.Template = corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      childLabels(cr),
			Annotations: map[string]string{"rancheraudit.io/config-hash": cfgHash},
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: saName,
			// Run on every node, including tainted control-plane nodes (rancher-desktop is one).
			Tolerations: []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
			Containers:  []corev1.Container{container},
			Volumes: []corev1.Volume{
				{Name: "config", VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: cmName}}}},
				{Name: "data", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
					Path: "/var/lib/" + resourceName(cr) + "-data", Type: hostPathType(corev1.HostPathDirectoryOrCreate)}}},
				{Name: "varlogcontainers", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
					Path: "/var/log/containers"}}},
				{Name: "varlogpods", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
					Path: "/var/log/pods"}}},
				{Name: "varlibdockercontainers", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
					Path: "/var/lib/docker/containers", Type: hostPathType(corev1.HostPathDirectoryOrCreate)}}},
			},
		},
	}
}

// cleanupClusterRBAC removes the cluster-scoped ClusterRole/ClusterRoleBinding, which
// cannot carry an owner reference back to the namespaced CR.
func (r *AuditLogConfigReconciler) cleanupClusterRBAC(ctx context.Context, cr *rancherauditv1alpha1.AuditLogConfig) error {
	for _, obj := range []client.Object{newClusterRoleBinding(cr), newClusterRole(cr)} {
		if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// setReady updates the Ready condition/flag, ignoring conflicts on a best-effort basis.
func (r *AuditLogConfigReconciler) setReady(ctx context.Context, cr *rancherauditv1alpha1.AuditLogConfig, ready bool, reason, msg string) {
	log := logf.FromContext(ctx)

	// Re-fetch to avoid status update conflicts after spec updates (finalizer).
	latest := &rancherauditv1alpha1.AuditLogConfig{}
	if err := r.Get(ctx, types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}, latest); err != nil {
		return
	}

	status := metav1.ConditionTrue
	if !ready {
		status = metav1.ConditionFalse
	}
	meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
		Type:    "Ready",
		Status:  status,
		Reason:  reason,
		Message: msg,
	})
	latest.Status.Ready = ready
	latest.Status.ObservedGeneration = latest.Generation

	if err := r.Status().Update(ctx, latest); err != nil {
		log.V(1).Info("status update failed", "error", err.Error())
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *AuditLogConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&rancherauditv1alpha1.AuditLogConfig{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&appsv1.DaemonSet{}).
		Named("auditlogconfig").
		Complete(r)
}
