/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package gitopsaddon

import (
	"context"
	"embed"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

//nolint:all
//go:embed charts/openshift-gitops-operator/**
//go:embed charts/openshift-gitops-dependency/**
//go:embed charts/dep-crds/**
//go:embed charts/argocd-agent/**
var ChartFS embed.FS

// GitopsAddonReconciler reconciles a openshift gitops operator
type GitopsAddonReconciler struct {
	client.Client
	Scheme                   *runtime.Scheme
	Config                   *rest.Config
	Interval                 int
	GitopsOperatorImage      string
	GitopsOperatorNS         string
	GitopsImage              string
	GitopsNS                 string
	RedisImage               string
	ReconcileScope           string
	HTTP_PROXY               string
	HTTPS_PROXY              string
	NO_PROXY                 string
	Uninstall                string
	ArgoCDAgentEnabled       string
	ArgoCDAgentImage         string
	ArgoCDAgentServerAddress string
	ArgoCDAgentServerPort    string
	ArgoCDAgentMode          string
}

func SetupWithManager(mgr manager.Manager, interval int, gitopsOperatorImage, gitopsOperatorNS,
	gitopsImage, gitopsNS, redisImage, reconcileScope,
	HTTP_PROXY, HTTPS_PROXY, NO_PROXY, uninstall, argoCDAgentEnabled, argoCDAgentImage, argoCDAgentServerAddress, argoCDAgentServerPort, argoCDAgentMode string) error {
	dsRS := &GitopsAddonReconciler{
		Client:                   mgr.GetClient(),
		Scheme:                   mgr.GetScheme(),
		Config:                   mgr.GetConfig(),
		Interval:                 interval,
		GitopsOperatorImage:      gitopsOperatorImage,
		GitopsOperatorNS:         gitopsOperatorNS,
		GitopsImage:              gitopsImage,
		GitopsNS:                 gitopsNS,
		RedisImage:               redisImage,
		ReconcileScope:           reconcileScope,
		HTTP_PROXY:               HTTP_PROXY,
		HTTPS_PROXY:              HTTPS_PROXY,
		NO_PROXY:                 NO_PROXY,
		Uninstall:                uninstall,
		ArgoCDAgentEnabled:       argoCDAgentEnabled,
		ArgoCDAgentImage:         argoCDAgentImage,
		ArgoCDAgentServerAddress: argoCDAgentServerAddress,
		ArgoCDAgentServerPort:    argoCDAgentServerPort,
		ArgoCDAgentMode:          argoCDAgentMode,
	}

	// Setup the secret controller to watch and copy ArgoCD agent client cert secrets
	if err := SetupSecretControllerWithManager(mgr); err != nil {
		return err
	}

	return mgr.Add(dsRS)
}

func (r *GitopsAddonReconciler) Start(ctx context.Context) error {
	go wait.Until(func() {
		r.houseKeeping()
	}, time.Duration(r.Interval)*time.Second, ctx.Done())

	return nil
}

func (r *GitopsAddonReconciler) houseKeeping() {
	if r.Uninstall == "true" {
		r.performUninstallOperations()
	} else {
		r.installOrUpdateOpenshiftGitops()
	}
}
