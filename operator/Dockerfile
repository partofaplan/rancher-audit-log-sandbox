# Minimal runtime image for the operator.
#
# The manager binary is cross-compiled on the host (CGO disabled, pure Go) and copied
# in here — no in-container `go build`. This keeps the build hermetic/offline and avoids
# qemu emulation when building a linux/amd64 image from an arm64 host. Build via
# ./build-image.sh, which produces bin/manager-linux-<arch> for the requested platform(s).
#
# For a conventional from-source multi-stage build (needs network for `go mod download`),
# see git history or use `go mod vendor` + a builder stage.
#
# Base is `scratch`: the static, CGO-free binary needs nothing else, and the operator only
# talks to the in-cluster Kubernetes API (which provides its own CA), so no system certs are
# required. Runs as a non-root numeric UID to satisfy runAsNonRoot.
FROM scratch
ARG TARGETARCH
COPY bin/manager-linux-${TARGETARCH} /manager
USER 65532:65532
ENTRYPOINT ["/manager"]
