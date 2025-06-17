# ACM ArgoCD Pull Model with Maestro integration

By integrating with [Maestro](https://github.com/stolostron/maestro-addon), the ArgoCD Pull Model is enhanced to address the following challenges in the large-scale multi-cluster environment

- Too many manifestwork resources are created per cluster namespace on the hub cluster. 

In large scale multi-cluster env such as 100 applicationSet on 3k managed clusters,  there will be 300K manifestwork resources deployed on the hub cluster in total. Such a workload has a big impact on the hub cluster performance.

- Performance impact on ACM Search component

The existing ArgoCD pull model fetches the ArgoCD application status on all managed clusters through Search API server. In a large scale multi-cluster case, it requires to loop through a large number of ArgoCD application records (300K) via Search API server per loop period. Since Search doesn’t support record listing pagination yet, this frequent large query would have a severe performance impact on the Search API server.

- Not good UI support on ArgoCD native console

The existing ArgoCD pull model generates the ACM MultiClusterApplicationSetReport kind resource for only storing the high level status including the application health status and sync status on all managed clusters with error messages. It is only visible in the ACM application console but not in the native ArgoCD console.  

Also the detailed application status on all managed clusters is not stored anywhere on the hub cluster. End users have to get such information by accessing each managed cluster

## Install the new ArgoCD pull model with Maestro integration

### PreReq

#### 1. Install Maestro addon server on the hub cluster

```
% git clone https://github.com/stolostron/maestro-addon
% cd maestro-addon
% helm install maestro-addon ./charts/maestro-addon
```

To verify the maestro components are running on the hub cluster
```
% oc get pods -n maestro
NAME                                     READY   STATUS    RESTARTS   AGE
maestro-7d8fc757d5-l29p9                 1/1     Running   0          84m
maestro-addon-manager-667cdb7c4c-lckj9   1/1     Running   0          84m
maestro-db-c47dd8fd5-59xhc               1/1     Running   0          84m
```

#### 2. Install Maestro addon agent on each managed cluster

```
% oc -n <Cluster Namespace> apply -f - << EOF
apiVersion: addon.open-cluster-management.io/v1alpha1
kind: ManagedClusterAddOn
metadata:
  name: maestro-addon
spec:
  installNamespace: open-cluster-management-agent
EOF
```

To verify the maestro addon agent is running on each managed cluster, go to the managed cluster
```
% oc get pods -n open-cluster-management-agent |grep maestro
maestro-addon-7fb469f897-9zvzk      1/1     Running   0          80m
```

#### 3. install openshift gitops instance on each managed cluster

```
% oc -n <Cluster Namespace> apply -f - << EOF
apiVersion: addon.open-cluster-management.io/v1alpha1
kind: ManagedClusterAddOn
metadata:
  name: gitops-addon
spec:
  installNamespace: open-cluster-management-agent-addon
EOF
```

To verify the openshift gitops instance is running on each managed cluster, go to the managed cluster
```
% oc get pods -n openshift-gitops      
NAME                                           READY   STATUS    RESTARTS   AGE
openshift-gitops-application-controller-0      1/1     Running   0          17h
openshift-gitops-redis-6fcf89754-ztdzx         1/1     Running   0          17h
openshift-gitops-repo-server-cf5974d4d-zpsb8   1/1     Running   0          17h
```

### Disable the current ArgoCD pull model

Note: The default ACM MCH namespace `open-cluster-cluster` is applied in the following CLI. Specify the actual MCH namespace if it changed during ACM installation

#### 1. pause mch
```
% oc annotate mch -n open-cluster-management multiclusterhub mch-pause=true --overwrite=true
```
#### 2. Disable the current ArgoCD pull model deployment 
```
% oc scale deployment -n open-cluster-management multicluster-integrations --replicas=0 
```

#### 3. Verify the current ArgoCD pull model pod is disabled 
```
% oc get pods -n open-cluster-management |grep multicluster-integration
```

### Install the new ArgoCD pull model with Maestro integration
```
% git clone https://github.com/stolostron/multicloud-integrations
% cd multicloud-integrations
% helm install multicluster-maestro-integrations ./maestroapplication/charts/multicluster-maestro-integrations --namespace open-cluster-management
```

To verify the new ArgoCD pull model is running on the hub cluster
```
% oc get pods -n open-cluster-management |grep multicluster-maestro-integration
multicluster-maestro-integrations-5fc5c8874b-jv9v9                2/2     Running   0                89m
```

## Deploy ArgoCD applicationSet by the new ArgoCD pull model with Maestro integration

### Deploy a bgdk ArgoCD applicationSet to all non local managed clusters
```
% oc apply -f maestroapplication/examples/gitopscluster-all-clusters.yaml
% oc apply -f maestroapplication/examples/pull-model-appset-bgdk.yaml
```
### Verify the bgdk ArgoCD application is deployed on each managed cluster
go to each managed cluster;
```
% oc get apps -n openshift-gitops 
NAME                    SYNC STATUS   HEALTH STATUS
xj-managed-1-bgdk-app   Synced        Healthy
```

### Verify the bgdk ArgoCD application status per managed cluster is synced back to hub cluster 
go to hub cluster
```
% oc get apps -n openshift-gitops 
NAME                    SYNC STATUS   HEALTH STATUS
xj-managed-1-bgdk-app   Synced        Healthy
xj-managed-2-bgdk-app   Synced        Healthy
```

## Uninstall the new ArgoCD pull model with Maestro integration

### Uninstall the new ArgoCD pull model helm chart
```
% helm uninstall multicluster-maestro-integrations --namespace open-cluster-management
% oc get pods -n open-cluster-management |grep multicluster-maestro-integration
```

### Enable the current ArgoCD pull model
#### Option 1: enable MCH. the current ArgoCD pull model deployment will be scaled up to 1 automatically
```
% oc annotate mch -n open-cluster-management multiclusterhub mch-pause=true --overwrite=true
```
#### Option 2: keep disabling MCH, manually scale the current ArgoCD pull model deployment up to 1
```
% oc scale deployment -n open-cluster-management multicluster-integrations --replicas=1
```

