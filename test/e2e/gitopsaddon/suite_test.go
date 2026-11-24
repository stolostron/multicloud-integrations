//go:build e2e

package gitopsaddon_e2e

import (
	"fmt"
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = BeforeSuite(func() {
	// The suite installs the chart's registry.redhat.io images; without credentials
	// there is nothing meaningful it can verify.
	if os.Getenv("RH_PULL_SECRET_FILE") == "" {
		Skip("RH_PULL_SECRET_FILE is not set: this suite installs operator and ArgoCD " +
			"images from registry.redhat.io, which need credentials. Point it at a docker " +
			"config JSON containing registry.redhat.io credentials to run the suite.")
	}
})

func TestGitOpsAddonE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	fmt.Fprintf(GinkgoWriter, "Starting GitOps Addon E2E test suite\n")
	RunSpecs(t, "GitOps Addon E2E Suite")
}

const (
	hubContext   = "kind-hub"
	spokeContext = "kind-cluster1"
	spokeName    = "cluster1"

	argoCDNamespace       = "openshift-gitops"
	addonAgentNamespace   = "open-cluster-management-agent-addon"
	controllerNamespace   = "open-cluster-management"
	operatorNamespace     = "openshift-gitops-operator"
	addonName             = "gitops-addon"
	gitopsClusterName     = "gitops-cluster"
	placementName         = "all-openshift-clusters"
	managedClusterSetName = "global"
	localClusterName      = "local-cluster"
)
