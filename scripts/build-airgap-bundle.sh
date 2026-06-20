#!/usr/bin/env bash
# Assemble a self-contained air-gap bundle: container images (saved as tarballs), the Helm
# chart, the ELK-team handoff package, docs, and load/install scripts — all in one tar.gz to
# carry into an air-gapped environment.
#
#   ./scripts/build-airgap-bundle.sh
#   INCLUDE_FILEBEAT=0 ./scripts/build-airgap-bundle.sh    # skip Filebeat (mirror it yourself)
#   VERSION=0.1.0 FILEBEAT_TAG=8.17.3 OUT=dist ./scripts/build-airgap-bundle.sh
#
# Produces dist/rancher-audit-log-airgap-<version>.tar.gz. Images are linux/amd64.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${REPO_ROOT}"

CHART_DIR="charts/rancher-audit-log-operator"
VERSION="${VERSION:-$(grep -E '^version:' "${CHART_DIR}/Chart.yaml" | awk '{print $2}')}"
OPERATOR_REPO="${OPERATOR_REPO:-rancher-audit-log-operator}"
OPERATOR_IMAGE="${OPERATOR_REPO}:${VERSION}"
FILEBEAT_TAG="${FILEBEAT_TAG:-8.17.3}"
FILEBEAT_IMAGE="docker.elastic.co/beats/filebeat:${FILEBEAT_TAG}"
INCLUDE_FILEBEAT="${INCLUDE_FILEBEAT:-1}"
PLATFORM="linux/amd64"
OUT="${OUT:-dist}"

NAME="rancher-audit-log-airgap-${VERSION}"
STAGE="$(mktemp -d)/${NAME}"
mkdir -p "${STAGE}/images" "${STAGE}/chart" "${STAGE}/docs"
echo ">> Staging ${NAME} (operator ${OPERATOR_IMAGE}, filebeat ${FILEBEAT_TAG}, platform ${PLATFORM})"

# 1. Operator image (build amd64, then save)
echo ">> Building operator image (${PLATFORM})"
IMG="${OPERATOR_IMAGE}" PLATFORM="${PLATFORM}" bash operator/build-image.sh >/dev/null
docker save "${OPERATOR_IMAGE}" -o "${STAGE}/images/operator-${VERSION}-amd64.tar"

# 2. Filebeat image (amd64) — prefer skopeo (platform-clean, host-arch-independent);
#    fall back to docker pull+save (works on an amd64 host / CI runner).
if [ "${INCLUDE_FILEBEAT}" = "1" ]; then
  FB_TAR="${STAGE}/images/filebeat-${FILEBEAT_TAG}-amd64.tar"
  if command -v skopeo >/dev/null 2>&1; then
    echo ">> Copying ${FILEBEAT_IMAGE} (${PLATFORM}) via skopeo — this is the large one"
    skopeo copy --override-os linux --override-arch amd64 \
      "docker://${FILEBEAT_IMAGE}" "docker-archive:${FB_TAR}:${FILEBEAT_IMAGE}"
  else
    echo ">> Pulling + saving ${FILEBEAT_IMAGE} (${PLATFORM}) — this is the large one"
    docker pull --platform "${PLATFORM}" "${FILEBEAT_IMAGE}" >/dev/null
    if ! docker save "${FILEBEAT_IMAGE}" -o "${FB_TAR}"; then
      echo "ERROR: 'docker save' of the amd64 image failed — common on an arm64 Docker Desktop" >&2
      echo "       (cross-arch pull leaves a missing manifest digest). Options:" >&2
      echo "         • install skopeo and re-run, or" >&2
      echo "         • build on an amd64 host / via the air-gap-bundle GitHub workflow, or" >&2
      echo "         • INCLUDE_FILEBEAT=0 and mirror Filebeat separately (docs/air-gap.md)." >&2
      exit 1
    fi
  fi
fi

# images manifest (source ref -> tar file)
{
  echo "${OPERATOR_IMAGE}	images/operator-${VERSION}-amd64.tar"
  [ "${INCLUDE_FILEBEAT}" = "1" ] && echo "${FILEBEAT_IMAGE}	images/filebeat-${FILEBEAT_TAG}-amd64.tar"
} > "${STAGE}/images/images.txt"

