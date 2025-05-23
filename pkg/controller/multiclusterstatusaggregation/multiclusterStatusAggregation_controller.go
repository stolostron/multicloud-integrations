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

package multiclusterstatusaggregation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ghodss/yaml"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog"
	workv1 "open-cluster-management.io/api/work/v1"
	appsetreportV1alpha1 "open-cluster-management.io/multicloud-integrations/pkg/apis/appsetreport/v1alpha1"
	propagation "open-cluster-management.io/multicloud-integrations/propagation-controller/application"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// ReconcilePullModelAggregation reconciles a MulticlusterApplicationSet object.
type ReconcilePullModelAggregation struct {
	client.Client
	Interval    int
	ResourceDir string
}

// Keys for the appSetClusterStatusMap
type AppSet struct {
	appset types.NamespacedName
}

type Cluster struct {
	clusterName string
}

// Value for the appSetClusterStatusMap
type OverallStatus struct {
	HealthStatus            string
	SyncStatus              string
	OperationStateStartedAt string
	OperationStatePhase     string
	SyncRevision            string
	App                     string
}

// AppSetClusterResourceSorter sorts appsetreport resources by name
type AppSetClusterResourceSorter []appsetreportV1alpha1.ResourceRef

func (a AppSetClusterResourceSorter) Len() int      { return len(a) }
func (a AppSetClusterResourceSorter) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a AppSetClusterResourceSorter) Less(i, j int) bool {
	if a[i].Name != a[j].Name {
		return a[i].Name < a[j].Name
	}

	return a[i].Kind < a[j].Kind
}

// AppSetClusterConditionsSorter sorts appsetreport clusterconditions by cluster
type AppSetClusterConditionsSorter []appsetreportV1alpha1.ClusterCondition

func (a AppSetClusterConditionsSorter) Len() int           { return len(a) }
func (a AppSetClusterConditionsSorter) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a AppSetClusterConditionsSorter) Less(i, j int) bool { return a[i].Cluster < a[j].Cluster }

func Add(mgr manager.Manager, interval int, resourceDir string) error {
	dsRS := &ReconcilePullModelAggregation{
		Client:      mgr.GetClient(),
		Interval:    interval,
		ResourceDir: resourceDir,
	}

	return mgr.Add(dsRS)
}

func (r *ReconcilePullModelAggregation) Start(ctx context.Context) error {
	go wait.Until(func() {
		r.houseKeeping()
	}, time.Duration(r.Interval)*time.Second, ctx.Done())

	return nil
}

func (r *ReconcilePullModelAggregation) houseKeeping() {
	klog.Info("Start aggregating all ArgoCD application manifestworks per appset...")

	// create or update all MulticlusterApplicationSetReport objects in the appset NS
	err := r.generateAggregation()
	if err != nil {
		klog.Warning("error occurred while generating ArgoCD application aggregation, err: ", err)
	}

	klog.Info("Finished aggregating all ArgoCD application manifestworks.")

	klog.Info("Start cleaning all MultiClusterApplicationSet reports.")

	err = r.cleanupReports()
	if err != nil {
		klog.Warning("error occurred while cleaning MultiClusterApplicationSet reports, err: ", err)
	}

	klog.Info("Finished cleaning all MultiClusterApplicationSet reports.")
}

