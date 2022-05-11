## Overview 

------

This repository hosts a controller that imports ManagedCluster kind resources into Argo CD (OpenShift GitOps), based on Placement resource.

## Quick start

------

1. Connect to OpenShift
2. Run:
   ```shell
   oc apply -f deploy/crds
   
   #TBD oc apply -f deploy/controller
   ```

## Usage
1. Create a placement resource
2. Create a GitOpsCluster resource kind that points to placement and an Argo CD namespace
3. Check Argo CD >> Configuration >> Clusters to make sure you see the imported ManagedClusters


### Troubleshooting
1. Check the logs for the multicloud-integration pod. 
2. Make sure the Placement resource generated a PlacementDecision resource kind and that the status has a decision list.

## Community, discussion, contribution, and support

Check the [CONTRIBUTING Doc](CONTRIBUTING.md) for how to contribute to the repository.

------

## Getting started

## Security response

Check the [Security Doc](SECURITY.md) if you find a security issue.

## References

### Multicloud-operators repositories

- [multicloud-operators-application](https://github.com/open-cluster-management/multicloud-operators-application)
- [multicloud-operators-channel](https://github.com/open-cluster-management/multicloud-operators-channel)
- [multicloud-operators-deployable](https://github.com/open-cluster-management/multicloud-operators-deployable)
- [multicloud-operators-placementrule](https://github.com/open-cluster-management/multicloud-operators-placementrule)
- [multicloud-operators-subscription](https://github.com/open-cluster-management/multicloud-operators-subscription)
- [multicloud-operators-subscription-release](https://github.com/open-cluster-management/multicloud-operators-subscription-release)
