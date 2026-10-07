# GitOps Addon in a Disconnected Environment: Image Mirroring via `ManagedClusterImageRegistry`

## 1. Feature Overview

### The problem

The GitOps addon (`gitopsaddon`) installs several component images on every managed cluster it
targets: the OpenShift GitOps operator, the ArgoCD instance it manages (application-controller,
repo-server, redis, dex), and — when agent mode is enabled — the ArgoCD agent process that
connects back to the hub. By default, every one of those images is pulled from a public source
registry (typically `registry.redhat.io`).

In a **disconnected environment**, a managed cluster cannot reach that public source registry at
all — it can only reach a private mirror registry that the administrator has already set up.
Previously, there was no supported way to redirect the GitOps addon to pull its images from a
mirror registry on a per-cluster basis. Administrators had to configure each managed cluster
individually and manually keep those configurations in sync whenever anything changed.

### What this feature adds

This feature adds a new, **opt-in** way to redirect the GitOps addon's component images to a
private mirror registry, on a per-cluster basis, using the existing
`ManagedClusterImageRegistry` API that ACM administrators are already familiar with.

Highlights:

- **Opt-in and non-breaking** — mirroring only applies to a `ManagedClusterImageRegistry` that is
  explicitly enabled for this purpose. Nothing changes for any other cluster or registry.
- **Reversible** — turning mirroring off (or removing the configuration) automatically restores
  the original image values.
- **Self-healing** — mirrored values are kept in sync automatically, so administrators don't need
  to manually re-apply mirroring after a hub upgrade or other change.
- **Works alongside existing hub automation** — this feature cooperates with the GitOps addon's
  own defaults instead of overriding or conflicting with them.
- **Consistent handling of the ArgoCD agent image** — the ArgoCD agent image on each managed
  cluster is automatically kept in sync with the hub, and is mirrored the same way as every other
  component image.

### What this feature does *not* do

