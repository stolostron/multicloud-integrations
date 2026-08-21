# GitOps Addon in a Disconnected Environment: Image Mirroring with `ManagedClusterImageRegistry`

This guide shows how to set up the GitOps addon (`gitopsaddon`) on managed clusters that can only
reach a disconnected/mirror registry, by opting a `ManagedClusterImageRegistry` (MCIR) into
mirroring the addon's component images. It also covers the two related controls that affect the
ArgoCD agent's image specifically: the Policy `spec.argoCDAgent.agent.image` field, and the
`skip-agent-version-heal` annotation.

For architectural background, see `CLAUDE.md` → "Disconnected Image Mirroring
(ManagedClusterImageRegistry)", "Agent Version Drift Heal", and "Key Annotations on ManagedClusterImageRegistry".

## How it fits together

```
GitOpsCluster controller                 ManagedClusterImageRegistry controller
  (pkg/controller/gitopscluster)           (imageregistry_controller.go)
        |                                            |
        | writes source-registry image values        | rewrites matching image values
        | into gitops-addon-config ADC               | to the mirror registry, IF the
        v                                            | MCIR opted in via annotation
  AddOnDeploymentConfig "gitops-addon-config" (per managed cluster namespace)
        |
        v
  gitopsaddon agent on the spoke installs the GitOps operator / ArgoCD using
  whatever image values are in the ADC (mirrored or not)
```

- **The `ManagedClusterImageRegistry` controller only touches a CR that opts in.** Without the
  opt-in annotation, a `ManagedClusterImageRegistry` is left entirely to ACM's own
  `cluster-image-registry-controller` (klusterlet image mirroring) — nothing about the GitOps
  addon's images changes. This means existing MCIR objects in your fleet are unaffected unless you
  explicitly annotate them.
- **The two controllers do not fight over the ADC.** The GitOpsCluster controller preserves
  already-mirrored values as long as the underlying source-registry value hasn't changed; it only
  writes through a fresh source value (which the image-registry controller then re-mirrors) when
  the true source actually changes (hub image upgrade, spec override, or agent-version drift
  heal).
