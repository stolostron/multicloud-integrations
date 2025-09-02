# Using GitOpsCluster resource
The following examples will allow you to import all OCM `ManagedCluster` resources 

## Requires
* Open Cluster Management hub cluster.
* Install the Argo CD.
* Register/Import/Join clusters.

## Examples

### Basic GitOpsCluster (`gitopscluster.yaml`)
Basic configuration without ArgoCD Agent.

### GitOpsCluster with ArgoCD Agent (`gitopscluster-with-agent.yaml`)
Configuration with ArgoCD Agent enabled for secure communication. When `argoCDAgent.enabled: true`, the controller will automatically create the necessary TLS certificates:
- `argocd-agent-principal-tls` - Certificate for external ArgoCD server communication
- `argocd-agent-resource-proxy-tls` - Certificate for internal resource proxy communication

**Additional Requirements for Agent mode:**
* ArgoCD Agent CA secret `argocd-agent-ca` must exist in the target namespace

## Configure
* Apply the basic example:
   ```shell
   kubectl apply -f gitopscluster.yaml
   kubectl apply -f placement.yaml
   kubectl apply -f managedclustersetbinding.yaml
   ```
* Or apply the example with ArgoCD Agent:
   ```shell
   kubectl apply -f gitopscluster-with-agent.yaml
   kubectl apply -f placement.yaml
   kubectl apply -f managedclustersetbinding.yaml
   ```
* All `ManagedCluster` will be imported into the Argo CD.
* Check the Argo CD Configuration tab, to see the list of clusters

## Notes
* The `ManagedCluster`'s name is used as the Argo CD server name
* The `ManagedCluster`'s API is used as the Argo CD cluster URL
