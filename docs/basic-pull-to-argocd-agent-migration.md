# Migrating from the Basic Pull Model to argocd-agent

This is a runbook for moving apps off the classic ManifestWork-based "basic pull model" (see
CLAUDE.md → "Basic Pull Model" under Architecture) and onto argocd-agent, without creating a new
`Application`/`ApplicationSet` and without disrupting the real workload underneath. For the
architectural background and the two rejected designs that led to the approach below, see CLAUDE.md
→ "Migrating an App from Basic Pull Model to argocd-agent".

## What this achieves

- **argocd-agent takes over the exact same `Application` object** — same name, same namespace, same
  `.metadata.uid`. No new `Application` or `ApplicationSet` is ever created.
- The app's real workload (Deployment/Service, etc.) is never deleted or recreated — no pod restart
  happens at any point.
- Once complete, the old pull-model machinery (`multicluster-integrations`'s propagation,
  search-sync, and aggregation controllers) never touches the app again.
- The agent genuinely takes over reconciliation — not just the hub's view of the app. Getting this
  right requires one more step than you might expect (Step 5): the app's spoke-side copy, delivered
  there by the classic pull model, is not automatically hand-off-able to the agent and needs to be
  explicitly replaced. Skipping Step 5 leaves an app that *looks* migrated (`Synced` on both hub and
  spoke) but is still being reconciled the old way underneath, with the agent doing nothing.

## Two kinds of pull-model apps

Not every basic-pull-model `Application` comes from an `ApplicationSet`. The pull label
(`apps.open-cluster-management.io/pull-to-ocm-managed-cluster: "true"`) and the
`ocm-managed-cluster`/`ocm-managed-cluster-app-namespace`/`skip-reconcile` annotations are all that
the propagation controller actually requires — a hand-created, standalone `Application` carrying
them gets pulled and reconciled on the spoke exactly the same way (the one difference is purely
cosmetic: only `ApplicationSet`-owned apps get a `MulticlusterApplicationSetReport`, since
`multiclusterstatusaggregation` keys off the `application-set` label that only an owned app has).

Which kind an app is changes **only** the takeover step (Step 4 below):

- **`ApplicationSet`-owned app**: take it over by editing the `ApplicationSet`'s template. The
  `ApplicationSet` controller then re-asserts the change onto the existing `Application` in place.
- **Standalone `Application`** (no `ApplicationSet` owner): there's no template to edit — take it
  over by patching the `Application` object itself, directly and once. Nothing re-asserts a
  standalone app's spec, so a direct patch is permanent with no template to keep in sync afterward.

Check which kind you have before starting:

```bash
kubectl get applications.argoproj.io $APP_NAME -n $ARGOCD_NS -o jsonpath='{.metadata.ownerReferences}'
```

An empty result means standalone; an `ApplicationSet` entry means `ApplicationSet`-owned. Steps 1-3
below are identical either way — only Step 4 branches. Step 5 applies to both, and is not optional
— see why in Step 5 itself.

## Overview

The migration has two parts:

1. **Disable the basic pull model, hub-wide, once.** This is a single ConfigMap edit plus a pod
   restart. It stops all three basic-pull-model controllers from reconciling anything, and freezes
   every existing pull-model `ManifestWork` in place (`updateStrategy: ReadOnly`) so spokes stop
   re-enforcing the delivered `Application` specs — without deleting anything or touching the real
   workloads. Do this before migrating any individual app. This step covers `ApplicationSet`-owned
   and standalone apps identically — the sweep matches on the `Application`/`ManifestWork`'s own
   annotations and labels, not on how the `Application` was created.
2. **Per managed cluster, transform each app over to argocd-agent.** This is the part that actually
   changes how an app is dispatched and reconciled — repeat Steps 4-5 for every `ApplicationSet` and
   every standalone `Application` you want to move (Steps 2-3 are per-cluster, not per-app — do them
   once per managed cluster, then migrate every app on that cluster with Steps 4-5).

## Before you start

You need:
- Agent-mode prerequisites already on the hub: `ManagedClusterSetBinding` for `global` in the
  ArgoCD namespace, the default `AppProject` patched with `destinations: [{name: "*", ...}]`, and
  the hub's own ArgoCD instance already running as an argocd-agent principal
  (`spec.argoCDAgent.principal.enabled: true`). See CLAUDE.md → "Agent Mode Setup — Full YAML" if
  any of this isn't done yet.