# 3. Helm chart (packaged)
echo ">> Packaging Helm chart"
helm package "${CHART_DIR}" -d "${STAGE}/chart" >/dev/null

# 4. ELK-team handoff + docs
cp -R elk-integration "${STAGE}/elk-integration"
cp docs/air-gap.md docs/enable-rancher-audit.md "${STAGE}/docs/"

# 5. load-images.sh — load + retag + push into the internal registry
cat > "${STAGE}/load-images.sh" <<'LOAD'
#!/usr/bin/env bash
# Load the bundled images and push them to your internal registry.
#   ./load-images.sh registry.internal/rancher-audit
set -euo pipefail
REG="${1:?usage: ./load-images.sh <registry-prefix>  e.g. registry.internal/rancher-audit}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
while IFS=$'\t' read -r src tar; do
  [ -z "${src:-}" ] && continue
  echo ">> loading ${src}"
  docker load -i "${HERE}/${tar}"
  # destination = <REG>/<path-after-registry-host>
  dest="${REG}/${src#*/}"
  docker tag "${src}" "${dest}"
  echo ">> pushing ${dest}"
  docker push "${dest}"
done < "${HERE}/images/images.txt"
echo ">> Done. Set image.registry=${REG} when installing the chart."
LOAD
chmod +x "${STAGE}/load-images.sh"

# 6. values-example.yaml — fill in your registry + the ELK team's endpoint/creds
cat > "${STAGE}/values-example.yaml" <<EOF
# Edit, then: helm install rancher-audit chart/rancher-audit-log-operator-${VERSION}.tgz \\
#   -n rancher-audit-system --create-namespace -f values-example.yaml
image:
  registry: registry.internal/rancher-audit   # the <REG> you pushed to with load-images.sh
imagePullSecrets:
  - name: internal-registry
elasticsearch:
  host: https://elasticsearch.example.com:9200   # from the ELK team
  index: rancher-audit
  auth:
    existingSecret: es-creds                     # kubectl create secret generic es-creds --from-literal=username=... --from-literal=password=...
  tls:
    existingCASecret: es-ca                      # kubectl create secret generic es-ca --from-file=ca.crt=...
EOF

# 7. bundle README
cat > "${STAGE}/README.md" <<EOF
# Rancher Audit Log Operator — air-gap bundle ${VERSION}

Self-contained bundle for an air-gapped Rancher cluster + an externally-managed ELK stack.

Contents:
- \`images/\` — container images saved as tarballs (linux/amd64) + \`images.txt\`
- \`load-images.sh\` — load the images and push them to your internal registry
- \`chart/rancher-audit-log-operator-${VERSION}.tgz\` — the Helm chart
- \`values-example.yaml\` — starting values (registry + ELK endpoint/creds)
- \`elk-integration/\` — hand this to the ELK team (index template, shipper role, dashboard, guide)
- \`docs/\` — air-gap & enable-rancher-audit references

## On the Rancher side (you)
1. Mirror images:        \`./load-images.sh registry.internal/rancher-audit\`
2. Enable Rancher audit logging (one-time): see \`docs/enable-rancher-audit.md\`
3. Install the chart:    edit \`values-example.yaml\`, then
   \`helm install rancher-audit chart/rancher-audit-log-operator-${VERSION}.tgz -n rancher-audit-system --create-namespace -f values-example.yaml\`

## On the ELK side (the other team)
Hand them \`elk-integration/\` — see \`elk-integration/ELK-INTEGRATION.md\` (apply the index
template, create the shipper credential, import the Kibana dashboard).
EOF

# 8. tar it up
mkdir -p "${OUT}"
TARBALL="${OUT}/${NAME}.tar.gz"
tar -C "$(dirname "${STAGE}")" -czf "${TARBALL}" "${NAME}"
rm -rf "$(dirname "${STAGE}")"
echo ">> Wrote ${TARBALL} ($(du -h "${TARBALL}" | awk '{print $1}'))"