- **The ArgoCD agent image is the one exception worth understanding up front.** It normally comes
  from the hub controller's drift-heal mechanism (mirrors the running principal's image) rather
  than a static default, and it is written to the ADC's `ARGOCD_AGENT_IMAGE` variable — never to
  the shared ArgoCD Policy. See [Use case 3](#use-case-3-control-the-argocd-agents-image) below.

## Prerequisites

- A `GitOpsCluster` with `spec.gitopsAddon.enabled: true` already reconciling the target managed
  cluster(s) — this is what creates the `gitops-addon-config` `AddOnDeploymentConfig` and the
  `gitops-addon` `ManagedClusterAddOn` that the image-registry controller looks for. If this
  doesn't exist yet, image mirroring has nothing to rewrite.
- A `Placement` selecting the managed cluster(s) you want mirrored, **in the same namespace** as
  the `ManagedClusterImageRegistry` you will create (the controller resolves `placementRef`
  against a `Placement` in the MCIR's own namespace). This can be the same `Placement` the
  `GitOpsCluster` uses, or a different one — mirroring only takes effect on clusters that already
  have the `gitops-addon` addon installed, regardless of which `Placement` the MCIR itself
  references.
- A pull secret for the mirror registry, in the same namespace as the `ManagedClusterImageRegistry`
  (referenced by `spec.pullSecret`).

---

## Use case 1: Enable image mirroring for the GitOps addon

Use this when managed clusters can't reach `registry.redhat.io` (or wherever the addon's default
images come from) directly, and must pull through a mirror instead.

### Step 1 — Create the pull secret for the mirror registry

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

### Step 2 — Create (or reuse) a Placement selecting the target clusters

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

### Step 3 — Create the `ManagedClusterImageRegistry` with the opt-in annotation

The `apps.open-cluster-management.io/gitops-addon-image-mirroring: "true"` annotation is
**required**. Without it, this CR is invisible to the gitops-addon image controller.

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

`spec.registries` maps one or more source registry hosts to a mirror host. If you only ever mirror
everything to a single registry regardless of source, you can instead use the simpler
`spec.registry` catch-all field (ignored whenever `spec.registries` is non-empty):

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

### Step 4 — Verify

```bash
# The controller adds a finalizer once it starts managing the CR
kubectl get managedclusterimageregistry gitops-addon-mirror -n openshift-gitops -o jsonpath='{.metadata.finalizers}'
# imageregistry.open-cluster-management.io/gitops-addon-cleanup

# Image values in the target cluster's ADC should now point at the mirror
kubectl get addondeploymentconfig gitops-addon-config -n <managed-cluster-name> -o yaml
```

You should see `spec.customizedVariables` entries (e.g. `GITOPS_OPERATOR_IMAGE`, `ARGOCD_IMAGE`,
etc.) rewritten to `my-mirror.example.com:5000/...`, and these bookkeeping annotations on the ADC:

- `imageregistry.open-cluster-management.io/managed-by`: `openshift-gitops/gitops-addon-mirror`
- `imageregistry.open-cluster-management.io/original-values`: JSON map of each mirrored
  variable's pre-mirror (source) value
- `imageregistry.open-cluster-management.io/last-mirrored-values`: JSON map of the mirrored value
  last written for each variable

The ADC being rewritten only proves the hub side did its job. To confirm the mirrored images were
actually **adopted by the GitOps operator and its ArgoCD instance on the managed cluster**, check
the spoke itself.

> **Important: this only applies to non-OCP managed clusters.** The mirrored ADC variables are
> consumed by the embedded Helm chart install path (`installViaEmbeddedManifests`), which templates
> them straight into the operator Deployment's container `image` and env vars. On **OCP** managed
> clusters the addon agent instead creates an OLM `Subscription`
> (`installViaOLMSubscription`) and never reads these image variables at all — OLM's catalog
> resolves every component image from the CSV, with zero hub-side control (see `CLAUDE.md` →
> "OCP vs Non-OCP Operator Version Gap"). Mirroring the ADC on an OCP cluster has no effect on
> what actually gets installed there; skip straight to
> [Use case 3](#use-case-3-control-the-argocd-agents-image) territory (Policy/ADC) if you need to
> influence the agent image on OCP, and rely on your OLM `CatalogSource`/`ImageContentSourcePolicy`
> setup for everything else on OCP.

Switch context to the managed cluster's own kubeconfig for the checks below:

```bash
export KUBECONFIG=/path/to/managed-cluster/kubeconfig
```

The operator itself and the ArgoCD instance it manages run in two different namespaces on the
managed cluster — find them first:

```bash
# The GitOps operator (the controller-manager Deployment) always runs in openshift-gitops-operator
oc get pods -n openshift-gitops-operator
```
```text
NAME                                                            READY   STATUS    RESTARTS   AGE
openshift-gitops-operator-controller-manager-77fbc7d7f7-9qj6v   1/1     Running   0          5d16h
```

```bash
# The ArgoCD instance the operator manages (application-controller, redis, repo-server, and the
# argocd-agent, if enabled) runs in the ArgoCD namespace -- openshift-gitops by default, or
# whatever GetEffectiveArgoNamespace()/spec.argoServer.argoNamespace resolves to
oc get pods -n openshift-gitops
```
```text
NAME                                                READY   STATUS    RESTARTS   AGE
acm-openshift-gitops-agent-agent-6d668bf7-slbnv     1/1     Running   0          73m
acm-openshift-gitops-application-controller-0       1/1     Running   0          5d16h
acm-openshift-gitops-redis-7f4bd5d6dc-ll6fp         1/1     Running   0          5d16h
acm-openshift-gitops-repo-server-6bcb9c5d8b-72prs   1/1     Running   0          5d16h
```

> `server` is disabled by default for both agent-mode and pull-model spokes (see `CLAUDE.md` →
> "Basic Pull Model Setup — Full YAML"), so it's normal not to see an
> `acm-openshift-gitops-server` pod here.

With both namespaces identified, check the actual images each pod is running:

1. **The GitOps operator Deployment's own image should already be the mirror** — its `image:` is
   templated directly from `GITOPS_OPERATOR_IMAGE`:

   ```bash
   kubectl get deployment openshift-gitops-operator-controller-manager \
     -n openshift-gitops-operator \
     -o jsonpath='{.spec.template.spec.containers[?(@.name=="manager")].image}{"\n"}'
   # expect: my-mirror.example.com:5000/redhat/...
   ```

2. **The operator's own container env vars carry the mirrored values it will use as defaults for
   every ArgoCD component it creates** (only variables with a non-empty value are rendered):

   ```bash
   kubectl get deployment openshift-gitops-operator-controller-manager \
     -n openshift-gitops-operator \
     -o jsonpath='{range .spec.template.spec.containers[?(@.name=="manager")].env[*]}{.name}={.value}{"\n"}{end}' \
     | grep -E '^(ARGOCD|BACKEND|GITOPS)_.*IMAGE='
   ```

3. **The running ArgoCD instance's own component pods should be pulling from the mirror.** The
   operator only applies its env-var image defaults to fields the `ArgoCD` CR (managed by the
   ArgoCD Policy on the hub) doesn't already override, so check the live pods rather than the CR
   spec:

   ```bash
   # application-controller, repo-server, redis, dex, server (server is disabled by default)
   kubectl get pods -n openshift-gitops -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{range .spec.containers[*]}{.image}{" "}{end}{"\n"}{end}'
   ```

   Every image column should be under `my-mirror.example.com:5000/...`, not `registry.redhat.io/...`.
   If a pod still shows the source registry, check whether it predates the mirroring change (an
   already-Running pod is not restarted just because the operator's defaults changed) — delete it
   to force a recreate with the new default, or wait for the next rollout that touches it.

4. **If the ArgoCD agent is enabled**, its Deployment image should match whichever of
   [Use case 3](#use-case-3-control-the-argocd-agents-image)'s variants you configured:

   ```bash
   kubectl get deployment -n openshift-gitops -l app.kubernetes.io/part-of=argocd-agent \
     -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.template.spec.containers[0].image}{"\n"}{end}'
   ```

---

## Use case 2: Disable image mirroring (revert to source images)

There are two ways to stop mirroring, depending on whether you want to keep the CR around.

### Option A — Remove the opt-in annotation, keep the CR

Useful if the CR is also used for something else (e.g. you plan to hand it back to ACM's own
`cluster-image-registry-controller` for klusterlet images) and you just want the gitops-addon
mirroring behavior turned off.

```bash
kubectl annotate managedclusterimageregistry gitops-addon-mirror -n openshift-gitops \
  apps.open-cluster-management.io/gitops-addon-image-mirroring-
```

The controller still sees this CR once (because its finalizer is still present), reverts every
`AddOnDeploymentConfig` it mirrored back to the recorded source values, and then removes its
finalizer. After that, the CR is fully ignored by the gitops-addon image controller again.

### Option B — Delete the `ManagedClusterImageRegistry`

```bash
kubectl delete managedclusterimageregistry gitops-addon-mirror -n openshift-gitops
```

The finalizer runs the same revert logic before the object is actually removed.

### Verify the revert

```bash
kubectl get addondeploymentconfig gitops-addon-config -n <managed-cluster-name> -o yaml
```

- The previously-mirrored `spec.customizedVariables` values are back to their source-registry
  form (e.g. `registry.redhat.io/...`).
- The `imageregistry.open-cluster-management.io/managed-by`,
  `imageregistry.open-cluster-management.io/original-values`, and
  `imageregistry.open-cluster-management.io/last-mirrored-values` annotations are gone.

As with enabling mirroring, the ADC alone only proves the hub-side revert happened. To confirm the
GitOps operator and its ArgoCD instance on the (non-OCP) managed cluster actually went back to
pulling from the source registry, go check the spoke itself the same way as
[Use case 1, Step 4](#step-4--verify): switch to the managed cluster's kubeconfig, find the
operator pod in `openshift-gitops-operator` and the ArgoCD instance's pods in `openshift-gitops`
(or your custom ArgoCD namespace), and re-run the same operator-Deployment-image, operator-env-var,
and live-pod-image checks — every image should now be back under `registry.redhat.io/...` instead
of the mirror. The same caveat about already-Running pods not being restarted just because the
default changed applies here too: a pod that predates the revert may still show the mirror image
until it's next recreated.

---

## Use case 3: Control the ArgoCD agent's image

The ArgoCD agent image is handled differently from every other addon component image: instead of a
static default, the hub controller normally auto-heals it to match whatever image the hub's
running argocd-agent principal actually uses, and it always flows through the per-cluster ADC
(`ARGOCD_AGENT_IMAGE`) — **never** the shared ArgoCD Policy. Three variants:

### 3a. Default: automatic drift heal (recommended for most users)

No extra configuration needed. Whenever the principal Deployment's image changes (e.g. after an
operator upgrade), the hub controller detects it and writes the new image into
`ARGOCD_AGENT_IMAGE` on every agent-enabled managed cluster's `gitops-addon-config` ADC. If you
also have a `ManagedClusterImageRegistry` opted in (Use case 1), that value is mirrored like any
other image.

```bash
# Confirm the ADC picked up the principal's image
kubectl get addondeploymentconfig gitops-addon-config -n <managed-cluster-name> \
  -o jsonpath='{.spec.customizedVariables[?(@.name=="ARGOCD_AGENT_IMAGE")].value}'
```

### 3b. Pin the agent image yourself via the ArgoCD Policy

If you want full manual control over the agent image and don't want it tied to the hub principal's
version, set `spec.argoCDAgent.agent.image` directly on the `ArgoCD` object template inside the
managed ArgoCD Policy. The controller **never writes or clears this field** — it's entirely yours
to manage, and the ArgoCD operator itself prefers this CR field over the `ARGOCD_AGENT_IMAGE` env
var whenever both are present, so your pinned value takes effect regardless of what the ADC
contains.

> **You must patch the root `Policy`, not a per-cluster replica.** The `GitOpsCluster` controller
> only ever creates one `Policy` object: `<gitopscluster-name>-argocd-policy`, in the **same
> namespace as the `GitOpsCluster` CR itself** (e.g. `openshift-gitops`) — that's the
> **root** policy.
> ACM's separate `governance-policy-propagator` then replicates it into each
> selected managed cluster's own namespace on the hub, named
> `<gitopscluster-namespace>.<gitopscluster-name>-argocd-policy` (e.g.
> `openshift-gitops.mc-gitops-agent-argocd-policy` inside `eks-cluster1`'s namespace) — that's the
> **replica** policy. Any edit made directly to a replica policy is
> transient: the propagator re-syncs it from the root on its next pass and silently overwrites your
> change back to whatever the root says. Always confirm you're patching the root first:
> `kubectl get policy <gitopscluster-name>-argocd-policy -n <gitopscluster-namespace>`.

```bash
kubectl patch policy <gitopscluster-name>-argocd-policy -n <gitopscluster-namespace> --type=json -p='[
  {"op":"add","path":"/spec/policy-templates/0/objectDefinition/spec/object-templates/0/objectDefinition/spec/argoCDAgent/agent/image",
   "value":"my-mirror.example.com:5000/argocd-agent:v0.9.0"}
]'
```

> The exact `policy-templates`/`object-templates` array index depends on your Policy's structure —
> inspect it first with `kubectl get policy <gitopscluster-name>-argocd-policy -n <gitopscluster-namespace> -o yaml`
> and adjust the JSONPatch indices to point at the `ArgoCD` object named `acm-openshift-gitops`.

Because drift heal still runs by default, the ADC's `ARGOCD_AGENT_IMAGE` value will keep tracking
the hub principal's image in the background even while the Policy value wins on the spoke — this
is harmless, but if you'd rather the ADC not show a value that isn't actually being used, combine
this with 3c below.

### 3c. Pin the agent image via the AddOnDeploymentConfig, with drift heal disabled

Use this when you want to manage `ARGOCD_AGENT_IMAGE` directly (e.g. hand-set it per cluster, or
let only `ManagedClusterImageRegistry` mirroring drive it) without the hub controller's drift heal
overwriting your value on every reconcile.

Step 1 — Annotate the `GitOpsCluster` to opt out of agent-version drift heal:

```yaml
apiVersion: apps.open-cluster-management.io/v1beta1
kind: GitOpsCluster
metadata:
  name: mc-gitops-agent
  namespace: openshift-gitops
  annotations:
    apps.open-cluster-management.io/skip-agent-version-heal: "true"
spec:
  # ... existing spec unchanged ...
```

```bash
kubectl annotate gitopscluster mc-gitops-agent -n openshift-gitops \
  apps.open-cluster-management.io/skip-agent-version-heal=true
```

With this annotation set, `ARGOCD_AGENT_IMAGE` is dropped from the *managed* set of variables the
GitOpsCluster controller writes to the ADC — the hub controller will neither add it nor overwrite
it from this point on. This is **not** the same as the key being removed or reset:

- **If `ARGOCD_AGENT_IMAGE` doesn't exist on the ADC yet** (e.g. a brand-new `GitOpsCluster` that
  had the annotation set from the start), it's simply never written — the ArgoCD operator's own
  built-in default takes over.
- **If `ARGOCD_AGENT_IMAGE` already exists on the ADC** (the common case — drift heal will have
  already written one before you added the annotation), it is left exactly as-is, frozen at
  whatever value it last had. The annotation stops *future* writes; it does not retroactively
  clear or touch the existing value. If a `ManagedClusterImageRegistry` is also opted in
  (Use case 1), that value keeps being tracked/re-mirrored by the image-registry controller too,
  since mirroring only cares about what's currently in `customizedVariables`, not who put it
  there or why — this is expected, not a conflict.

Step 2 — Set the value yourself on the ADC (only needed if you want a different value than
whatever is already frozen there — skip this if the existing/frozen value is already what you
want):

```bash
kubectl patch addondeploymentconfig gitops-addon-config -n <managed-cluster-name> --type=json -p='[
  {"op":"add","path":"/spec/customizedVariables/-","value":{"name":"ARGOCD_AGENT_IMAGE","value":"my-mirror.example.com:5000/argocd-agent:v0.9.0"}}
]'
```

> If the variable already exists, `add` on an array index replaces via `-` only appends — check
> first with `kubectl get addondeploymentconfig gitops-addon-config -n <managed-cluster-name> -o
> jsonpath='{.spec.customizedVariables[?(@.name=="ARGOCD_AGENT_IMAGE")]}'`; if it's already
> present, patch that specific array entry's `value` field instead of appending a duplicate.

If a `ManagedClusterImageRegistry` is already opted in (Use case 1) and its `registries`/`registry`
config matches this value's host, the image-registry controller will mirror it exactly like any
other tracked variable on its next reconcile.

#### Re-enabling drift heal (removing the annotation)

Removing `skip-agent-version-heal` puts `ARGOCD_AGENT_IMAGE` back under the hub controller's
active management. It may take a few minutes to see the customized ARGOCD_AGENT_IMAGE value in addondeploymentconfig gets reverted.

### Summary: which one to use

| Goal | Mechanism |
|---|---|
| Agent image should always match the hub principal | Do nothing (3a, default) |
| Agent image should be pinned via Policy, independent of the principal | Set `spec.argoCDAgent.agent.image` in the Policy (3b) |
| Agent image should be a specific value on the ADC, with no hub auto-heal interference | `skip-agent-version-heal: "true"` + set `ARGOCD_AGENT_IMAGE` on the ADC yourself (3c) |
| Only stop drift heal from *reading* the principal, on a `GitOpsCluster` that never had `ARGOCD_AGENT_IMAGE` written yet | `skip-agent-version-heal: "true"` alone — the variable is simply never added; the ArgoCD operator's own built-in default applies |
| Only stop drift heal, on a `GitOpsCluster` where `ARGOCD_AGENT_IMAGE` was already written before you annotated it | `skip-agent-version-heal: "true"` alone freezes the existing value in place (does **not** clear it) — patch or remove it yourself on the ADC if you don't want that frozen value |

---

## End-to-end example: disconnected cluster with agent mode, image mirroring, and a pinned agent image

```yaml
---
apiVersion: v1
kind: Secret
metadata:
  name: mirror-registry-pull-secret
  namespace: openshift-gitops
type: kubernetes.io/dockerconfigjson
data:
  .dockerconfigjson: <base64-encoded-docker-config>
---
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
---
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
---
apiVersion: apps.open-cluster-management.io/v1beta1
kind: GitOpsCluster
metadata:
  name: mc-gitops-agent
  namespace: openshift-gitops
  annotations:
    apps.open-cluster-management.io/skip-agent-version-heal: "true"
spec:
  argoServer:
    cluster: local-cluster
    argoNamespace: openshift-gitops
  placementRef:
    kind: Placement
    apiVersion: cluster.open-cluster-management.io/v1beta1
    name: disconnected-clusters-placement
  gitopsAddon:
    enabled: true
    argoCDAgent:
      enabled: true
      mode: managed
```

After the `GitOpsCluster` and MCIR both reconcile:

1. The `gitops-addon-config` ADC in each selected managed cluster's namespace has its
   `GITOPS_OPERATOR_IMAGE`/`ARGOCD_IMAGE`/etc. values mirrored to `my-mirror.example.com:5000/...`.
2. `ARGOCD_AGENT_IMAGE` is **not** auto-healed (because of `skip-agent-version-heal`) — either set
   it yourself on the ADC (Use case 3c) or pin `spec.argoCDAgent.agent.image` on the Policy
   (Use case 3b); either way the value can also be mirrored by the same MCIR if its host matches
   one of the configured `registries` entries.

## Troubleshooting

- **`addOnDeploymentConfig ... is already managed by ManagedClusterImageRegistry ..., refusing to
  take over`**: two different `ManagedClusterImageRegistry` objects (both opted in) resolve to the
  same managed cluster. Only one MCIR can own a given cluster's ADC mirroring at a time — adjust
  the placements so they don't overlap, or consolidate into a single MCIR.
- **Mirroring doesn't apply to a cluster**: confirm the cluster already has the `gitops-addon`
  `ManagedClusterAddOn` and a `gitops-addon-config` ADC (i.e. the `GitOpsCluster` with
  `gitopsAddon.enabled: true` has already reconciled that cluster) — the image-registry controller
  has nothing to rewrite until that exists.
- **A source image value keeps reverting instead of staying mirrored**: check whether the
  GitOpsCluster's desired source value actually changed (hub image upgrade, spec override, agent
  drift heal) — that's the one case where `CreateAddOnDeploymentConfig` intentionally writes
  through a fresh source value for the image-registry controller to re-mirror on its next pass.
- **Removed the opt-in annotation but the ADC still shows mirrored values**: give the controller a
  reconcile pass — the revert happens on the same event that removes the finalizer, not
  instantaneously. If it's still not reverted after a few minutes, check the controller logs for
  errors resolving the MCIR's `placementRef`.
- **A custom `spec.argoCDAgent.agent.image` set on the Policy (Use case 3b) keeps getting reverted
  back to `""`**: this is a known behavior of *older* hub controller builds, not the current code.
  A previous version of `reconcileArgoCDPolicyAgentSpec` actively reset any non-empty
  `agent.image` back to `""` on every `GitOpsCluster` reconcile (logged as `resetting stale
  argoCDAgent.agent.image=... to "" (now handled per-cluster via AddOnDeploymentConfig
  instead)`), on the theory that the image should only ever come from the ADC. That behavior has
  been removed — the current controller never writes or clears `agent.image`, so a user-set value
  is left alone indefinitely. If you're hitting this, check the controller pod's logs for that
  exact message; if it's present, the running image predates the fix and needs to be rebuilt/
  redeployed (see `CLAUDE.md` → "Build / Push / Redeploy Workflow") before Policy-pinning the
  agent image will actually stick.
- **Edited the Policy but the change didn't take, or reverted on its own for an unrelated
  reason**: make sure you're editing the **root** Policy (`<gitopscluster-name>-argocd-policy`, in
  the same namespace as the `GitOpsCluster` CR, e.g. `openshift-gitops`), not the **replica**
  (`<argocd-namespace>.<gitopscluster-name>-argocd-policy`, in the managed cluster's own
  namespace). The governance-policy-propagator continuously re-syncs every replica from the root,
  so any direct edit to a replica is transient and gets overwritten back to whatever the root
  says on the next sync.
- **Removed `skip-agent-version-heal` but the ADC's `ARGOCD_AGENT_IMAGE` value looks unchanged**:
  this is expected, not a stuck reconcile, when a `ManagedClusterImageRegistry` is opted in and the
  principal's image hasn't actually drifted since the value was last mirrored — drift heal
  re-computes the value on the very next reconcile (seconds, driven by the watch on the
  `GitOpsCluster`, not a slow poll), but if the freshly-healed source matches the recorded
  `original-values` entry, `preserveMirroredImageValues` swaps the existing mirrored value straight
  back in, so the string on the ADC ends up identical even though the write genuinely happened.
  Verify via the controller logs instead of the value — see
  [3c's "Re-enabling drift heal"](#re-enabling-drift-heal-removing-the-annotation) for the exact
  `grep` commands. It only produces a visibly different value once the underlying source (the
  principal's image) actually changes.