func (r *ReconcilePullModelAggregation) generateAggregation() error {
	klog.Info(GetMemUsage("Prepare to aggregate manifestwork statuses"))

	var (
		limit         int64 = 500
		continueToken string
	)

	appSetRequirement, err := labels.NewRequirement(propagation.LabelKeyAppSet, selection.Exists, []string{})
	if err != nil {
		klog.Errorf("bad requirement: %v", err)
	}

	appSetSelector := labels.NewSelector()
	appSetSelector = appSetSelector.Add(*appSetRequirement)

	// create a map for containing overallstatus per cluster, appset. 2 Keys Appset NamespacedName, Cluster Name
	appSetClusterStatusMap := make(map[AppSet]map[Cluster]OverallStatus)

	for {
		appSetClusterList := &workv1.ManifestWorkList{}

		listopts := &client.ListOptions{
			LabelSelector: appSetSelector,
			Limit:         limit,
			Continue:      continueToken,
		}

		err = r.List(context.TODO(), appSetClusterList, listopts) // list upto limit # of manifestworks
		if err != nil {
			klog.Errorf("Failed to list Argo Application manifestWorks, err: %v", err)

			return err
		}

		// above fetches manifestworks generated by the propagation controller.
		appSetClusterCount := len(appSetClusterList.Items)
		if appSetClusterCount == 0 {
			klog.Infof("No aggregration per cluster with labels %v found", appSetSelector)
		}

		klog.Infof("cluster aggregation Count: %v", appSetClusterCount)

		klog.Info(GetMemUsage("Initialize AppSet Map."))

		for _, manifestWork := range appSetClusterList.Items {
			appsetNs, appsetName := ParseNamespacedName(manifestWork.Annotations[propagation.AnnotationKeyAppSet])

			if appsetNs == "" || appsetName == "" {
				klog.Warningf("Appset namespace: %v , Appset name: %v", appsetNs, appsetName)
			}

			// Need to allocate a map of clusters for each appset only once.
			appSetKey := AppSet{types.NamespacedName{Namespace: appsetNs, Name: appsetName}}
			if _, ok := appSetClusterStatusMap[appSetKey]; !ok {
				appSetClusterStatusMap[appSetKey] = make(map[Cluster]OverallStatus)
			}
		}

		for _, manifestWork := range appSetClusterList.Items {
			healthStatus, syncStatus := "Unknown", "Unknown"
			operationStatePhase := ""
			operationStateStartedAt := ""
			syncRevision := ""

			appsetNs, appsetName := ParseNamespacedName(manifestWork.Annotations[propagation.AnnotationKeyAppSet])

			if appsetNs == "" && appsetName == "" {
				klog.Warningf("Appset namespace: %v , Appset name: %v", appsetNs, appsetName)
			}

			appSetKey := AppSet{types.NamespacedName{Namespace: appsetNs, Name: appsetName}}
			clusterKey := Cluster{manifestWork.Namespace}

			for _, manifest := range manifestWork.Status.ResourceStatus.Manifests {
				for _, statuses := range manifest.StatusFeedbacks.Values {
					if statuses.Name == "healthStatus" {
						healthStatus = *statuses.Value.String
					} else if statuses.Name == "syncStatus" {
						syncStatus = *statuses.Value.String
					} else if statuses.Name == "operationStatePhase" {
						operationStatePhase = *statuses.Value.String
					} else if statuses.Name == "operationStateStartedAt" {
						operationStateStartedAt = *statuses.Value.String
					} else if statuses.Name == "syncRevision" {
						syncRevision = *statuses.Value.String
					}
				}
			}

			appSetClusterStatusMap[appSetKey][clusterKey] = OverallStatus{
				HealthStatus:            healthStatus,
				SyncStatus:              syncStatus,
				OperationStateStartedAt: operationStateStartedAt,
				OperationStatePhase:     operationStatePhase,
				SyncRevision:            syncRevision,
				App: appsetNs + "/" + manifestWork.Annotations[propagation.AnnotationKeyHubApplicationName] +
					"/" + manifestWork.Namespace + "/" + manifestWork.Name,
			}
		}

		klog.V(1).Infof("AppSet Map: %v", appSetClusterStatusMap)

		if continueToken = appSetClusterList.GetContinue(); continueToken == "" {
			break
		}
	}

	klog.Info(GetMemUsage("AppSet Map generated."))
	klog.V(1).Infof("Final AppSet Map: %v", appSetClusterStatusMap)

	// generate report from both propagation and resource sync controllers.
	r.generateAppSetReport(appSetClusterStatusMap)

	runtime.GC()

	klog.Info(GetMemUsage("AppSet Report refreshed."))

	return nil
}

