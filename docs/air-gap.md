# Air-gapped install: mirroring images & deploying the chart

The operator runs in an air-gapped Rancher cluster that pulls only from an **internal
registry**. Two images must be mirrored there, then the Helm chart is pointed at that registry
with a single `image.registry` prefix.

## Easiest path: download the prebuilt bundle from a GitHub Release

The [`air-gap-bundle` workflow](../.github/workflows/airgap-bundle.yml) builds a single
self-contained tarball and attaches it to the GitHub Release for each `v*` tag (you can also
run it manually from the Actions tab and download the artifact). Grab
`rancher-audit-log-airgap-<version>.tar.gz` and carry it into the air gap; it contains:

```
images/            operator + Filebeat images (linux/amd64 tarballs) + images.txt
load-images.sh     load + retag + push both images to your registry
chart/             the Helm chart (.tgz)
values-example.yaml
elk-integration/   the ELK-team handoff package
docs/              this file + enable-rancher-audit.md
README.md
```

Inside the air gap:

```bash
tar xzf rancher-audit-log-airgap-<version>.tar.gz && cd rancher-audit-log-airgap-<version>
./load-images.sh registry.internal/rancher-audit        # load + push both images
# edit values-example.yaml (registry + the ELK team's endpoint/creds), then:
helm install rancher-audit chart/rancher-audit-log-operator-<version>.tgz \
  -n rancher-audit-system --create-namespace -f values-example.yaml
```

To build the bundle yourself on a connected machine:
`./scripts/build-airgap-bundle.sh` (→ `dist/`). Prefer an **amd64** host or have `skopeo`
installed — saving the amd64 Filebeat image via `docker save` on an arm64 Docker Desktop fails
(use `skopeo`, the GitHub workflow, or `INCLUDE_FILEBEAT=0` to mirror Filebeat separately).

## Optional: build the operator image inside the air gap (Go + Podman/Docker)

You normally don't need to build in the gap — the bundle ships a prebuilt operator image. But
if you prefer to build from source on an air-gapped node, it works **offline** because the Go
dependencies are vendored into `operator/vendor/` (committed). `build-image.sh`:

- uses `-mod=vendor` + `GOPROXY=off` automatically when `vendor/` is present (no module cache
  or network needed), and
- auto-detects **Podman** when `docker` isn't on the node (or set `CONTAINER_TOOL=podman`).

```bash
cd operator
IMG=rancher-audit-log-operator:0.1.1 PLATFORM=linux/amd64 ./build-image.sh
podman tag rancher-audit-log-operator:0.1.1 registry.internal/rancher-audit/rancher-audit-log-operator:0.1.1
podman push registry.internal/rancher-audit/rancher-audit-log-operator:0.1.1
```

> Make sure the node has the repo **including `operator/vendor/`** (re-pull/re-copy if you
> uploaded before it was added). If you change `operator/go.mod`, re-run `go mod vendor` on a
> connected machine and commit the result.

## Manual path: mirror images & install the chart yourself

## Images to mirror

| Image | Source | Notes |
|-------|--------|-------|
| Operator | built from this repo → `operator/dist/rancher-audit-log-operator-0.1.0-amd64.tar` | `scratch`-based, self-contained (~17 MB) |
| Filebeat | `docker.elastic.co/beats/filebeat:8.17.3` | the shipper the operator deploys; match your ELK's major version |

Target paths under your registry prefix `<REG>` (e.g. `registry.internal/rancher-audit`):

```
<REG>/rancher-audit-log-operator:0.1.0
<REG>/beats/filebeat:8.17.3
```

## Build the operator image (on a connected machine)

```bash
cd operator
IMG=rancher-audit-log-operator:0.1.0 PLATFORM=linux/amd64 ./build-image.sh
# -> also written to operator/dist/rancher-audit-log-operator-0.1.0-amd64.tar
```

## Move both images into the air gap

**Option A — `skopeo` (no Docker on the bastion; preserves the amd64 manifest):**

```bash
REG=registry.internal/rancher-audit
# operator (from the tarball produced above)
skopeo copy --all docker-archive:operator/dist/rancher-audit-log-operator-0.1.0-amd64.tar \
  docker://$REG/rancher-audit-log-operator:0.1.0
# filebeat (pull on a connected host, then copy into the gap per your transfer process)
skopeo copy --all docker://docker.elastic.co/beats/filebeat:8.17.3 \
  docker://$REG/beats/filebeat:8.17.3
```

**Option B — Docker save/load + push:**

```bash
REG=registry.internal/rancher-audit
# operator
docker load -i operator/dist/rancher-audit-log-operator-0.1.0-amd64.tar
docker tag rancher-audit-log-operator:0.1.0 $REG/rancher-audit-log-operator:0.1.0
docker push $REG/rancher-audit-log-operator:0.1.0
# filebeat
docker pull --platform linux/amd64 docker.elastic.co/beats/filebeat:8.17.3
docker tag docker.elastic.co/beats/filebeat:8.17.3 $REG/beats/filebeat:8.17.3
docker push $REG/beats/filebeat:8.17.3
```

(Rancher's own air-gap tooling — `rancher-save`/`rancher-load`, or Hauler — works too; the two
references above are all this operator adds.)

## Install the chart

Load `charts/rancher-audit-log-operator` into your internal Helm/chart repo (or install the
directory directly), then:

```bash
helm install rancher-audit charts/rancher-audit-log-operator \
  -n rancher-audit-system --create-namespace \
  --set image.registry=registry.internal/rancher-audit \
  --set imagePullSecrets[0].name=internal-registry \
  --set elasticsearch.host=https://es.corp.example:9200 \
  --set elasticsearch.auth.existingSecret=es-creds \
  --set elasticsearch.tls.existingCASecret=es-ca
```

`image.registry` prefixes **both** images. Set `imagePullSecrets` if the registry requires
auth. The ES endpoint/credentials/CA come from the ELK team (see
[../elk-integration/ELK-INTEGRATION.md](../elk-integration/ELK-INTEGRATION.md)). Audit logging
must be enabled on this cluster's Rancher first ([enable-rancher-audit.md](enable-rancher-audit.md)).
