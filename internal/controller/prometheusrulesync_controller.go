/*
Copyright 2025 Giacomo Triggiano <giacomotriggiano@gmail.com>

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

package controller

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/gtriggiano/mimir-rules-sync/api/v1alpha1"
	mimirrulerclient "github.com/gtriggiano/mimir-rules-sync/internal/mimir_ruler_client"
)

const (
	TenantAnnotation          string = "mimir-rules-sync.creativecoding.it/tenant"
	NamespacePrefixAnnotation string = "mimir-rules-sync.creativecoding.it/namespace-prefix"
	Finalizer                 string = "mimir-rules-sync.creativecoding.it/finalizer"
)

// PrometheusRuleSyncReconciler reconciles a PrometheusRuleSync object
type PrometheusRuleSyncReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Recorder         record.EventRecorder
	MimirRulerClient *mimirrulerclient.MimirRulerClient
	Tenant           string
	NamespacePrefix  string
}

// +kubebuilder:rbac:groups=mimir-rules-sync.creativecoding.it,resources=prometheusrulesyncs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mimir-rules-sync.creativecoding.it,resources=prometheusrulesyncs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mimir-rules-sync.creativecoding.it,resources=prometheusrulesyncs/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

// Kubernetes reconciliation loop
func (r *PrometheusRuleSyncReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, cancel := context.WithDeadline(ctx, metav1.Now().Add(60*time.Second))
	defer cancel()

	var log = log.FromContext(ctx)

	prometheusRuleSync := &v1alpha1.PrometheusRuleSync{}
	if err := r.Get(ctx, req.NamespacedName, prometheusRuleSync); err != nil {
		if errors.IsNotFound(err) {
			log.Info("PrometheusRuleSync resource not found. Ignoring since object must be deleted.")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get PrometheusRuleSync")
		return ctrl.Result{}, err
	}

	mimirTenant := r.computeMimirTenant(prometheusRuleSync)
	mimirNamespace := r.computeMimirNamespace(prometheusRuleSync)
	mimirRuleGroups := clientMimirRuleGroupsToMap(buildClientMimirRuleGroups(prometheusRuleSync.Spec.Groups))

	log.Info(fmt.Sprintf("Reconciling PrometheusRule %s/%s to Mimir namespace %s", prometheusRuleSync.Namespace, prometheusRuleSync.Name, mimirNamespace), "tenant", mimirTenant, "namespace", prometheusRuleSync.Namespace, "name", prometheusRuleSync.Name)

	if prometheusRuleSync.ObjectMeta.DeletionTimestamp.IsZero() {
		// The object is being created or updated

		// Ensure the finalizer is set
		if !controllerutil.ContainsFinalizer(prometheusRuleSync, Finalizer) {
			controllerutil.AddFinalizer(prometheusRuleSync, Finalizer)
			if err := r.Update(ctx, prometheusRuleSync); err != nil {
				log.Error(err, "Failed to add finalizer to PrometheusRuleSync")
				return ctrl.Result{}, err
			}
		}

		// Get the RuleGroups already present in Mimir
		actualMimirRuleGroups, out, err := r.MimirRulerClient.GetNamespaceRuleGroups(mimirTenant, mimirNamespace)
		if err != nil {
			log.Error(err, fmt.Sprintf("Could not retrieve the RuleGroups already present in Mimir namespace %s", mimirNamespace), "tenant", mimirTenant, "namespace", prometheusRuleSync.Namespace, "name", prometheusRuleSync.Name)
			r.Recorder.Eventf(prometheusRuleSync, "Warning", "GetNamespaceRuleGroupsFailed", "Failed to get RuleGroups from Mimir namespace \"%s\"\n%s\n%s", mimirNamespace, err.Error(), out)
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}

		// Convert the RuleGroups to a map
		actualMimirRuleGroupsByName := clientMimirRuleGroupsToMap(actualMimirRuleGroups)

		successfulSetRuleGroupsByName := make(map[string]string)
		unsuccessfulSetRuleGroupsByName := make(map[string]string)

		if len(mimirRuleGroups) > 0 {
			// Create or update the RuleGroups
			for ruleGroupName, mimirRuleGroup := range mimirRuleGroups {
				actualMimirRuleGroup, found := actualMimirRuleGroupsByName[ruleGroupName]

				if !found || !reflect.DeepEqual(actualMimirRuleGroup, mimirRuleGroup) {
					if out, err := r.MimirRulerClient.SetRuleGroup(mimirTenant, mimirNamespace, &mimirRuleGroup); err != nil {
						unsuccessfulSetRuleGroupsByName[mimirRuleGroup.Name] = err.Error()
						log.Error(err, "Failed to set rule in Mimir", "tenant", mimirTenant, "namespace", prometheusRuleSync.Namespace, "name", prometheusRuleSync.Name)
						r.Recorder.Eventf(prometheusRuleSync, "Warning", "SetRuleGroupFailed", "Failed to set RuleGroup %s in Mimir namespace \"%s\"\n%s\n%s", mimirRuleGroup.Name, mimirNamespace, err.Error(), out)
					} else {
						successfulSetRuleGroupsByName[mimirRuleGroup.Name] = fmt.Sprintf("Rule was set in Mimir for tenant %s", mimirTenant)
						log.Info("Rule was set in Mimir", "tenant", mimirTenant, "namespace", prometheusRuleSync.Namespace, "name", prometheusRuleSync.Name)
						r.Recorder.Eventf(prometheusRuleSync, "Normal", "SetRuleGroupSuccess", "RuleGroup %s was set in Mimir namespace \"%s\"", mimirRuleGroup.Name, mimirNamespace)
					}
				}
			}
		}

		successfulDeleteByRuleGroupName := make(map[string]string)
		unsuccessfulDeleteByRuleGroupName := make(map[string]string)

		// Delete the RuleGroups that are not present in the PrometheusRuleSync
		for ruleGroupName, actualRuleGroup := range actualMimirRuleGroupsByName {
			if _, found := mimirRuleGroups[ruleGroupName]; !found {
				// Rule group is not present in the PrometheusRuleSync so it needs to be deleted
				if out, err := r.MimirRulerClient.DeleteRuleGroup(mimirTenant, mimirNamespace, actualRuleGroup.Name); err != nil {
					unsuccessfulDeleteByRuleGroupName[actualRuleGroup.Name] = err.Error()
					log.Error(err, "Failed to delete rule in Mimir", "tenant", mimirTenant, "namespace", prometheusRuleSync.Namespace, "name", prometheusRuleSync.Name)
					r.Recorder.Eventf(prometheusRuleSync, "Warning", "DeleteRuleGroupFailed", "Failed to delete RuleGroup %s in Mimir namespace \"%s\"\n%s\n%s", actualRuleGroup.Name, mimirNamespace, err.Error(), out)
				} else {
					successfulDeleteByRuleGroupName[actualRuleGroup.Name] = "OK"
					log.Info("Rule was deleted in Mimir", "tenant", mimirTenant, "namespace", prometheusRuleSync.Namespace, "name", prometheusRuleSync.Name)
					r.Recorder.Eventf(prometheusRuleSync, "Normal", "DeleteRuleGroupSuccess", "RuleGroup %s was deleted in Mimir namespace \"%s\"", actualRuleGroup.Name, mimirNamespace)
				}
			}
		}

		prometheusRuleSync.Status.MimirTenant = mimirTenant
		prometheusRuleSync.Status.MimirNamespace = mimirNamespace

		// Get a map of manipulated RuleGroups by name
		syncStatusesByRuleGroupName := buildSyncStatusesByRuleGroupName(successfulSetRuleGroupsByName, unsuccessfulSetRuleGroupsByName, successfulDeleteByRuleGroupName)

		// Take from actual status the RuleGroups present in the PrometheusRuleSync which have not been manipulated
		for _, ruleGroup := range prometheusRuleSync.Status.RuleGroups {
			if _, found := syncStatusesByRuleGroupName[ruleGroup.Name]; !found {
				if _, found := actualMimirRuleGroupsByName[ruleGroup.Name]; found {
					syncStatusesByRuleGroupName[ruleGroup.Name] = ruleGroup
				}
			}
		}

		// Create a new slice of RuleGroupSyncStatus
		newRuleGroupsSyncStatuses := make([]v1alpha1.RuleGroupSyncStatus, 0, len(syncStatusesByRuleGroupName))
		for _, syncStatus := range syncStatusesByRuleGroupName {
			newRuleGroupsSyncStatuses = append(newRuleGroupsSyncStatuses, syncStatus)
		}

		// Update the PrometheusRuleSync status
		prometheusRuleSync.Status.MimirTenant = mimirTenant
		prometheusRuleSync.Status.MimirNamespace = mimirNamespace
		prometheusRuleSync.Status.RuleGroups = newRuleGroupsSyncStatuses
		if err := r.Status().Update(ctx, prometheusRuleSync); err != nil {
			log.Error(err, "Failed to update PrometheusRuleSync status", "tenant", mimirTenant, "namespace", prometheusRuleSync.Namespace, "name", prometheusRuleSync.Name)
			return ctrl.Result{}, err
		}

		if len(unsuccessfulSetRuleGroupsByName) > 0 || len(unsuccessfulDeleteByRuleGroupName) > 0 {
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}

		return ctrl.Result{}, nil
	} else {
		// The object is being deleted
		if controllerutil.ContainsFinalizer(prometheusRuleSync, Finalizer) {
			// Delete the Mimir namespace
			if out, err := r.MimirRulerClient.DeleteNamespace(mimirTenant, mimirNamespace); err != nil {
				log.Error(err, fmt.Sprintf("Failed to delete Mimir namespace \"%s\"", mimirNamespace), "tenant", mimirTenant, "namespace", prometheusRuleSync.Namespace, "name", prometheusRuleSync.Name)
				r.Recorder.Eventf(prometheusRuleSync, "Warning", "DeleteNamespaceFailed", "Failed to delete Mimir namespace \"%s\"\n%s\n%s", mimirNamespace, err.Error(), out)
				return ctrl.Result{}, err
			}

			log.Info(fmt.Sprintf("Mimir namespace \"%s\" was deleted", mimirNamespace), "tenant", mimirTenant, "namespace", prometheusRuleSync.Namespace, "name", prometheusRuleSync.Name)

			// Remove finalizer
			controllerutil.RemoveFinalizer(prometheusRuleSync, Finalizer)
			if err := r.Update(ctx, prometheusRuleSync); err != nil {
				log.Error(err, "Failed to remove finalizer from PrometheusRuleSync", "tenant", mimirTenant, "namespace", prometheusRuleSync.Namespace, "name", prometheusRuleSync.Name)
				return ctrl.Result{}, err
			}
		}

		return ctrl.Result{}, nil
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *PrometheusRuleSyncReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("prometheusrulesync").
		For(&v1alpha1.PrometheusRuleSync{}).
		Complete(r)
}

// computeMimirNamespace computes the Mimir namespace from the PrometheusRuleSync
func (r *PrometheusRuleSyncReconciler) computeMimirNamespace(prometheusRuleSync *v1alpha1.PrometheusRuleSync) string {
	namespacePrefix := r.NamespacePrefix
	if namespacePrefixFromAnnotation, found := prometheusRuleSync.Annotations[NamespacePrefixAnnotation]; found {
		namespacePrefix = namespacePrefixFromAnnotation
	}

	if namespacePrefix == "" {
		return fmt.Sprintf("%s - %s", prometheusRuleSync.Namespace, prometheusRuleSync.Name)
	} else {
		return fmt.Sprintf("%s %s - %s", namespacePrefix, prometheusRuleSync.Namespace, prometheusRuleSync.Name)
	}
}

// computeMimirTenant computes the Mimir tenant from the PrometheusRuleSync
func (r *PrometheusRuleSyncReconciler) computeMimirTenant(prometheusRuleSync *v1alpha1.PrometheusRuleSync) string {
	tenant := r.Tenant
	if tenantFromAnnotation, found := prometheusRuleSync.Annotations[TenantAnnotation]; found {
		tenant = tenantFromAnnotation
	}
	return tenant
}

// clientMimirRuleGroupsToMap converts a slice of MimirRuleGroup to a map
func clientMimirRuleGroupsToMap(ruleGroups []mimirrulerclient.MimirRuleGroup) map[string]mimirrulerclient.MimirRuleGroup {
	dictionary := make(map[string]mimirrulerclient.MimirRuleGroup)
	for _, ruleGroup := range ruleGroups {
		dictionary[ruleGroup.Name] = ruleGroup
	}
	return dictionary
}

// buildClientMimirRuleGroups maps the v1alpha1.RuleGroup to the mimirrulerclient.MimirRuleGroup
func buildClientMimirRuleGroups(groups []v1alpha1.RuleGroup) []mimirrulerclient.MimirRuleGroup {
	mimirRuleGroups := make([]mimirrulerclient.MimirRuleGroup, len(groups))
	for i, group := range groups {
		mimirRuleGroups[i] = mimirrulerclient.MimirRuleGroup{
			Name:     group.Name,
			Interval: group.Interval,
			Rules:    buildClientMimirRule(group.Rules),
		}
	}
	return mimirRuleGroups
}

// buildClientMimirRule maps the v1alpha1.Rule to the mimirrulerclient.MimirRule
func buildClientMimirRule(rules []v1alpha1.Rule) []mimirrulerclient.MimirRule {
	mimirRules := make([]mimirrulerclient.MimirRule, len(rules))
	for i, rule := range rules {
		mimirRules[i] = mimirrulerclient.MimirRule{
			Record:      rule.Record,
			Alert:       rule.Alert,
			Expr:        rule.Expr,
			For:         rule.For,
			Labels:      rule.Labels,
			Annotations: rule.Annotations,
		}
	}
	return mimirRules
}

func buildSyncStatusesByRuleGroupName(
	successfulSetRuleGroupsByName,
	unsuccessfulSetRuleGroupsByName,
	unsuccessfulDeleteByRuleGroupName map[string]string,
) map[string]v1alpha1.RuleGroupSyncStatus {
	now := metav1.Now()
	statusByRuleName := make(map[string]v1alpha1.RuleGroupSyncStatus)

	for ruleName := range successfulSetRuleGroupsByName {
		statusByRuleName[ruleName] = v1alpha1.RuleGroupSyncStatus{
			Name:         ruleName,
			LastSyncTime: now,
			Synchronized: true,
		}
	}

	for ruleName, err := range unsuccessfulSetRuleGroupsByName {
		statusByRuleName[ruleName] = v1alpha1.RuleGroupSyncStatus{
			Name:         ruleName,
			LastSyncTime: now,
			Synchronized: false,
			Error:        err,
		}
	}

	for ruleName, err := range unsuccessfulDeleteByRuleGroupName {
		statusByRuleName[ruleName] = v1alpha1.RuleGroupSyncStatus{
			Name:         ruleName,
			LastSyncTime: now,
			Synchronized: false,
			Error:        err,
		}
	}

	return statusByRuleName
}