func (r *ReconcilePullModelAggregation) generateAppSetReport(appSetClusterStatusMap map[AppSet]map[Cluster]OverallStatus) {
	for appset := range appSetClusterStatusMap {
		appsetNs := appset.appset.Namespace
		appsetName := appset.appset.Name

		// create/update the applicationset report for this appset
		klog.V(1).Infof("Updating AppSetReport for appset: %v", appset)

		//    1. create a new applicationseet report and assign it to a variable
		existingAppsetReport := &appsetreportV1alpha1.MulticlusterApplicationSetReport{
			TypeMeta: metav1.TypeMeta{
				Kind:       "MulticlusterApplicationSetReport",
				APIVersion: "apps.open-cluster-management.io/v1alpha1",
			},
		}

		//    2. fetch the existing appset report
		if err := r.Get(context.TODO(), appset.appset, existingAppsetReport); err != nil {
			if errors.IsNotFound(err) {
				// Create report for the first time
				klog.V(1).Infof("Creating AppSetReport for first time: %v", appset)

				existingAppsetReport.ObjectMeta = metav1.ObjectMeta{
					Name:      appsetName,
					Namespace: appsetNs,
					Labels: map[string]string{
						propagation.AnnotationKeyAppSet: fmt.Sprintf("%.63s", appsetNs+"."+appsetName),
					},
				}

				if err := r.Create(context.TODO(), existingAppsetReport); err != nil {
					klog.Errorf("Failed to create the appsetReport, err: %v", err)

					continue
				}
			}
		}

		// load yaml from Resource Sync Controller
		var (
			newAppSetReport *appsetreportV1alpha1.MulticlusterApplicationSetReport
			appSetCRD       appsetreportV1alpha1.AppConditions
		)

		loadYAML := true
		reportName := filepath.Join(r.ResourceDir, appsetNs+"_"+appsetName+".yaml")
		testappSetCRD, err := loadAppSetCRD(reportName)

		if err != nil {
			klog.Warning("Failed to load appSet CRD err: ", err)

			loadYAML = false
		}

		if loadYAML {
			appSetCRD.ClusterConditions = testappSetCRD.Statuses.ClusterConditions
			appSetCRD.Resources = testappSetCRD.Statuses.Resources
			appSetCRD.Summary = testappSetCRD.Statuses.Summary

			klog.V(1).Info("Map: ", appSetClusterStatusMap)
			klog.V(1).Info("Appset: ", appset)
			klog.V(1).Info("Clusterconditions: ", appSetCRD.ClusterConditions)
			klog.V(1).Info("Resources: ", appSetCRD.Resources)
			klog.V(1).Info("Summary: ", appSetCRD.Summary)
			newSummary, appSetClusterConditions := r.generateSummary(appSetClusterStatusMap, appset, appSetCRD.ClusterConditions)
			newAppSetReport = r.newAppSetReport(appsetNs, appsetName, appSetCRD.Resources, appSetClusterConditions, newSummary)
		} else {
			newSummary, appSetClusterConditions := r.generateSummary(appSetClusterStatusMap, appset, []appsetreportV1alpha1.ClusterCondition{})
			newAppSetReport = r.newAppSetReport(appsetNs, appsetName, []appsetreportV1alpha1.ResourceRef{}, appSetClusterConditions, newSummary)
		}

		klog.Info(GetMemUsage("memory usage when updating MulticlusterApplicationSetReport."))

		//    3. compare the existing report to the new one
		if !r.compareAppSetReports(existingAppsetReport, newAppSetReport) {
			//    4. update the appset report only if there are changes
			existingAppsetReport.SetName(newAppSetReport.GetName())
			existingAppsetReport.SetNamespace(newAppSetReport.GetNamespace())
			existingAppsetReport.SetLabels(newAppSetReport.GetLabels())

			existingAppsetReport.Statuses.Resources = newAppSetReport.Statuses.Resources

			existingAppsetReport.Statuses.ClusterConditions = newAppSetReport.Statuses.ClusterConditions

			existingAppsetReport.Statuses.Summary = newAppSetReport.Statuses.Summary

			if err := r.Update(context.TODO(), existingAppsetReport); err != nil {
				klog.Errorf("Failed to update MulticlusterApplicationSetReport err: %v", err)

				continue
			}

			klog.V(1).Infof("MulticlusterApplicationSetReport updated, %v/%v", existingAppsetReport.GetNamespace(), existingAppsetReport.GetName())
		}
	}
}