- For each app you're migrating: the hub `GitOpsCluster` object managing its target cluster (call
  its name `$GITOPSCLUSTER`), the app's name (`$APP_NAME`) and namespace (`$ARGOCD_NS`, normally
  `openshift-gitops`), and the target `ManagedCluster` name (`$CLUSTER_NAME`). If the app is
  `ApplicationSet`-owned, also note the `ApplicationSet`'s name (`$PULL_APPSET`).

This works whether the pull model was set up via `gitopsAddon.enabled: true` or via
`createBlankClusterSecrets: true` directly — the latter is how the pull model can exist completely
independent of the addon; see CLAUDE.md → "Basic Pull Model" under Architecture.

**Basic pull model is OCP-only.** This runbook applies to apps delivered to OCP managed clusters.

## Step 1 — Disable the basic pull model (one-time, hub-wide)

This step covers every existing pull-model app across every managed cluster in a single pass — you
do not need to repeat it per app or per cluster, and you do not need to manually find and patch any
`ManifestWork` yourself.

1. Find the namespace the `multicluster-integrations` Deployment runs in (normally
   `open-cluster-management`):

   ```bash
   kubectl get deployment -A --field-selector metadata.name=multicluster-integrations
   ```

2. Edit (or create, if it doesn't exist yet) the `multicluster-integrations-config` ConfigMap in
   that namespace:

   ```bash
   kubectl get configmap multicluster-integrations-config -n <namespace> -o yaml
   ```

   If it doesn't exist, one of the controller's containers will self-create it with defaults the
   first time it starts — either wait for it to appear, or create it yourself directly:

   ```bash
   cat > /tmp/config.yaml <<'EOF'
   pullModel:
     basic:
       disabled: true
   EOF
   kubectl create configmap multicluster-integrations-config -n <namespace> \
     --from-file=config.yaml=/tmp/config.yaml
   ```

   `--from-file=config.yaml=<path>` stores the file's raw bytes under that key with no shell
   quoting/escaping involved at all — simpler than the patch form below, and the right choice
   whenever the ConfigMap doesn't exist yet. If it already exists, `kubectl create` just fails
   with `AlreadyExists` — fall through to editing it in place instead.

   Set (or add) `pullModel.basic.disabled: true` under the `config.yaml` key on an **existing**
   ConfigMap. `config.yaml` is a
   single YAML blob (not flat ConfigMap keys), so a merge patch on `data` replaces the **entire**
   string — do not just paste a `pullModel`-only patch if the ConfigMap already has other sections
   under `config.yaml` (current or future). Edit the existing content instead:

   ```bash
   kubectl get configmap multicluster-integrations-config -n <namespace> -o jsonpath='{.data.config\.yaml}' > /tmp/config.yaml
   # edit /tmp/config.yaml: add or change pullModel.basic.disabled: true, leaving any other
   # sections untouched
   # jq --rawfile safely JSON-encodes the file's content (quotes, backslashes, newlines) --
   # do not hand-build the JSON string with sed/string interpolation, it will break on any
   # YAML value containing a quote or backslash.
   kubectl patch configmap multicluster-integrations-config -n <namespace> --type=merge \
     -p "$(jq -n --rawfile cfg /tmp/config.yaml '{"data":{"config.yaml":$cfg}}')"
   ```

   If you know the ConfigMap has no other sections yet (a fresh install), the simpler one-shot form
   is fine:

   ```bash
   kubectl patch configmap multicluster-integrations-config -n <namespace> --type=merge -p '{
     "data": {
       "config.yaml": "pullModel:\n  basic:\n    disabled: true\n"
     }
   }'
   ```

3. Restart the pod. The reconcile-time guards themselves check this ConfigMap live and would pick
   up the change within moments even without a restart, but the one-time `ManifestWork` sweep
   (step 5 below) only ever runs once, at process startup — a restart is what triggers it:

   ```bash
   kubectl rollout restart deployment multicluster-integrations -n <namespace>
   kubectl rollout status deployment multicluster-integrations -n <namespace>
   ```

4. Confirm the disable took effect. The controller-manager container logs a line when it sees the
   config is disabled, and reports the outcome of the one-time `ManifestWork` sweep:

   ```bash
   kubectl logs -n <namespace> deployment/multicluster-integrations \
     -c argocd-pull-integration-controller-manager | grep -E "pull-model disable|disable sweep"
   ```

   You should see `basic pull model is disabled via multicluster-integrations-config; sweeping
   existing ManifestWorks to ReadOnly` followed by a summary line like `pull-model disable sweep
   complete: N patched to ReadOnly, M already ReadOnly, K not pull-model-owned (skipped)`.

5. Confirm every pull-model `ManifestWork` is now `ReadOnly`:

   ```bash
   kubectl get manifestwork -A -o json | python3 -c "
   import json, sys
   d = json.load(sys.stdin)
   for i in d['items']:
       ann = i.get('metadata', {}).get('annotations', {}) or {}
       if 'apps.open-cluster-management.io/hub-application-name' not in ann:
           continue
       for mc in i['spec'].get('manifestConfigs', []):
           ri = mc.get('resourceIdentifier', {})
           if ri.get('group') == 'argoproj.io' and ri.get('resource') == 'applications':
               strat = (mc.get('updateStrategy') or {}).get('type')
               print(i['metadata']['namespace'], i['metadata']['name'], strat)
   "
   ```

   Every row should read `ReadOnly`.

What just happened, at the code level:
- The propagation controller's `Application` and `Application`-status reconcilers now return
  immediately on every event, for every app — no ManifestWork is created, updated, or deleted by
  this component from this point on.
- `gitopssyncresc` (the ACM Search polling loop) and `multiclusterstatusaggregation` (the
  `MulticlusterApplicationSetReport` generator) both skip their periodic work entirely.
- The sweep is **best-effort, not guaranteed**: each matching `ManifestWork`'s `applications`
  manifest config gets its `updateStrategy` set to `ReadOnly`, but a failed update (e.g. a
  resourceVersion conflict) is logged and skipped rather than retried indefinitely — check the log
  line from step 4 above and the per-`ManifestWork` verification in step 5 below; if any row isn't
  `ReadOnly`, either fix it directly or rerun the sweep (restart the pod again) before migrating
  that app. Once genuinely applied, this stops the spoke's klusterlet work-agent from re-asserting
  the delivered `Application` spec, without deleting anything. Health/sync/operation status
  feedback continues flowing normally; only spec enforcement stops.
- None of this touches `pkg/controller/gitopscluster` or anything used by argocd-agent — this is a
  pure basic-pull-model shutoff switch.

**Never delete a pull-model `ManifestWork`, at any point — before, during, or after this step.**
Deleting it (even once it's `ReadOnly`) cascades through the delivered Application's
`resources-finalizer.argocd.argoproj.io` and deletes the real workload resources. Leave every
pull-model `ManifestWork` in place permanently; once `ReadOnly`, it's inert and harmless.

Record the workload's pod `uid` now, before doing anything else — you'll compare against it after
every subsequent step:

```bash
kubectl --kubeconfig $SPOKE_KUBECONFIG get pods -n <dest-namespace> -o jsonpath='{.items[0].metadata.uid}'
```

## Step 2 — Enable argoCDAgent on the GitOpsCluster

Repeat steps 2-4 for each managed cluster / `ApplicationSet` you're migrating.

Patch the existing `GitOpsCluster` in place — do not create a new one. Do this **before** Step 4's
template change, so the agent cluster secret already exists by the time the destination needs to
resolve against it:

```bash
kubectl patch gitopscluster $GITOPSCLUSTER -n $ARGOCD_NS --type=merge -p '{
  "spec": {"gitopsAddon": {"enabled": true, "argoCDAgent": {"enabled": true, "mode": "managed"}}}
}'
```

Wait for (each can take a few reconcile passes — cert chain generation is sequential, this is
expected, not a stuck reconcile):

```bash
# ARGOCD_AGENT_ENABLED flips to true
kubectl get addondeploymentconfig gitops-addon-config -n $CLUSTER_NAME \
  -o jsonpath='{.spec.customizedVariables[?(@.name=="ARGOCD_AGENT_ENABLED")].value}'

# agent cluster secret appears on the hub, correctly labeled AND annotated skip-reconcile: "true"
# (skip-reconcile is what keeps the hub's own app-controller from fighting the agent for this
# cluster -- see CLAUDE.md -> "Skip-Reconcile Annotation on Agent Cluster Secrets"). Assert both
# explicitly rather than just eyeballing the printed value -- proceeding with either one missing
# means either the agent was never actually wired up, or the hub app-controller will fight the
# agent for this cluster once Step 4 dispatches to it.
AGENT_NAME_LABEL=$(kubectl get secret cluster-$CLUSTER_NAME -n $ARGOCD_NS \
  -o jsonpath='{.metadata.labels.argocd-agent\.argoproj-labs\.io/agent-name}')
if [ "$AGENT_NAME_LABEL" != "$CLUSTER_NAME" ]; then
  echo "agent cluster secret is missing its agent-name label (got '$AGENT_NAME_LABEL') -- stop, do not proceed to Step 3/4" >&2
  exit 1
fi

SKIP_RECONCILE=$(kubectl get secret cluster-$CLUSTER_NAME -n $ARGOCD_NS \
  -o jsonpath='{.metadata.annotations.argocd\.argoproj\.io/skip-reconcile}')
if [ "$SKIP_RECONCILE" != "true" ]; then
  echo "agent cluster secret is missing skip-reconcile: \"true\" (got '$SKIP_RECONCILE') -- stop, do not proceed to Step 3/4" >&2
  exit 1
fi
```

## Step 3 — Resolve a pre-existing, differently-named ArgoCD instance (if any)

**Check this even if you think you don't need to.** If the managed cluster already had its own
ArgoCD instance under a name other than `acm-openshift-gitops` (e.g. a manually installed default
`openshift-gitops` instance from before this cluster ever used the addon), it now coexists with the
new addon-managed `acm-openshift-gitops` instance **in the same namespace**. ArgoCD's core settings
(`argocd-cm`, `argocd-rbac-cm`, `argocd-secret`) are shared per-namespace, not per-instance — two
ArgoCD CRs in the same namespace fight over them continuously (symptom: `argocd-server` stuck
permanently restarting on `"url modified. restarting"`).

```bash
kubectl --kubeconfig $SPOKE_KUBECONFIG get argocd -n $ARGOCD_NS
```

**Before deleting the other instance, inventory every `Application` in this namespace** — deleting
it removes its control-plane, so *every* `Application` it was reconciling (not just the one you're
migrating) loses that reconciliation, not only the app you're migrating:

```bash
kubectl --kubeconfig $SPOKE_KUBECONFIG get applications.argoproj.io -n $ARGOCD_NS
```

For each app in that list other than the one you're migrating, either plan to migrate it too (repeat
this runbook for it) or explicitly accept that it stops being reconciled once the old instance is
gone. Do not delete the old ArgoCD CR until you've made a call on every app it lists — an app
silently losing reconciliation is easy to miss until something drifts.

```bash
# only once you've accounted for every app the old instance was reconciling:
kubectl --kubeconfig $SPOKE_KUBECONFIG delete argocd <the-other-name> -n $ARGOCD_NS
```

This is safe for the workloads themselves — deleting an ArgoCD CR does not cascade to the
Applications or real workloads it was reconciling (they aren't Kubernetes-owned by the ArgoCD CR),
only to its own control-plane components (application-controller, repo-server, redis, server).
Confirm the workload's pod `uid` is unchanged before and after.

Then wait for the agent pod to reach **two consecutive stable `Running` samples**, not just one — a
crash-looping pod can report `Running` momentarily between restarts:

```bash
kubectl --kubeconfig $SPOKE_KUBECONFIG get pods -n $ARGOCD_NS -l app.kubernetes.io/part-of=argocd-agent
```

Wait a couple of minutes and recheck the workload's pod `uid` again before proceeding — an
immediate check after a risky step is not sufficient proof that it was safe (see "A standing
caution" below for why).

## Step 4 — The takeover

Pick the case that matches this app (see "Two kinds of pull-model apps" above).

### Case A: the app is generated by an ApplicationSet

Transform the `ApplicationSet`'s own template in place — it's already the durable, self-enforcing
source of truth for the generated `Application`, so changing what the template *says* changes the
live object in place, without ever deleting or recreating it.

```bash
kubectl patch applicationset $PULL_APPSET -n $ARGOCD_NS --type=json -p='[
  {"op":"remove","path":"/spec/template/metadata/labels/apps.open-cluster-management.io~1pull-to-ocm-managed-cluster"},
  {"op":"remove","path":"/spec/template/metadata/annotations/apps.open-cluster-management.io~1ocm-managed-cluster"},
  {"op":"remove","path":"/spec/template/metadata/annotations/apps.open-cluster-management.io~1ocm-managed-cluster-app-namespace"},
  {"op":"remove","path":"/spec/template/metadata/annotations/argocd.argoproj.io~1skip-reconcile"},
  {"op":"remove","path":"/spec/template/spec/destination/server"},
  {"op":"add","path":"/spec/template/spec/destination/name","value":"{{name}}"}
]'
```

### Case B: the app is a standalone Application (no ApplicationSet)

There's no template — patch the `Application` object directly, once. Nothing else re-asserts a
standalone app's spec, so this single patch is permanent:

```bash
kubectl patch applications.argoproj.io $APP_NAME -n $ARGOCD_NS --type=json -p='[
  {"op":"remove","path":"/metadata/labels/apps.open-cluster-management.io~1pull-to-ocm-managed-cluster"},
  {"op":"remove","path":"/metadata/annotations/apps.open-cluster-management.io~1ocm-managed-cluster"},
  {"op":"remove","path":"/metadata/annotations/apps.open-cluster-management.io~1ocm-managed-cluster-app-namespace"},
  {"op":"remove","path":"/metadata/annotations/argocd.argoproj.io~1skip-reconcile"},
  {"op":"remove","path":"/spec/destination/server"},
  {"op":"add","path":"/spec/destination/name","value":"'"$CLUSTER_NAME"'"}
]'
```

Note the one real difference from Case A: `destination.name` is set to the literal `$CLUSTER_NAME`
value, not the `{{name}}` placeholder — there's no `ApplicationSet` generator to template it, so
you supply the actual cluster name directly.

### What this changes and why (both cases)

- Removing the pull label and the two `ocm-managed-cluster*` annotations means this app is no
  longer pull-model-shaped — moot at the code level once Step 1 is done hub-wide, but keeps the
  object's own metadata honest about what it actually is now.
- Removing `skip-reconcile` is required — the whole point is for this Application to actually be
  reconciled now, by the agent, instead of sitting inert on the hub.
- Changing `destination` from the literal `server: https://kubernetes.default.svc` to a
  `name`-based destination makes it resolve through the agent cluster secret from Step 2 instead of
  the fake/hub-local destination — this is what actually routes it through the principal → agent
  path.

Verify immediately, then again after a real wait (same checks for both cases):

```bash
# The hub Application's uid must be IDENTICAL before and after this patch. If it changes,
# something deleted and recreated the object instead of updating it in place -- stop and
# investigate before proceeding.
kubectl get applications.argoproj.io $APP_NAME -n $ARGOCD_NS -o jsonpath='{.metadata.uid}'

# Both hub and spoke copies reach Synced
kubectl get applications.argoproj.io $APP_NAME -n $ARGOCD_NS -o jsonpath='{.status.sync.status}'
kubectl --kubeconfig $SPOKE_KUBECONFIG get applications.argoproj.io $APP_NAME -n $ARGOCD_NS -o jsonpath='{.status.sync.status}'

# The real workload's pod uid is UNCHANGED from what you recorded in Step 1
kubectl --kubeconfig $SPOKE_KUBECONFIG get pods -n <dest-namespace> -o jsonpath='{.items[0].metadata.uid}'
```

Wait a couple of minutes and recheck all three again before considering the app fully migrated.

## Step 5 — Confirm genuine agent management (do not skip this)

The `Synced` status you just checked can be misleading, and this step is not optional. This app
was, by definition, delivered to the spoke at least once via the classic pull model — the
klusterlet work-agent applied it there directly, as a plain Kubernetes object. That pre-existing
copy is invisible to argocd-agent's own bookkeeping: the agent has no record of ever having created
it, so it cannot take real control of it. Step 4 only changed the **hub's** view of the app; the
spoke's copy does not automatically get replaced, and can keep being reconciled by whatever was
already reconciling it before — which, from the hub's perspective, still reports back as `Synced`,
even though the agent isn't actually managing anything.

Check the spoke's copy directly (`-o json | jq` rather than `-o jsonpath` on the whole object, since
`jsonpath`'s formatting of a nested object isn't reliably valid JSON across `kubectl` versions):

```bash
kubectl --kubeconfig $SPOKE_KUBECONFIG get applications.argoproj.io $APP_NAME -n $ARGOCD_NS -o json | jq '.spec.destination'
```

- `{"name":"in-cluster", ...}` (or another agent-resolved name) — the agent already has genuine
  control. Nothing more to do; skip the rest of this step.
- `{"server":"https://kubernetes.default.svc", ...}` (the old pull-model value) — the agent has
  **not** taken over. The spoke's stale copy needs to be replaced:

```bash
set -e   # fail closed: stop at the first error rather than risk deleting under an unverified state

# On the SPOKE: remove automated first, so the delete below does not prune the real resources.
# A merge patch setting it to null is idempotent -- unlike a JSON-patch "remove", it does not
# error if automated is already absent (e.g. you're re-running this after a partial attempt).
kubectl --kubeconfig $SPOKE_KUBECONFIG patch applications.argoproj.io $APP_NAME -n $ARGOCD_NS \
  --type=merge -p '{"spec":{"syncPolicy":{"automated":null}}}'

# On the SPOKE: remove ONLY the ArgoCD finalizer -- preserve any other, unrelated finalizer the
# object might carry, rather than blindly clearing the whole list. Make this patch conditional on
# the resourceVersion we just read: including metadata.resourceVersion in a merge-patch body is a
# genuine, server-enforced precondition -- unlike the finalizer list itself, resourceVersion is
# never merged, so if the object changed since our read (e.g. something re-added the finalizer
# concurrently) the API server rejects the whole patch with a 409 Conflict instead of silently
# clobbering whatever is there. This is a real optimistic-concurrency check, not just an
# application-level read-then-act race (confirmed live: a merge patch carrying a stale
# resourceVersion is rejected with "Operation cannot be fulfilled ... the object has been
# modified").
CURRENT=$(kubectl --kubeconfig $SPOKE_KUBECONFIG get applications.argoproj.io $APP_NAME -n $ARGOCD_NS -o json)
CURRENT_RV=$(echo "$CURRENT" | jq -r '.metadata.resourceVersion')
NEW_FINALIZERS=$(echo "$CURRENT" | jq -c '[.metadata.finalizers[]? | select(. != "resources-finalizer.argocd.argoproj.io")]')
PATCHED=$(kubectl --kubeconfig $SPOKE_KUBECONFIG patch applications.argoproj.io $APP_NAME -n $ARGOCD_NS \
  --type=merge -o json -p "{\"metadata\":{\"resourceVersion\":\"$CURRENT_RV\",\"finalizers\":$NEW_FINALIZERS}}") || {
  echo "finalizer-removal patch was rejected (object changed since our read) -- stop, re-verify before retrying" >&2
  exit 1
}

# The patch response above IS the server's confirmed post-patch state -- no separate read-back is
# needed to check the finalizer list, since the conditional patch already proves nothing else
# touched the object between our read and this write. Only something reconciling in the instant
# AFTER our patch succeeded could still re-add a finalizer, which the delete-time check below
# catches.
REMAINING=$(echo "$PATCHED" | jq -c '.metadata.finalizers // []')
if [ "$REMAINING" != "[]" ]; then
  echo "finalizer(s) still present after patch ($REMAINING) -- stop, do not delete, investigate before retrying" >&2
  exit 1
fi

# kubectl delete has no resourceVersion precondition flag at all -- "the delete command does NOT
# do resource version checks" (kubectl delete --help, verified against a live cluster). This
# immediate re-check right before deleting is the closest practical equivalent: abort if the
# object changed again in the (now much smaller) window between our just-confirmed atomic patch
# above and this delete call, rather than deleting an object we no longer have a verified-clean
# view of.
RESOURCE_VERSION=$(echo "$PATCHED" | jq -r '.metadata.resourceVersion')
CURRENT_RESOURCE_VERSION=$(kubectl --kubeconfig $SPOKE_KUBECONFIG get applications.argoproj.io $APP_NAME -n $ARGOCD_NS \
  -o jsonpath='{.metadata.resourceVersion}')
if [ "$CURRENT_RESOURCE_VERSION" != "$RESOURCE_VERSION" ]; then
  echo "object changed (resourceVersion $RESOURCE_VERSION -> $CURRENT_RESOURCE_VERSION) since the patch -- stop, re-verify before retrying" >&2
  exit 1
fi
kubectl --kubeconfig $SPOKE_KUBECONFIG delete applications.argoproj.io $APP_NAME -n $ARGOCD_NS --wait=true

# Confirm the real workload survived this (pod uid unchanged from what you recorded earlier)
kubectl --kubeconfig $SPOKE_KUBECONFIG get pods -n <dest-namespace> -o jsonpath='{.items[0].metadata.uid}'

# On the HUB: nudge a fresh dispatch -- the very first create attempt after the stale copy is
# removed can race and silently fail once; any harmless metadata touch forces a clean retry
kubectl annotate applications.argoproj.io $APP_NAME -n $ARGOCD_NS migration-nudge=1 --overwrite
```

Wait ~30 seconds, then recheck the spoke copy's `destination` again — it should now show a
`name`-based destination, confirming genuine agent management. Recheck the workload's pod `uid`
one more time. This has been confirmed safe live, for both `ApplicationSet`-owned and standalone
apps: the real workload's pod `uid` never changes across this replacement, because you removed
`syncPolicy.automated` *before* deleting the stale copy — ArgoCD only prunes on delete when
`automated` (with `prune: true`) is still active.

**That's the whole per-app migration.** No detach step, no other cleanup step — for Case A, the
same `ApplicationSet` is now the permanent, ongoing manager of the agent-dispatched app, exactly
like any other agent-mode `ApplicationSet`; for Case B, the `Application` you patched is now a
normal, permanent agent-dispatched app with no other object managing it. The frozen `ManifestWork`
from Step 1 is the only leftover either way; leave it in place permanently.

## A standing caution (not specific to migration)

**An immediate post-action check is not proof that a step was safe.** A pod `uid` unchanged one
second after a risky operation only means nothing had cascaded *yet* — some failure modes (see
CLAUDE.md → "Designs that were rejected" for a real example) take real wall-clock minutes to
finish. Re-verify after a wait, not just immediately, at every step above.

**Never delete an `ApplicationSet` whose generated `Application` has an active
`resources-finalizer.argocd.argoproj.io` while `syncPolicy.automated` is still set** — that
combination is what deletes the real managed resources. This section is about decommissioning a
**still-pull-model** app you've decided not to migrate — for decommissioning an app *after* you've
already migrated it, see the warning further below instead; the procedure is different and simpler.
To decommission a pull-model app before migrating it, the safe order is:
1. Remove `syncPolicy.automated` from the `ApplicationSet`'s template first.
2. Confirm that change reached the live Application (`spec.syncPolicy` no longer has `automated`).
3. Delete the `ApplicationSet`. Do this *before* touching the Application's finalizer — with the
   `ApplicationSet` still present, stripping the Application's `finalizers` to `[]` can get
   silently reverted moments later, and a `kubectl delete --wait=true` right after times out
   waiting on a finalizer that's already back. Deleting the `ApplicationSet` first is safe here
   specifically because `automated` was already confirmed off, so there's no prune-on-delete
   cascade risk.
4. Only now strip the orphaned Application's `finalizers` to `[]` and delete it directly.

For a **standalone** `Application` (Case B), there's no `ApplicationSet` in the picture, so step 3
above doesn't apply: remove `syncPolicy.automated` directly on the `Application`, confirm it took,
then strip `finalizers` to `[]` and delete it directly.

**Decommissioning an app *after* it's already been migrated (agent-routed) is different, and
force-stripping the finalizer here is actively harmful.** Once Step 4 above is done, the
`Application`'s destination is real and reachable (via the agent), unlike the fake pull-model
destination — so ArgoCD's own finalize-then-remove-finalizer flow can actually complete on its own,
and letting it complete normally is how a delete is *supposed* to relay to the spoke's mirrored
copy through the agent. If you force-strip the finalizer to `[]` before the delete completes (the
technique used above for a stuck pull-model app with an unreachable destination), the hub object
disappears immediately but the relay to the agent never gets a chance to happen at all.

**Even with a normal, non-force-stripped delete, still explicitly verify the spoke's copy is
actually gone — do not assume the relay completed just because the hub delete did.** Confirmed
live: for an app that existed before argoCDAgent was ever enabled on its cluster (i.e. any
migrated pull-model app, since migration is by definition retroactive), the delete-relay to the
agent is not always reliable, with or without force-stripping — in one observed case the relay
was rejected outright with `source UID annotation is not found for app: <name>` in the agent's
logs, leaving the app stuck `Terminating` on the hub until its finalizer was cleared manually; in
another, a clean `--wait=true` delete completed fully on the hub with no error, yet the spoke's
mirrored `Application` (and its real workload) was silently left behind regardless. Both cases were
resolved the same way: check for a leftover copy directly on the spoke and delete it there too if
one exists:

```bash
kubectl --kubeconfig $SPOKE_KUBECONFIG get applications.argoproj.io $APP_NAME -n $ARGOCD_NS
# if found (with no corresponding object left on the hub), it's an orphan -- delete it directly:
kubectl --kubeconfig $SPOKE_KUBECONFIG delete applications.argoproj.io $APP_NAME -n $ARGOCD_NS --wait=true
```

An orphaned spoke copy is not just inert leftover clutter — left alone, it keeps actively
reconciling its resources, and will fight any later, unrelated app that happens to reuse the same
destination namespace and resource names, with the two copies repeatedly overwriting each other's
`argocd.argoproj.io/tracking-id` and the affected app flapping `Synced`/`OutOfSync` indefinitely.
Always verify and clean up the spoke side, every time, for every migrated app you decommission.

## Final verification checklist

```bash
# 1. Basic pull model is disabled hub-wide and the sweep ran
kubectl get configmap multicluster-integrations-config -n <namespace> -o jsonpath='{.data.config\.yaml}'
#    ^ should show pullModel.basic.disabled: true

# 2. Same Application, same uid as before you started
kubectl get applications.argoproj.io $APP_NAME -n $ARGOCD_NS -o jsonpath='{.metadata.uid}'
#    ^ compare against the uid you recorded before Step 1 -- must be identical

# 3. It's Synced via the agent on both hub and spoke
kubectl get applications.argoproj.io $APP_NAME -n $ARGOCD_NS
kubectl --kubeconfig $SPOKE_KUBECONFIG get applications.argoproj.io $APP_NAME -n $ARGOCD_NS

# 3b. It's Synced via the agent for real, not just coincidentally still Synced from before
# (see Step 5 -- this is the check that actually distinguishes the two)
kubectl --kubeconfig $SPOKE_KUBECONFIG get applications.argoproj.io $APP_NAME -n $ARGOCD_NS -o json | jq '.spec.destination'
#    ^ must show a name-based destination (e.g. "in-cluster") -- NOT server: https://kubernetes.default.svc

# 4. The real workload was never touched
kubectl --kubeconfig $SPOKE_KUBECONFIG get pods -n <dest-namespace> -o jsonpath='{.items[0].metadata.uid}'

# 5. No duplicate ArgoCD instances fighting
kubectl --kubeconfig $SPOKE_KUBECONFIG get argocd -n $ARGOCD_NS
#    ^ should show exactly ONE (the addon-managed acm-openshift-gitops)
kubectl --kubeconfig $SPOKE_KUBECONFIG get pods -n $ARGOCD_NS
#    ^ no CrashLoopBackOff / restart-looping pods

# 6. argocd-agent is healthy and has been stable for a while
kubectl --kubeconfig $SPOKE_KUBECONFIG get pods -n $ARGOCD_NS -l app.kubernetes.io/part-of=argocd-agent

# 7. This app's own ManifestWork is frozen, not enforcing
kubectl get manifestwork -n $CLUSTER_NAME -o json | python3 -c "
import json, sys
d = json.load(sys.stdin)
for i in d['items']:
    ann = i.get('metadata', {}).get('annotations', {}) or {}
    if ann.get('apps.open-cluster-management.io/hub-application-name') != '$APP_NAME':
        continue
    for mc in i['spec'].get('manifestConfigs', []):
        ri = mc.get('resourceIdentifier', {})
        if ri.get('group') == 'argoproj.io' and ri.get('resource') == 'applications':
            print(i['metadata']['name'], (mc.get('updateStrategy') or {}).get('type'))
"
#    ^ should show "ReadOnly" -- never "ServerSideApply"/"Update" for this app again -- match by
#    resourceIdentifier, not position: a ManifestWork can carry other manifestConfigs entries
#    (e.g. a Namespace) ahead of the applications one
```

## Re-enabling the basic pull model

If you need to bring the basic pull model back (e.g. rolling back a migration you haven't
completed yet), set `pullModel.basic.disabled: false` in the ConfigMap and restart the pod again.
Reconciliation resumes for any app still carrying the pull label/annotations. This does **not**
automatically revert `ManifestWork`s that were swept to `ReadOnly` back to their prior update
strategy — if you want a specific app to go back to being pull-model-enforced, patch its
`ManifestWork`'s `updateStrategy` back explicitly.
