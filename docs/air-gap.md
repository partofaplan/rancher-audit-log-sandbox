# Air-gapped install: mirroring images & deploying the chart

The operator runs in an air-gapped Rancher cluster that pulls only from an **internal
registry**. Two images must be mirrored there, then the Helm chart is pointed at that registry
with a single `image.registry` prefix.

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