func (r *ReconcilePullModelAggregation) generateSummary(appSetClusterStatusMap map[AppSet]map[Cluster]OverallStatus,
	appset AppSet, appSetCRDConditions []appsetreportV1alpha1.ClusterCondition) (appsetreportV1alpha1.ReportSummary, []appsetreportV1alpha1.ClusterCondition) {
	var (
		synced, notSynced, healthy, notHealthy, inProgress, clusters int
	)

	appSetClusterConditionsMap := make(map[string]appsetreportV1alpha1.ClusterCondition)

	klog.V(1).Info("Starting to generate appset summary for ", appset)

	for cluster := range appSetClusterStatusMap[appset] {
		klog.V(1).Info("Cluster: ", cluster)
		// generate the cluster condition list per this appset
		appSetClusterConditionsMap[cluster.clusterName] = appsetreportV1alpha1.ClusterCondition{
			Cluster:                 cluster.clusterName,
			SyncStatus:              appSetClusterStatusMap[appset][cluster].SyncStatus,
			HealthStatus:            appSetClusterStatusMap[appset][cluster].HealthStatus,
			OperationStateStartedAt: appSetClusterStatusMap[appset][cluster].OperationStateStartedAt,
			OperationStatePhase:     appSetClusterStatusMap[appset][cluster].OperationStatePhase,
			SyncRevision:            appSetClusterStatusMap[appset][cluster].SyncRevision,
			App:                     appSetClusterStatusMap[appset][cluster].App,
		}

		// Calculate the summary while we're here.
		clusters++

		switch appSetClusterStatusMap[appset][cluster].HealthStatus {
		case "Healthy":
			healthy++
		case "Progressing":
			inProgress++
			notHealthy++
		default:
			notHealthy++
		}

		switch appSetClusterStatusMap[appset][cluster].SyncStatus {
		case "Synced":
			synced++
		default:
			notSynced++
		}
	}

	summary := appsetreportV1alpha1.ReportSummary{
		Synced:     strconv.Itoa(synced),
		NotSynced:  strconv.Itoa(notSynced),
		Healthy:    strconv.Itoa(healthy),
		NotHealthy: strconv.Itoa(notHealthy),
		InProgress: strconv.Itoa(inProgress),
		Clusters:   strconv.Itoa(clusters),
	}
	if clusters != (synced+notSynced) || clusters != (healthy+notHealthy) {
		klog.Warningf("Total number of clusters does not add up, %v", summary)
	}

	// Combine cluster conditions from manifestwork and yaml
	for _, yamlConditions := range appSetCRDConditions {
		klog.V(1).Info("AppSetCRD conditions item: ", yamlConditions)

		if cCond, ok := appSetClusterConditionsMap[yamlConditions.Cluster]; ok {
			cCond.Conditions = yamlConditions.Conditions
			appSetClusterConditionsMap[yamlConditions.Cluster] = cCond
		} else {
			appSetClusterConditionsMap[yamlConditions.Cluster] = yamlConditions
		}
	}

	res := make([]appsetreportV1alpha1.ClusterCondition, 0, len(appSetClusterConditionsMap))

	for _, cCondition := range appSetClusterConditionsMap {
		res = append(res, cCondition)
	}

	// Sort after list has been created so it's in a natural order
	sort.Sort(AppSetClusterConditionsSorter(res))
	klog.Info("Sorted cluster conditions list", res)

	return summary, res
}

func (r *ReconcilePullModelAggregation) newAppSetReport(appsetNs, appsetName string, appsetResources []appsetreportV1alpha1.ResourceRef,
	appsetClusterConditions []appsetreportV1alpha1.ClusterCondition,
	appsetSummary appsetreportV1alpha1.ReportSummary) *appsetreportV1alpha1.MulticlusterApplicationSetReport {
	newAppSetReport := &appsetreportV1alpha1.MulticlusterApplicationSetReport{
		TypeMeta: metav1.TypeMeta{
			Kind:       "MulticlusterApplicationSetReport",
			APIVersion: "apps.open-cluster-management.io/v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      appsetName,
			Namespace: appsetNs,
			Labels: map[string]string{
				propagation.AnnotationKeyAppSet: fmt.Sprintf("%.63s", appsetNs+"."+appsetName),
			},
		},
		Statuses: appsetreportV1alpha1.AppConditions{
			Resources:         appsetResources,
			ClusterConditions: appsetClusterConditions,
			Summary:           appsetSummary,
		},
	}

	return newAppSetReport
}

