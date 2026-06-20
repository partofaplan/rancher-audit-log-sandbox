#!/usr/bin/env bash
# Cross-compile the operator and build a container image. The binary is cross-compiled on the
# host (CGO disabled) and copied into a scratch image (see Dockerfile) — no in-container build,
# no qemu.
#
#   ./build-image.sh                                   # amd64, tag rancher-audit-log-operator:0.1.0
#   IMG=myreg/op:1.0 PLATFORM=linux/arm64 ./build-image.sh
#   CONTAINER_TOOL=podman ./build-image.sh             # use podman instead of docker
#   DOCKER_CONTEXT=rancher-desktop ./build-image.sh    # build into a specific docker engine
#
# Multi-arch manifest push (docker/buildx only):
#   IMG=myreg/op:1.0 PLATFORM=linux/amd64,linux/arm64 PUSH=1 ./build-image.sh
#
# Offline / air-gapped: run `go mod vendor` (committed here as vendor/) and the build uses
# `-mod=vendor` with GOPROXY=off automatically — no module cache or network needed.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${HERE}"

IMG="${IMG:-rancher-audit-log-operator:0.1.0}"
PLATFORM="${PLATFORM:-linux/amd64}"

# Container tool: explicit CONTAINER_TOOL wins, else prefer docker, else podman.
if [ -z "${CONTAINER_TOOL:-}" ]; then
  if command -v docker >/dev/null 2>&1; then CONTAINER_TOOL=docker; else CONTAINER_TOOL=podman; fi
fi

# Go build mode: prefer the committed vendor/ tree (fully offline); otherwise fall back to the
# local module cache as a file proxy. A caller can override GOFLAGS/GOPROXY/GOSUMDB.
if [ -d vendor ]; then
  export GOFLAGS="${GOFLAGS:--mod=vendor}"
  export GOPROXY="${GOPROXY:-off}"
else
  export GOFLAGS="${GOFLAGS:--mod=mod}"
  export GOPROXY="${GOPROXY:-file://$(go env GOMODCACHE)/cache/download}"
fi
export GOSUMDB="${GOSUMDB:-off}"

mkdir -p bin
for plat in ${PLATFORM//,/ }; do
  arch="${plat#linux/}"
  echo ">> cross-compiling manager for ${plat} (GOFLAGS=${GOFLAGS})"
  CGO_ENABLED=0 GOOS=linux GOARCH="${arch}" \
    go build -trimpath -ldflags="-s -w" -o "bin/manager-linux-${arch}" cmd/main.go
done

if [ "${CONTAINER_TOOL}" = "docker" ]; then
  DC=(); [ -n "${DOCKER_CONTEXT:-}" ] && DC=(--context "${DOCKER_CONTEXT}")
  if [[ "${PLATFORM}" == *,* || "${PUSH:-}" == "1" ]]; then
    echo ">> docker buildx build ${PLATFORM} -t ${IMG} (push=${PUSH:-0})"
    docker ${DC[@]+"${DC[@]}"} buildx build --platform "${PLATFORM}" -t "${IMG}" \
      --provenance=false --sbom=false \
      $([ "${PUSH:-}" == "1" ] && echo --push || echo --load) .
  else
    echo ">> docker build ${PLATFORM} -t ${IMG}"
    docker ${DC[@]+"${DC[@]}"} build --platform "${PLATFORM}" --provenance=false --sbom=false -t "${IMG}" .
  fi
else
  # podman: single-platform build (pass TARGETARCH explicitly), optional push.
  if [[ "${PLATFORM}" == *,* ]]; then
    echo "ERROR: multi-arch (${PLATFORM}) needs docker/buildx; build one arch at a time with podman." >&2
    exit 1
  fi
  arch="${PLATFORM#linux/}"
  echo ">> ${CONTAINER_TOOL} build ${PLATFORM} -t ${IMG}"
  "${CONTAINER_TOOL}" build --platform "${PLATFORM}" --build-arg "TARGETARCH=${arch}" -t "${IMG}" .
  [ "${PUSH:-}" == "1" ] && { echo ">> ${CONTAINER_TOOL} push ${IMG}"; "${CONTAINER_TOOL}" push "${IMG}"; }
fi

echo ">> Built ${IMG} (${PLATFORM}) with ${CONTAINER_TOOL}"
