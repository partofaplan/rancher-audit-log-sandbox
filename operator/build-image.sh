#!/usr/bin/env bash
# Cross-compile the operator (offline, from the Go module cache) and build a container
# image. The host here is arm64 but the default target is linux/amd64 — the binary is
# cross-compiled (no qemu) and copied into a distroless image (see Dockerfile).
#
#   ./build-image.sh                                   # amd64, tag rancher-audit-log-operator:0.1.0
#   IMG=myreg/op:1.0 PLATFORM=linux/arm64 ./build-image.sh
#   DOCKER_CONTEXT=rancher-desktop ./build-image.sh    # build into a specific docker engine
#
# Pushing a multi-arch manifest (amd64+arm64) to a registry:
#   IMG=myreg/op:1.0 PLATFORM=linux/amd64,linux/arm64 PUSH=1 ./build-image.sh
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${HERE}"

IMG="${IMG:-rancher-audit-log-operator:0.1.0}"
PLATFORM="${PLATFORM:-linux/amd64}"
DC=(); [ -n "${DOCKER_CONTEXT:-}" ] && DC=(--context "${DOCKER_CONTEXT}")

# Default to the local module cache as a file proxy so the build works offline; a caller
# (e.g. CI with network) can override GOPROXY/GOSUMDB/GOFLAGS via the environment.
export GOPROXY="${GOPROXY:-file://$(go env GOMODCACHE)/cache/download}"
export GOSUMDB="${GOSUMDB:-off}"
export GOFLAGS="${GOFLAGS:--mod=mod}"

mkdir -p bin
for plat in ${PLATFORM//,/ }; do
  arch="${plat#linux/}"
  echo ">> cross-compiling manager for ${plat}"
  CGO_ENABLED=0 GOOS=linux GOARCH="${arch}" \
    go build -trimpath -ldflags="-s -w" -o "bin/manager-linux-${arch}" cmd/main.go
done

if [[ "${PLATFORM}" == *,* || "${PUSH:-}" == "1" ]]; then
  # multi-platform and/or push -> buildx
  echo ">> buildx build ${PLATFORM} -t ${IMG} (push=${PUSH:-0})"
  docker ${DC[@]+"${DC[@]}"} buildx build --platform "${PLATFORM}" -t "${IMG}" \
    --provenance=false --sbom=false \
    $([ "${PUSH:-}" == "1" ] && echo --push || echo --load) .
else
  echo ">> docker build ${PLATFORM} -t ${IMG}"
  docker ${DC[@]+"${DC[@]}"} build --platform "${PLATFORM}" --provenance=false --sbom=false -t "${IMG}" .
fi

echo ">> Built ${IMG} (${PLATFORM})"