func (r *ReconcilePullModelAggregation) compareAppSetReports(report1, report2 *appsetreportV1alpha1.MulticlusterApplicationSetReport) bool {
	isSame := true

	if !equality.Semantic.DeepEqual(report1.GetLabels(), report2.GetLabels()) {
		klog.Info("Labels not same")

		isSame = false
	}

	if !equality.Semantic.DeepEqual(report1.Statuses.Summary, report2.Statuses.Summary) {
		klog.Info("Summary not same")

		isSame = false
	}

	if len(report1.Statuses.Resources) != len(report2.Statuses.Resources) {
		klog.Infof("Resources length not same, report1: %v report2: %v", report1.Statuses.Resources, report2.Statuses.Resources)

		isSame = false
	} else {
		// sort new appset resources by name then kind
		sort.Sort(AppSetClusterResourceSorter(report1.Statuses.Resources))
		sort.Sort(AppSetClusterResourceSorter(report2.Statuses.Resources))

		// check equality of resources
		if !equality.Semantic.DeepEqual(report1.Statuses.Resources, report2.Statuses.Resources) {
			klog.Infof("Resources not same, report1: %v report2: %v", report1.Statuses.Resources, report2.Statuses.Resources)

			isSame = false
		}
	}

	if len(report1.Statuses.ClusterConditions) != len(report2.Statuses.ClusterConditions) {
		klog.Infof("ClusterConditions length not same, report1: %v report2: %v", report1.Statuses.ClusterConditions, report2.Statuses.ClusterConditions)

		isSame = false
	} else {
		// sort existing appset clusterConditions by name
		sort.Sort(AppSetClusterConditionsSorter(report1.Statuses.ClusterConditions))
		sort.Sort(AppSetClusterConditionsSorter(report2.Statuses.ClusterConditions))

		// check equality of clusterConditions
		if !equality.Semantic.DeepEqual(report1.Statuses.ClusterConditions, report2.Statuses.ClusterConditions) {
			klog.Infof("ClusterConditions not same, report1: %v report2: %v", report1.Statuses.ClusterConditions, report2.Statuses.ClusterConditions)

			isSame = false
		}
	}

	return isSame
}

func (r *ReconcilePullModelAggregation) cleanupReports() error {
	files, err := os.ReadDir(r.ResourceDir)
	if err != nil {
		return err
	}

	var missingAppset []types.NamespacedName

	for _, file := range files {
		appsetName := strings.TrimRight(file.Name(), ".yaml")
		names := strings.Split(appsetName, "_")
		klog.V(1).Info("Checking file ", appsetName)

		if len(names) > 1 {
			appsetNsN := types.NamespacedName{Namespace: names[0], Name: names[1]}
			klog.V(1).Info("Check if corresponding appset exists for YAML, ", appsetNsN)

			existingAppset := &unstructured.Unstructured{}
			existingAppset.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "argoproj.io",
				Version: "v1alpha1",
				Kind:    "ApplicationSet",
			})

			if err := r.Get(context.TODO(), appsetNsN, existingAppset); err != nil {
				if errors.IsNotFound(err) {
					klog.Info("Appset not found for YAML ", appsetName)

					missingAppset = append(missingAppset, appsetNsN)
				} else {
					klog.Warning("Error retrieving appset ", err)
				}
			}
		}
	}

	for _, m := range missingAppset {
		// Remove yaml
		YAML := fmt.Sprintf("%s_%s.yaml", m.Namespace, m.Name)
		if err := os.Remove(filepath.Join(r.ResourceDir, YAML)); err != nil {
			klog.Warningf("Failed to remove file %v err %v", filepath.Join(r.ResourceDir, YAML), err)
		}
	}

	if err := r.cleanupOrphanReports(); err != nil {
		return err
	}

	return nil
}

