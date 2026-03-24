//go:build e2e

package gitopsaddon_e2e

import (
	"fmt"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestGitOpsAddonE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	fmt.Fprintf(GinkgoWriter, "Starting GitOps Addon E2E test suite\n")
	RunSpecs(t, "GitOps Addon E2E Suite")
}

const (
	hubContext   = "kind-hub"
	spokeContext = "kind-cluster1"
	spokeName    = "cluster1"

	argoCDNamespace         = "openshift-gitops"
	addonAgentNamespace     = "open-cluster-management-agent-addon"
	controllerNamespace     = "open-cluster-management"
	operatorNamespace       = "openshift-gitops-operator"
	addonName               = "gitops-addon"
	gitopsClusterName       = "gitops-cluster"
	placementName           = "all-openshift-clusters"
	managedClusterSetName   = "global"
	localClusterName        = "local-cluster"
)
