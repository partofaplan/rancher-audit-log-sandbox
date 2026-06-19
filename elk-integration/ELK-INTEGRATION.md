# Onboarding Rancher audit logs into Elasticsearch / Kibana

This folder is a **self-contained handoff** for the team that runs Elasticsearch & Kibana.
A separate team runs a **Filebeat shipper** in their Rancher cluster (deployed by the
`rancher-audit-log-operator`). It tails Rancher's UI/API audit log and ships enriched events
to **your** Elasticsearch. You don't run the shipper — you provision the index, a credential,
and the dashboard, and allow the network path.

Files here:

| File | Purpose |
|------|---------|
| `index-template.json` | Elasticsearch index template (field mappings) for `rancher-audit*` |
| `shipper-role.json` | least-privilege role descriptor for the shipper's credential |
| `rancher-audit-dashboard.ndjson` | Kibana saved objects: data view, searches, visualizations, dashboard |
| `ELK-INTEGRATION.md` | this document |

## The data contract (interface between the two teams)

| | |
|---|---|
| **Transport** | Filebeat → Elasticsearch HTTP(S) `_bulk` API |
| **Endpoint** | you provide: `https://<your-es-host>:<port>` (+ base path if behind a proxy) |
| **Index** | `rancher-audit` (default; configurable on the shipper side) |
| **Auth** | you provide a credential: an **API key** (preferred) or a basic-auth **user/password** |
| **TLS** | HTTPS; the shipper trusts a public CA automatically, or you provide your CA cert |
| **Version** | the shipper uses a Filebeat image matching your ES **major** version (default `8.17.x` for ES 8.x — tell the Rancher team if you're on 7.x/9.x) |
| **Network** | the **Rancher cluster** needs egress to your ES endpoint; no inbound to Rancher is required |

What you hand back to the Rancher/operator team: the **ES endpoint URL**, the **index name**
(if not the default), the **credential**, and (if not a public CA) the **CA certificate**.

## Steps

Examples below use `curl` against ES at `$ES` (e.g. `https://es.corp.example:9200`) and Kibana
at `$KB`, as a user allowed to manage templates/roles/api-keys. Adapt to your tooling.

### 1. Allow the network path

Permit egress from the Rancher cluster's nodes/pods to your ES endpoint (host + port, TLS).
Nothing needs to reach *into* the Rancher cluster.

### 2. Apply the index template

This maps `audit.*` as `keyword`/`date` so aggregations and the dashboard work (and prevents
mapping drift). Apply it **before** data first flows; if the index already exists with dynamic
mappings, delete/reindex it after applying.

```bash
curl -u "$USER:$PASS" -X PUT "$ES/_index_template/rancher-audit" \
  -H 'Content-Type: application/json' --data-binary @index-template.json
```

### 3. Create the shipper credential (least privilege)

The shipper only needs to write to `rancher-audit*`. Create an **API key** scoped by
`shipper-role.json`:

```bash
curl -u "$USER:$PASS" -X POST "$ES/_security/api_key" -H 'Content-Type: application/json' -d "{
  \"name\": \"rancher-audit-shipper\",
  \"role_descriptors\": { \"rancher_audit_writer\": $(cat shipper-role.json) }
}"
```

The response's `encoded` value is the base64 `id:api_key` — hand that to the Rancher team
(the operator stores it for Filebeat). **Or** create a role + user instead:

```bash
curl -u "$USER:$PASS" -X PUT "$ES/_security/role/rancher_audit_writer" \
  -H 'Content-Type: application/json' --data-binary @shipper-role.json
curl -u "$USER:$PASS" -X POST "$ES/_security/user/rancher-audit-shipper" \
  -H 'Content-Type: application/json' -d '{"password":"<generated>","roles":["rancher_audit_writer"]}'
```

> Filebeat's Elasticsearch output today authenticates with **basic auth** (username/password).
> If you issue an API key, give the Rancher team a user/password, or confirm their shipper
> version supports `api_key` output auth.

### 4. Import the Kibana dashboard

UI: **Stack Management → Saved Objects → Import** → `rancher-audit-dashboard.ndjson` →
enable "overwrite". Or via API:

```bash
curl -u "$USER:$PASS" -X POST "$KB/api/saved_objects/_import?overwrite=true" \
  -H 'kbn-xsrf: true' --form file=@rancher-audit-dashboard.ndjson
```

This creates a **data view titled `rancher-audit`** (must match the index name — if you use a
different index, rename the data view after import; the dashboard follows it), plus the
**"Rancher Audit Overview"** dashboard and two saved searches.

## What the data looks like

One document per audited Rancher API request. The shipper decodes the raw audit JSON into
`rancher.*` and adds a derived `audit.*` summary:

```json
{
  "@timestamp": "2026-06-19T18:35:02Z",
  "audit": {
    "actor": "zperkins",          "verb": "delete",
    "resource": "pods",           "target": "default/web-77f9",
    "category": "workload",       "event": "delete pods",
    "responseCode": 200,
    "summary": "zperkins delete pods default/web-77f9"
  },
  "rancher": {
    "auditID": "cd23…", "method": "DELETE",
    "requestURI": "/k8s/clusters/c-abc/v1/pods/default/web-77f9",
    "responseCode": 200, "user": { "name": "user-xdzqk" }
  },
  "message": "{…the raw audit JSON line…}"
}
```

Field reference (the ones the dashboard uses):

| Field | Type | Meaning |
|-------|------|---------|
| `audit.actor` | keyword | Rancher local/login username (falls back to principal, then `unknown`) |
| `audit.verb` | keyword | create / update / delete / get / invoke (from the HTTP method) |
| `audit.resource` | keyword | resource kind (pods, deployments, ingresses, …) |
| `audit.target` | keyword | namespace/name (or name) of the object |
| `audit.category` | keyword | workload / rbac / networking / storage / config / auth / cluster / catalog / system |
| `audit.event` | keyword | `<verb> <resource>` (for top-events breakdown) |
| `audit.summary` | text+keyword | the readable sentence |
| `rancher.*` | — | the decoded raw audit fields (method, requestURI, user.name, responseCode, …) |

## Verify

```bash
curl -u "$USER:$PASS" "$ES/rancher-audit/_count"
curl -u "$USER:$PASS" "$ES/rancher-audit/_search?size=3" -H 'Content-Type: application/json' \
  -d '{"_source":["audit.summary"],"sort":[{"@timestamp":"desc"}]}'
```

Then open Kibana → **Dashboards → Rancher Audit Overview**.