func (r *ReconcilePullModelAggregation) cleanupOrphanReports() error {
	appsetReportList := &appsetreportV1alpha1.MulticlusterApplicationSetReportList{}

	var missingAppset []appsetreportV1alpha1.MulticlusterApplicationSetReport

	if err := r.List(context.TODO(), appsetReportList); err != nil {
		klog.Errorf("Failed to list multiclusterapplicationsetreports, err: %v", appsetReportList)

		return err
	}

	for _, appsetReport := range appsetReportList.Items {
		klog.V(1).Info("Check if corresponding appset exists for Report, ", appsetReport.Namespace, appsetReport.Name)

		existingAppset := &unstructured.Unstructured{}
		existingAppset.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "argoproj.io",
			Version: "v1alpha1",
			Kind:    "ApplicationSet",
		})

		if err := r.Get(context.TODO(), types.NamespacedName{Namespace: appsetReport.Namespace,
			Name: appsetReport.Name}, existingAppset); err != nil {
			if errors.IsNotFound(err) {
				klog.Info("Appset not found for Report ", appsetReport.Namespace, appsetReport.Name)

				missingAppset = append(missingAppset, appsetReport)
			} else {
				klog.Warning("Error retrieving appset ", err)
			}
		}
	}

	for m := range missingAppset {
		appSet := &missingAppset[m]
		errOccured := false

		if len(appSet.Statuses.ClusterConditions) > 0 {
			for _, clusterCondition := range appSet.Statuses.ClusterConditions {
				nsn := strings.Split(clusterCondition.App, "/")

				if len(nsn) > 3 {
					workNamespace := nsn[2]
					workName := nsn[3]
					work := &workv1.ManifestWork{}

					if err := r.Get(context.TODO(), types.NamespacedName{Namespace: workNamespace, Name: workName}, work); err != nil {
						if !errors.IsNotFound(err) {
							klog.Error("Couldn't get ManifestWork", err)

							errOccured = true
						}

						continue
					}

					deleteInBackground := metav1.DeletePropagationBackground
					if err := r.Delete(context.TODO(), work, &client.DeleteOptions{PropagationPolicy: &deleteInBackground}); err != nil {
						if !errors.IsNotFound(err) {
							klog.Error("Couldn't delete ManifestWork", err)

							errOccured = true
						}
					}
				}
			}
		}

		if errOccured {
			continue
		}

		// Remove orphaned report
		if err := r.Delete(context.TODO(), &missingAppset[m]); err != nil {
			if errors.IsNotFound(err) {
				klog.Info("Couldn't find Multiclusterappsetreport to delete ", err)
			}
		}
	}

	return nil
}

func loadAppSetCRD(pathname string) (*appsetreportV1alpha1.MulticlusterApplicationSetReport, error) {
	klog.V(1).Info("Loading appsSet CRD ", pathname)

	var (
		err     error
		crddata []byte
		crdobj  appsetreportV1alpha1.MulticlusterApplicationSetReport
	)

	crddata, err = os.ReadFile(filepath.Clean(pathname))

	if err != nil {
		return nil, err
	}

	err = yaml.Unmarshal(crddata, &crdobj)

	if err != nil {
		return nil, err
	}

	return &crdobj, nil
}

func ParseNamespacedName(namespacedName string) (string, string) {
	parsedstr := strings.Split(namespacedName, "/")

	if len(parsedstr) != 2 {
		klog.Infof("invalid namespacedName: %v", namespacedName)
		return "", ""
	}

	return parsedstr[0], parsedstr[1]
}

func GetMemUsage(title string) string {
	var m runtime.MemStats

	runtime.ReadMemStats(&m)

	// For info on each, see: https://golang.org/pkg/runtime/#MemStats
	usage := title
	usage += fmt.Sprintf("\nAlloc = %v MiB", bToMb(m.Alloc))
	usage += fmt.Sprintf("\n\tTotalAlloc = %v MiB", bToMb(m.TotalAlloc))
	usage += fmt.Sprintf("\n\tSys = %v MiB", bToMb(m.Sys))
	usage += fmt.Sprintf("\n\tNumGC = %v\n", m.NumGC)

	return usage
}

func bToMb(b uint64) uint64 {
	return b / 1024 / 1024
}
