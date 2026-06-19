package controllers

import (
	context "context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	auditregistrationv1 "k8s.io/api/auditregistration/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	auditv1alpha1 "github.com/zachperkins/rancher-audit-log-sandbox/operator/api/v1alpha1"
)

const (
	finalizerName = "auditlogconfig.rancheraudit.io/finalizer"
)

// AuditLogConfigReconciler reconciles an AuditLogConfig object.
type AuditLogConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=rancheraudit.io,resources=auditlogconfigs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rancheraudit.io,resources=auditlogconfigs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=rancheraudit.io,resources=auditlogconfigs/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=auditregistration.k8s.io,resources=auditsinks,verbs=get;list;watch;create;update;patch;delete

func (r *AuditLogConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var config auditv1alpha1.AuditLogConfig
	if err := r.Get(ctx, req.NamespacedName, &config); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if config.ObjectMeta.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(&config, finalizerName) {
			controllerutil.AddFinalizer(&config, finalizerName)
			if err := r.Update(ctx, &config); err != nil {
				return ctrl.Result{}, err
			}
		}
	} else {
		if controllerutil.ContainsFinalizer(&config, finalizerName) {
			if err := r.cleanupResources(ctx, &config); err != nil {
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(&config, finalizerName)
			if err := r.Update(ctx, &config); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if err := r.reconcileForwarder(ctx, &config); err != nil {
		logger.Error(err, "failed to reconcile forwarder")
		return ctrl.Result{RequeueAfter: 15 * time.Second}, err
	}

	if err := r.reconcileAuditSink(ctx, &config); err != nil {
		logger.Error(err, "failed to reconcile audit sink")
		return ctrl.Result{RequeueAfter: 15 * time.Second}, err
	}

	config.Status.Ready = true
	if err := r.Status().Update(ctx, &config); err != nil {
		logger.Error(err, "unable to update AuditLogConfig status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *AuditLogConfigReconciler) reconcileForwarder(ctx context.Context, config *auditv1alpha1.AuditLogConfig) error {
	logger := log.FromContext(ctx)
	logger.Info("reconciling forwarder resources")

	deploymentName := fmt.Sprintf("audit-forwarder-%s", config.Name)
	serviceAccountName := fmt.Sprintf("audit-forwarder-sa-%s", config.Name)
	serviceName := fmt.Sprintf("audit-forwarder-svc-%s", config.Name)
	labels := map[string]string{"app": "audit-forwarder", "auditlogconfig": config.Name}

	replicas := int32(1)
	if config.Spec.Forwarder.Replicas != nil {
		replicas = *config.Spec.Forwarder.Replicas
	}

	image := config.Spec.Forwarder.Image
	if image == "" {
		image = "rancher-audit-log-operator:latest"
	}

	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceAccountName,
			Namespace: config.Namespace,
			Labels:    labels,
		},
	}
	if err := controllerutil.SetControllerReference(config, serviceAccount, r.Scheme); err != nil {
		return err
	}
	if err := r.createOrUpdate(ctx, serviceAccount); err != nil {
		return err
	}

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("audit-forwarder-cm-%s", config.Name),
			Namespace: config.Namespace,
			Labels:    labels,
		},
		Data: map[string]string{"LOKI_URL": config.Spec.Loki.URL, "LOKI_TENANT": config.Spec.Loki.Tenant},
	}
	if err := controllerutil.SetControllerReference(config, configMap, r.Scheme); err != nil {
		return err
	}
	if err := r.createOrUpdate(ctx, configMap); err != nil {
		return err
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: config.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					ServiceAccountName: serviceAccountName,
					Containers: []corev1.Container{
						{
							Name:  "audit-forwarder",
							Image: image,
							Ports: []corev1.ContainerPort{{ContainerPort: 8080}},
							Env: []corev1.EnvVar{
								{Name: "PORT", Value: "8080"},
								{Name: "LOKI_URL", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{Key: "LOKI_URL", LocalObjectReference: corev1.LocalObjectReference{Name: configMap.Name}}}},
								{Name: "LOKI_TENANT", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{Key: "LOKI_TENANT", LocalObjectReference: corev1.LocalObjectReference{Name: configMap.Name}}}},
							},
							Resources: config.Spec.Forwarder.Resources,
						},
					},
				},
			},
		},
	}

	if config.Spec.Loki.BasicAuthSecretRef != "" {
		deployment.Spec.Template.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: config.Spec.Loki.BasicAuthSecretRef}}}}
	}

	if err := controllerutil.SetControllerReference(config, deployment, r.Scheme); err != nil {
		return err
	}
	if err := r.createOrUpdate(ctx, deployment); err != nil {
		return err
	}

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: config.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports:    []corev1.ServicePort{{Port: 8080, TargetPort: intstr.FromInt(8080), Protocol: corev1.ProtocolTCP}},
		},
	}
	if err := controllerutil.SetControllerReference(config, service, r.Scheme); err != nil {
		return err
	}
	if err := r.createOrUpdate(ctx, service); err != nil {
		return err
	}

	return nil
}

