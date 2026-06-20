#!/usr/bin/env bash
# Manually test the shipper pipeline WITHOUT Elasticsearch.
#
# It takes the EXACT Filebeat config the operator generated for an AuditLogConfig, swaps the
# Elasticsearch output for a console output, and runs it as a throwaway pod — so you can see
# the enriched audit.* events (e.g. the audit.summary sentences) in `kubectl logs`. No ES/ELK.
#
#   ./console-test.sh [CR_NAME] [NAMESPACE]            # run the test pod and tail its logs
#   ./console-test.sh --cleanup [CR_NAME] [NAMESPACE]  # remove the test pod + configmap
#
# Defaults: CR_NAME=rancher-audit  NAMESPACE=rancher-audit-system  (override for your install).
# Requires: kubectl access to the cluster, and an AuditLogConfig already reconciled by the
# operator (so its ConfigMap/ServiceAccount exist).
set -euo pipefail

CLEANUP=0; [ "${1:-}" = "--cleanup" ] && { CLEANUP=1; shift; }
CR="${1:-rancher-audit}"
NS="${2:-rancher-audit-system}"
KUBECTL="${KUBECTL:-kubectl}"
CM="audit-shipper-${CR}"; SA="audit-shipper-${CR}"; DS="audit-shipper-${CR}"
TEST="audit-console-test-${CR}"

if [ "${CLEANUP}" = 1 ]; then
  ${KUBECTL} -n "${NS}" delete pod/"${TEST}" configmap/"${TEST}" --ignore-not-found
  exit 0
fi

IMAGE="$(${KUBECTL} -n "${NS}" get ds "${DS}" -o jsonpath='{.spec.template.spec.containers[0].image}')"
echo ">> deriving console config from configmap ${CM} (image ${IMAGE})"

# Swap the elasticsearch output for a console output; drop the setup.* keys.
TMP="$(mktemp)"
${KUBECTL} -n "${NS}" get cm "${CM}" -o jsonpath='{.data.filebeat\.yml}' | awk '
/^[A-Za-z]/ {                                   # a top-level key
  if ($0 ~ /^output\.elasticsearch:/) { drop=1; next }
  if ($0 ~ /^setup\./)                { next }  # setup.* are single-line keys
  drop=0                                        # any other top-level key ends the dropped block
}
!drop { print }
END { print "output.console:"; print "  pretty: true" }
' > "${TMP}"

${KUBECTL} -n "${NS}" create configmap "${TEST}" --from-file=filebeat.yml="${TMP}" \
  --dry-run=client -o yaml | ${KUBECTL} -n "${NS}" apply -f -
rm -f "${TMP}"

cat <<EOF | ${KUBECTL} -n "${NS}" apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${TEST}
  labels: { app: audit-console-test }
spec:
  serviceAccountName: ${SA}
  terminationGracePeriodSeconds: 5
  tolerations: [{ operator: Exists }]
  containers:
    - name: filebeat
      image: ${IMAGE}
      args: ["-e", "-c", "/etc/filebeat/filebeat.yml", "--strict.perms=false"]
      securityContext: { runAsUser: 0 }
      env:
        - { name: NODE_NAME, valueFrom: { fieldRef: { fieldPath: spec.nodeName } } }
      volumeMounts:
        - { name: config, mountPath: /etc/filebeat, readOnly: true }
        - { name: data, mountPath: /usr/share/filebeat/data }
        - { name: varlogcontainers, mountPath: /var/log/containers, readOnly: true }
        - { name: varlogpods, mountPath: /var/log/pods, readOnly: true }
        - { name: varlibdockercontainers, mountPath: /var/lib/docker/containers, readOnly: true }
  volumes:
    - { name: config, configMap: { name: ${TEST} } }
    - { name: data, emptyDir: {} }
    - { name: varlogcontainers, hostPath: { path: /var/log/containers } }
    - { name: varlogpods, hostPath: { path: /var/log/pods } }
    - { name: varlibdockercontainers, hostPath: { path: /var/lib/docker/containers, type: DirectoryOrCreate } }
EOF

echo ">> waiting for the test pod…"
${KUBECTL} -n "${NS}" wait --for=condition=Ready pod/"${TEST}" --timeout=90s || true
echo ">> tailing enriched events (Ctrl-C to stop; then: $0 --cleanup ${CR} ${NS})"
${KUBECTL} -n "${NS}" logs -f "${TEST}"
