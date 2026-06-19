module github.com/zachperkins/rancher-audit-log-sandbox/operator

go 1.25

require (
	github.com/go-logr/logr v1.4.3
	github.com/go-logr/zapr v1.2.4
	go.uber.org/zap v1.28.0
	k8s.io/api v0.28.0
	k8s.io/apimachinery v0.28.0
	k8s.io/client-go v0.28.0
	sigs.k8s.io/controller-runtime v0.9.0
)
