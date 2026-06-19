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
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
// Alloy reads pod logs in the source namespace; the operator must be able to grant that.
// +kubebuilder:rbac:groups=core,resources=pods;pods/log,verbs=get;list;watch

// Reconcile drives the actual cluster state toward the AuditLogConfig spec by
// reconciling a Grafana Alloy shipper (ConfigMap + ServiceAccount + Role/Binding +
// Deployment) that tails the Rancher audit-log sidecar and pushes to Loki.
func (r *AuditLogConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	cr := &rancherauditv1alpha1.AuditLogConfig{}
	if err := r.Get(ctx, req.NamespacedName, cr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	applyDefaults(cr)

	// Handle deletion: tear down the cross-namespace RBAC, then drop the finalizer.
	if !cr.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(cr, finalizerName) {
			if err := r.cleanupSourceRBAC(ctx, cr); err != nil {
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

	r.setReady(ctx, cr, true, "Reconciled", "Alloy shipper is configured")
	return ctrl.Result{}, nil
}

// reconcileShipper creates/updates every child object.
func (r *AuditLogConfigReconciler) reconcileShipper(ctx context.Context, cr *rancherauditv1alpha1.AuditLogConfig) error {
	// Render the Alloy config once; its hash is stamped on the Deployment so a
	// config change rolls the pod (mounted ConfigMaps don't trigger restarts).
	cfg := buildAlloyConfig(cr)
	cfgHash := configHash(cfg)

	// ConfigMap (CR namespace, owned).
	cm := newConfigMap(cr)
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		cm.Labels = childLabels(cr)
		cm.Data = map[string]string{"config.alloy": cfg}
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

	// Role + RoleBinding in the source namespace (cross-namespace, no owner ref).
	role := newRole(cr)
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, role, func() error {
		role.Labels = childLabels(cr)
		role.Rules = []rbacv1.PolicyRule{{
			APIGroups: []string{""},
			Resources: []string{"pods", "pods/log"},
			Verbs:     []string{"get", "list", "watch"},
		}}
		return nil
	}); err != nil {
		return fmt.Errorf("role: %w", err)
	}

	rb := newRoleBinding(cr)
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, rb, func() error {
		rb.Labels = childLabels(cr)
		rb.RoleRef = rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     role.Name,
		}
		rb.Subjects = []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      sa.Name,
			Namespace: cr.Namespace,
		}}
		return nil
	}); err != nil {
		return fmt.Errorf("rolebinding: %w", err)
	}

	// Deployment (CR namespace, owned).
	dep := newDeployment(cr)
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, dep, func() error {
		mutateDeployment(cr, sa.Name, cm.Name, cfgHash, dep)
		return controllerutil.SetControllerReference(cr, dep, r.Scheme)
	}); err != nil {
		return fmt.Errorf("deployment: %w", err)
	}

	return nil
}

func mutateDeployment(cr *rancherauditv1alpha1.AuditLogConfig, saName, cmName, cfgHash string, dep *appsv1.Deployment) {
	replicas := int32(1)
	dep.Labels = childLabels(cr)

	container := corev1.Container{
		Name:  "alloy",
		Image: cr.Spec.Alloy.Image,
		Args: []string{
			"run",
			"--server.http.listen-addr=0.0.0.0:12345",
			"--storage.path=/var/lib/alloy/data",
			"/etc/alloy/config.alloy",
		},
		Resources: cr.Spec.Alloy.Resources,
		Ports:     []corev1.ContainerPort{{Name: "http", ContainerPort: 12345}},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "config", MountPath: "/etc/alloy"},
			{Name: "data", MountPath: "/var/lib/alloy/data"},
		},
	}
	if cr.Spec.Loki.BasicAuthSecretRef != "" {
		container.Env = []corev1.EnvVar{
			{Name: "LOKI_USERNAME", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: cr.Spec.Loki.BasicAuthSecretRef}, Key: "username",
			}}},
			{Name: "LOKI_PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: cr.Spec.Loki.BasicAuthSecretRef}, Key: "password",
			}}},
		}
	}

	dep.Spec.Replicas = &replicas
	dep.Spec.Selector = &metav1.LabelSelector{MatchLabels: selectorLabels(cr)}
	dep.Spec.Template = corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      childLabels(cr),
			Annotations: map[string]string{"rancheraudit.io/config-hash": cfgHash},
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: saName,
			Containers:         []corev1.Container{container},
			Volumes: []corev1.Volume{
				{Name: "config", VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: cmName}},
				}},
				{Name: "data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			},
		},
	}
}

// cleanupSourceRBAC removes the Role/RoleBinding created in the source namespace,
// which cannot carry an owner reference back to the (different-namespace) CR.
func (r *AuditLogConfigReconciler) cleanupSourceRBAC(ctx context.Context, cr *rancherauditv1alpha1.AuditLogConfig) error {
	for _, obj := range []client.Object{newRoleBinding(cr), newRole(cr)} {
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
		Owns(&appsv1.Deployment{}).
		Named("auditlogconfig").
		Complete(r)
}