- It does not apply to the GitOps operator or ArgoCD images on **OpenShift (OCP)** managed
  clusters. OCP clusters install the operator through OLM, which resolves images from its own
  catalog, independent of this feature. (The ArgoCD agent image is the one exception — see
  [Use case 3](#use-case-3-control-the-argocd-agents-image).)
- It does not provide or manage registry **credentials** — and it doesn't need to. The GitOps
  addon already has its own built-in mechanism for authenticating to whatever registry it pulls
  from: it propagates the registry pull secret to every managed cluster, attaches it to every
  ArgoCD component's ServiceAccount, and automatically recreates any Pod that's stuck unable to
  pull. That mechanism works the same way regardless of which registry is involved, so once your
  mirror registry's credentials are included in your organization's standard pull-secret chain
  (the same one the addon already relies on), authentication to the mirror just works — this
  feature only changes *which* image location is requested, not how access to it is granted.

---

## 2. Architecture & Workflow

The full diagram is at [`Architecture Workflow`](./gitops-addon-disconnected-image-mirroring-architecture.png). 

**Reconcile-time flow (hub side):**

1. The hub controller keeps each managed cluster's GitOps addon configuration current — writing
   the default image values and, when the ArgoCD agent is enabled, whatever image the hub's own
   ArgoCD agent principal is currently running.
2. If an administrator has opted a `ManagedClusterImageRegistry` into mirroring, a separate
   controller rewrites the matching image values in that same configuration to point at the
   mirror registry instead.
3. These two controllers cooperate rather than compete: once a value is mirrored, it stays
   mirrored unless the underlying default value genuinely changes.
4. The OCM addon framework then delivers the resulting configuration to the managed cluster.

**On the managed cluster:** the GitOps addon agent installs the GitOps operator using the image
values it received (mirrored or not). The operator then creates the ArgoCD instance and, when
agent mode is enabled, the ArgoCD agent, which connects back to the hub's ArgoCD agent principal.
See [Troubleshooting](#5-troubleshooting) for common connectivity and certificate issues with that
connection.

---

## 3. Prerequisites

- A working `GitOpsCluster` that has already deployed the GitOps addon to the managed cluster(s)
  you want to mirror. Image mirroring extends an existing addon deployment — it does not create
  one on its own.
- A `Placement` that selects the managed cluster(s) to mirror, created in the same namespace where
  you will create the `ManagedClusterImageRegistry`.
- A pull secret for the mirror registry, also created in that same namespace.
- Confirm the mirror registry's credentials are included in your organization's standard
  pull-secret chain — the same one the GitOps addon already uses to authenticate to any registry
  today. No extra setup is needed beyond that: the addon automatically propagates those
  credentials to every managed cluster and wires them onto its own components. Mirroring only
  changes *where* images are requested from — it does not grant access to pull them.

---

## 4. Major Use Cases

### Use case 1: Enable image mirroring for the GitOps addon

Use this when managed clusters can't reach `registry.redhat.io` (or wherever the addon's default
images come from) directly, and must pull through a mirror instead.

**Step 1 — Create the pull secret for the mirror registry**

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: mirror-registry-pull-secret
  namespace: openshift-gitops
type: kubernetes.io/dockerconfigjson
data:
  .dockerconfigjson: <base64-encoded-docker-config>
```

**Step 2 — Create (or reuse) a `Placement` selecting the target clusters**

```yaml
apiVersion: cluster.open-cluster-management.io/v1beta1
kind: Placement
metadata:
  name: disconnected-clusters-placement
  namespace: openshift-gitops
spec:
  clusterSets: [global]
  predicates:
    - requiredClusterSelector:
        labelSelector:
          matchLabels:
            environment: disconnected
```

**Step 3 — Create the `ManagedClusterImageRegistry` with the opt-in annotation**

The `apps.open-cluster-management.io/gitops-addon-image-mirroring: "true"` annotation is
**required**. Without it, this CR is invisible to the gitops-addon image mirroring feature.

```yaml
apiVersion: imageregistry.open-cluster-management.io/v1alpha1
kind: ManagedClusterImageRegistry
metadata:
  name: gitops-addon-mirror
  namespace: openshift-gitops
  annotations:
    apps.open-cluster-management.io/gitops-addon-image-mirroring: "true"
spec:
  pullSecret:
    name: mirror-registry-pull-secret
  placementRef:
    group: cluster.open-cluster-management.io
    resource: placements
    name: disconnected-clusters-placement
  registries:
    - source: registry.redhat.io
      mirror: my-mirror.example.com:5000/redhat
```

If you mirror everything to one registry regardless of source, use the simpler catch-all
`spec.registry` field instead (ignored whenever `spec.registries` is non-empty):

```yaml
spec:
  pullSecret:
    name: mirror-registry-pull-secret
  placementRef:
    group: cluster.open-cluster-management.io
    resource: placements
    name: disconnected-clusters-placement
  registry: my-mirror.example.com:5000
```

**Step 4 — Verify on the hub**

```bash
# A finalizer appears once the controller starts managing the CR
kubectl get managedclusterimageregistry gitops-addon-mirror -n openshift-gitops \
  -o jsonpath='{.metadata.finalizers}'

# Image values in the target cluster's addon configuration should now point at the mirror
kubectl get addondeploymentconfig gitops-addon-config -n <managed-cluster-name> -o yaml
```

You should see the relevant image values (e.g. the GitOps operator and ArgoCD images) rewritten to
`my-mirror.example.com:5000/...`, along with a few bookkeeping annotations that record the
original (pre-mirror) values so they can be restored later.

**Step 5 — Verify on the managed cluster (non-OCP)**

The configuration being rewritten only proves the hub side did its job — see
[Use case 5](#use-case-5-know-when-mirroring-does-and-does-not-apply-ocp-vs-non-ocp) for why this
step only applies to non-OCP spokes. Switch to the managed cluster's kubeconfig and check:

```bash
# Operator image
kubectl get deployment openshift-gitops-operator-controller-manager \
  -n openshift-gitops-operator \
  -o jsonpath='{.spec.template.spec.containers[?(@.name=="manager")].image}{"\n"}'
# expect: my-mirror.example.com:5000/redhat/...

# Live ArgoCD component pods should be pulling from the mirror
kubectl get pods -n openshift-gitops \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{range .spec.containers[*]}{.image}{" "}{end}{"\n"}{end}'
```

A pod that predates the mirroring change may still show the old image until it's next recreated —
delete it to force a refresh, or wait for a rollout that touches it.

---

### Use case 2: Disable image mirroring (revert to source images)

There are two ways to stop mirroring, depending on whether you want to keep the CR around.

**Option A — Remove the opt-in annotation, keep the CR**

Useful if the CR is also used for something else (e.g. you plan to hand it back to ACM's own
klusterlet image mirroring) and you just want the gitops-addon mirroring behavior turned off.

```bash
kubectl annotate managedclusterimageregistry gitops-addon-mirror -n openshift-gitops \
  apps.open-cluster-management.io/gitops-addon-image-mirroring-
```

**Option B — Delete the `ManagedClusterImageRegistry`**

```bash
kubectl delete managedclusterimageregistry gitops-addon-mirror -n openshift-gitops
```

Either way, the controller reverts every value it mirrored back to the recorded original values
before the configuration is fully cleaned up.

**Verify the revert**

```bash
kubectl get addondeploymentconfig gitops-addon-config -n <managed-cluster-name> -o yaml
```

The image values should be back to their source-registry form, and the mirroring bookkeeping
annotations should be gone.

As in Use case 1, re-check the spoke itself (same commands as Use case 1, Step 5) to confirm the
GitOps operator and its ArgoCD instance actually went back to pulling from the source registry —
an already-running pod isn't restarted just because the default changed.

---

### Use case 3: Control the ArgoCD agent's image

**Default: automatic drift heal (recommended for most users)**

No extra configuration is needed. The hub automatically keeps the ArgoCD agent image on every
agent-enabled managed cluster in sync with the image the hub's own ArgoCD agent is currently
running. Whenever that image changes (for example, after an operator upgrade), the hub updates
every managed cluster accordingly. If a cluster has been opted into image mirroring (Use case 1),
the agent image is mirrored the same way as every other component image — no extra configuration
needed.

To check the current value:

```bash
kubectl get addondeploymentconfig gitops-addon-config -n <managed-cluster-name> \
  -o jsonpath='{.spec.customizedVariables[?(@.name=="ARGOCD_AGENT_IMAGE")].value}'
```

---

### Use case 4: Mirror different source registries to different destinations

Use this when your addon images come from more than one upstream registry (e.g. Red Hat images
from `registry.redhat.io` and a community image from `quay.io`) and your disconnected environment
mirrors them to different local repositories.

**Step 1 — List each source → mirror pair under `spec.registries`**

```yaml
apiVersion: imageregistry.open-cluster-management.io/v1alpha1
kind: ManagedClusterImageRegistry
metadata:
  name: gitops-addon-mirror
  namespace: openshift-gitops
  annotations:
    apps.open-cluster-management.io/gitops-addon-image-mirroring: "true"
spec:
  pullSecret:
    name: mirror-registry-pull-secret
  placementRef:
    group: cluster.open-cluster-management.io
    resource: placements
    name: disconnected-clusters-placement
  registries:
    - source: registry.redhat.io
      mirror: my-mirror.example.com:5000/redhat
    - source: quay.io
      mirror: my-mirror.example.com:5000/quay
```

**Step 2 — Know the matching rule: last match wins**

Entries are evaluated **in order**; every entry whose `source` is empty *or* matches the image's
registry host is a candidate, and the **last** matching entry in the list is the one applied. This
lets a later, more specific entry override an earlier catch-all:

```yaml
  registries:
    - source: ""                       # catch-all: mirrors every host not matched below
      mirror: my-mirror.example.com:5000/default
    - source: registry.redhat.io       # overrides the catch-all specifically for this host
      mirror: my-mirror.example.com:5000/redhat
```

Put your most specific entries **last** if you also need a catch-all.

**Step 3 — Verify each image landed under the right prefix**

```bash
kubectl get addondeploymentconfig gitops-addon-config -n <managed-cluster-name> \
  -o jsonpath='{range .spec.customizedVariables[*]}{.name}={.value}{"\n"}{end}'
```

---

### Use case 5: Know when mirroring does and does not apply (OCP vs non-OCP)

This is the single most common point of confusion when setting this up in a disconnected
**OpenShift** fleet — read this before assuming mirroring "isn't working."

**Step 1 — Identify the install path for the target managed cluster**

- **Non-OCP (Kind, EKS, etc.):** the GitOps addon installs the operator from a bundled chart.
  Every image value comes directly from the addon configuration. Mirroring from
  [Use case 1](#use-case-1-enable-image-mirroring-for-the-gitops-addon) fully applies to the
  operator, ArgoCD, redis, dex, and the agent image.
- **OCP:** the GitOps addon instead installs the operator through OLM, which resolves every
  component image from its own catalog — the hub has **zero** control over the
  operator/ArgoCD/redis/dex images through this feature on OCP. **The ArgoCD agent image is the
  one exception**: it is always controlled the same way on OCP as on non-OCP, because the agent is
  deployed directly by the addon rather than bundled into the OLM-resolved operator.

**Step 2 — For OCP spokes, mirror the operator/ArgoCD images at the cluster level instead**

Set up the standard OCP disconnected-registry mechanisms independently of this feature:

- An `ImageContentSourcePolicy` (or `ImageDigestMirrorSet`) on the managed cluster pointing
  `registry.redhat.io` (and any other relevant source) at your mirror.
- A `CatalogSource` serving the `redhat-operators` (or your custom) catalog from the mirror.

Mirroring the non-agent image values on an OCP cluster through this feature is harmless (they're
simply unused by the OLM install path) but has **no effect** — don't spend time debugging "the
operator pod still shows the source image" on OCP; that's expected.

**Step 3 — Confirm which path a given cluster is actually using**

```bash
# Non-OCP: the operator namespace is populated by the addon directly
oc get pods -n openshift-gitops-operator

# OCP: an OLM Subscription/InstallPlan/CSV exists for the operator
oc get subscription,installplan,csv -n openshift-operators | grep -i gitops
```

---

## 5. Troubleshooting

> **The two issues below are the most common ones encountered when running the ArgoCD Agent
> Pull Model on managed clusters after setting up `GitopsAddon` in a disconnected environment.**
> Both come from assumptions in the agent's connectivity and certificate handling that don't hold
> once the managed cluster has no direct route to the hub's public-facing endpoints. If the agent
> on a disconnected managed cluster isn't connecting, start here.

### Common issue 1: ArgoCD agent can't reach the principal because its Route hostname isn't resolvable from the managed cluster

**Symptom** — on the managed cluster:

```text
# oc logs -n openshift-gitops acm-openshift-gitops-agent-agent-958b8cc75-fqcpr
time="2026-10-06T15:21:41Z" level=warning msg="Auth failure: rpc error: code = Unavailable desc = name resolver error: produced zero addresses (retrying in 55.206143874s)"
time="2026-10-06T15:22:37Z" level=warning msg="Could not connect to openshift-gitops-agent-principal-openshift-gitops.apps.mist11-0.qe.red-chesterfield.com:443: rpc error: code = Unavailable desc = name resolver error: produced zero addresses" module=Agent
```

**Root cause**

The hub automatically discovers the address the managed cluster's agent should connect to, in a
fixed order: **Route → LoadBalancer → NodePort**. Route discovery "succeeds" as soon as a matching
`Route` object exists in the ArgoCD namespace — it never checks whether that hostname is actually
resolvable/reachable from the managed cluster's network. In a disconnected environment the hub's
external Route hostname is frequently *not* resolvable from the spoke, so the agent is handed an
address it can never reach, and keeps retrying forever with `name resolver error: produced zero
addresses`.

**Solution**

**Step 1 — Switch the principal's `Service` to `NodePort` via the `ArgoCD` CR** on the hub cluster.

```bash
oc patch argocd openshift-gitops -n openshift-gitops --type=merge -p \
  '{"spec":{"argoCDAgent":{"principal":{"server":{"service":{"type":"NodePort"}}}}}}'
```

Wait a reconcile, then confirm:

```bash
oc get svc openshift-gitops-agent-principal -n openshift-gitops -o yaml
```

```yaml
apiVersion: v1
kind: Service
metadata:
  labels:
    app.kubernetes.io/component: principal
    app.kubernetes.io/managed-by: openshift-gitops
    app.kubernetes.io/name: openshift-gitops-agent-principal
    app.kubernetes.io/part-of: argocd-agent
  name: openshift-gitops-agent-principal
  namespace: openshift-gitops
spec:
  clusterIP: 172.30.205.45
  externalTrafficPolicy: Cluster
  ports:
    - name: https
      nodePort: 31139        # ========> node port
      port: 443
      protocol: TCP
      targetPort: 8443
  selector:
    app.kubernetes.io/name: openshift-gitops-agent-principal
  type: NodePort
```

**Step 2 — Get the `NodePort` and a reachable hub node's internal IP**

```bash
oc get svc openshift-gitops-agent-principal -n openshift-gitops \
  -o jsonpath='{.spec.ports[0].nodePort}{"\n"}'
# 31139

oc get nodes -o wide
# worker-0-0   Ready   worker   21d   v1.35.5   192.168.123.116   ...
```

**Step 3 — Point the `GitOpsCluster` at `<node-IP>:<nodePort>`**

```bash
oc patch gitopscluster mc-gitops-agent -n openshift-gitops --type=merge -p \
  '{"spec":{"gitopsAddon":{"argoCDAgent":{"serverAddress":"<hub-node-internal-ip>","serverPort":"<nodePort>"}}}}'
```

**Step 4 — Verify on the managed cluster**

The gitops addon restarts, and the `ArgoCD` resource on the managed cluster reflects the change:

```yaml
# oc get argocd -n openshift-gitops acm-openshift-gitops -o yaml
spec:
  argoCDAgent:
    agent:
      allowedNamespaces: ['*']
      client:
        mode: managed
        principalServerAddress: 192.168.123.116   # ==> from the hub GitOpsCluster patch
        principalServerPort: "31139"               # ==> from the hub GitOpsCluster patch
      destinationBasedMapping:
        enabled: true
      enabled: true
      tls:
        rootCASecretName: argocd-agent-ca
        secretName: argocd-agent-client-tls
```

The agent pod restarts cleanly with no resolver error:

```text
# oc get pods -n openshift-gitops | grep openshift-gitops-agent
acm-openshift-gitops-agent-agent-54c9b9b4-fd48d   1/1   Running   0   23m

# oc logs -n openshift-gitops acm-openshift-gitops-agent-agent-54c9b9b4-fd48d
# (no "name resolver error: produced zero addresses")
```

> **Why NodePort instead of fixing the Route:** NodePort removes the dependency on any DNS name
> being resolvable from the spoke's network at all — only the hub node's IP (already reachable, by
> definition, since that's how the managed cluster was imported) and the chosen port need to be
> reachable. The principal's TLS certificate is still valid for this path: the hub automatically
> adds its node IPs to the certificate whenever no `LoadBalancer` ingress is present, so switching
> to `NodePort` does not introduce a new certificate-mismatch problem on top of this one. If a
> reachable `LoadBalancer` is available in your environment, that is also a valid fix and skips the
> manual node-IP lookup in Step 2 — NodePort is the fallback for environments where it isn't.

---

### Common issue 2: ArgoCD agent reports the principal's certificate has expired after a renewal

**Symptom** — on the managed cluster:

```text
# oc logs -n openshift-gitops acm-openshift-gitops-agent-agent-958b8cc75-fqcpr
time="2026-08-15T07:28:01Z" level=warning msg="Auth failure: rpc error: code = Unavailable desc = connection error: desc = \"transport: authentication handshake failed: tls: failed to verify certificate: x509: certificate has expired or is not yet valid: current time 2026-08-15T07:28:01Z is after 2026-08-14T23:56:25Z\" (retrying in 7.430083706s)"
```

**Root cause**

The agent is validating the **principal's server-side TLS certificate**(`argocd-agent-principal-tls`).
It can be renewed on the hub (by whatever issuer manages it) without anything telling the **principal pod itself** to reload it: the new certificate is written to the secret, but an already-running principal process keeps serving the old (now-expired) certificate until it is restarted.

**Solution**

**Step 1 — Confirm the secret itself has actually been renewed**

```bash
oc get secret argocd-agent-principal-tls -n openshift-gitops -o jsonpath='{.data.tls\.crt}' \
  | base64 -d | openssl x509 -noout -dates -subject
# notBefore=Aug 15 07:10:04 2026 GMT
# notAfter=Sep 14 07:10:05 2026 GMT
# subject=CN = 10.0.36.55
```

If the secret's `notAfter` is still in the past, the issuer itself hasn't renewed it yet — fix that
first (this is the actual root cause in that case, not a stale listener).

**Step 2 — If the secret is current but the error persists, restart the principal to reload it**

```bash
oc get pods -n openshift-gitops
# openshift-gitops-agent-principal-894c89c76-l6xtj   1/1   Running   6 (30d ago)   30d

oc rollout restart deployment -n openshift-gitops -l app.kubernetes.io/component=principal
oc rollout status deployment -n openshift-gitops -l app.kubernetes.io/component=principal
```

The agent's next reconnect attempt succeeds once the new principal pod is serving the renewed
certificate.

---

### Other known issues (quick reference)

- **Mirroring doesn't apply to a cluster** — confirm the cluster already has the GitOps addon
  installed (i.e. its `GitOpsCluster` has already reconciled it) before expecting mirroring to
  take effect; there's nothing to mirror until the addon configuration exists.
- **Removed the opt-in annotation but the configuration still shows mirrored values** — give it a
  reconcile pass; the revert isn't instant. If it's still not reverted after a few minutes, check
  that the `Placement` referenced by the `ManagedClusterImageRegistry` can still be resolved.