func (r *AuditLogConfigReconciler) reconcileAuditSink(ctx context.Context, config *auditv1alpha1.AuditLogConfig) error {
	logger := log.FromContext(ctx)
	logger.Info("reconciling audit sink")

	auditSink := &auditregistrationv1.AuditSink{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("audit-sink-%s", config.Name),
		},
		Spec: auditregistrationv1.AuditSinkSpec{
			Policy: auditregistrationv1.Policy{
				Level: auditregistrationv1.LevelRequestResponse,
			},
			Webhook: auditregistrationv1.Webhook{Service: &auditregistrationv1.ServiceReference{
				Name:      fmt.Sprintf("audit-forwarder-svc-%s", config.Name),
				Namespace: config.Namespace,
				Port:      8080,
			}},
		},
	}

	if len(config.Spec.AuditLevels) > 0 {
		auditSink.Spec.Policy.Level = auditregistrationv1.Level(config.Spec.AuditLevels[0])
	}

	if err := controllerutil.SetControllerReference(config, auditSink, r.Scheme); err != nil {
		return err
	}

	var existing auditregistrationv1.AuditSink
	if err := r.Get(ctx, client.ObjectKey{Name: auditSink.Name}, &existing); err != nil {
		if errors.IsNotFound(err) {
			return r.Create(ctx, auditSink)
		}
		return err
	}
	auditSink.ResourceVersion = existing.ResourceVersion
	return r.Update(ctx, auditSink)
}

func (r *AuditLogConfigReconciler) cleanupResources(ctx context.Context, config *auditv1alpha1.AuditLogConfig) error {
	logger := log.FromContext(ctx)
	logger.Info("cleaning up resources for finalizer")

	auditSink := &auditregistrationv1.AuditSink{}
	if err := r.Get(ctx, client.ObjectKey{Name: fmt.Sprintf("audit-sink-%s", config.Name)}, auditSink); err == nil {
		if err := r.Delete(ctx, auditSink); err != nil {
			return err
		}
	}

	return nil
}

func (r *AuditLogConfigReconciler) createOrUpdate(ctx context.Context, obj client.Object) error {
	var existing client.Object
	switch obj.(type) {
	case *corev1.ServiceAccount:
		existing = &corev1.ServiceAccount{}
	case *rbacv1.ClusterRole:
		existing = &rbacv1.ClusterRole{}
	case *rbacv1.ClusterRoleBinding:
		existing = &rbacv1.ClusterRoleBinding{}
	case *corev1.ConfigMap:
		existing = &corev1.ConfigMap{}
	case *appsv1.Deployment:
		existing = &appsv1.Deployment{}
	case *corev1.Service:
		existing = &corev1.Service{}
	default:
		return fmt.Errorf("unsupported object type %T", obj)
	}

	if err := r.Get(ctx, client.ObjectKeyFromObject(obj), existing); err != nil {
		if errors.IsNotFound(err) {
			return r.Create(ctx, obj)
		}
		return err
	}

	obj.SetResourceVersion(existing.GetResourceVersion())
	return r.Update(ctx, obj)
}

func (r *AuditLogConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&auditv1alpha1.AuditLogConfig{}).
		Complete(r)
}
