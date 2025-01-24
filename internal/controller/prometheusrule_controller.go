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

	"reflect"

	"github.com/gtriggiano/mimir-rules-sync/api/v1alpha1"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// PrometheusRuleReconciler reconciles a PrometheusRule object
type PrometheusRuleReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=prometheusrules,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=prometheusrules/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

// Kubernetes reconciliation loop
func (r *PrometheusRuleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var log = log.FromContext(ctx)

	prometheusRule := &monitoringv1.PrometheusRule{}
	if err := r.Get(ctx, req.NamespacedName, prometheusRule); err != nil {
		if errors.IsNotFound(err) {
			log.Info("PrometheusRule resource not found. Ignoring since object must be deleted.")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get PrometheusRule")
		return ctrl.Result{}, err
	}

	prometheusRuleIsBeingDeleted := !prometheusRule.ObjectMeta.DeletionTimestamp.IsZero()

	if !prometheusRuleIsBeingDeleted {
		err := r.setFinalizer(prometheusRule)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	prometheusRuleSync := &v1alpha1.PrometheusRuleSync{}
	if err := r.Get(ctx, req.NamespacedName, prometheusRuleSync); err != nil {
		if errors.IsNotFound(err) {
			if prometheusRuleIsBeingDeleted {
				// Reconcialiation is not needed if the PrometheusRule is being deleted and the PrometheusRuleSync does not exist
				return ctrl.Result{}, r.removeFinalizer(prometheusRule)
			}

			// Build a new PrometheusRuleSync
			prometheusRuleSync = buildPrometheusRuleSync(prometheusRule)

			// Set the owner reference to the PrometheusRule
			if err := controllerutil.SetControllerReference(prometheusRule, prometheusRuleSync, r.Scheme); err != nil {
				log.Error(err, "Failed to set owner reference on PrometheusRuleSync")
				return ctrl.Result{}, err
			}

			// Create the PrometheusRuleSync
			if err := r.Create(ctx, prometheusRuleSync); err != nil {
				log.Error(err, "Failed to create PrometheusRuleSync")
				r.Recorder.Event(prometheusRule, "Warning", "FailedRuleSyncCreation", "Failed to create PrometheusRuleSync")
				return ctrl.Result{}, err
			}

			log.Info("PrometheusRuleSync created")
			r.Recorder.Event(prometheusRule, "Normal", "RuleSyncCreated", "PrometheusRuleSync created")
			return ctrl.Result{}, nil
		} else {
			log.Error(err, "Failed to get PrometheusRuleSync")
			return ctrl.Result{}, err
		}
	} else {
		if prometheusRuleIsBeingDeleted {
			// Delete the PrometheusRuleSync if the PrometheusRule is being deleted
			if err := r.Delete(ctx, prometheusRuleSync); err != nil {
				log.Error(err, "Failed to delete PrometheusRuleSync")
				return ctrl.Result{}, err
			}

			return ctrl.Result{}, r.removeFinalizer(prometheusRule)
		} else {
			expectedRuleSync := buildPrometheusRuleSync(prometheusRule)
			if !reflect.DeepEqual(prometheusRuleSync.Spec, expectedRuleSync.Spec) {
				// The PrometheusRuleSync is outdated and needs to be updated
				prometheusRuleSync.Spec = expectedRuleSync.Spec
				if err := r.Update(ctx, prometheusRuleSync); err != nil {
					log.Error(err, "Failed to update PrometheusRuleSync")
					r.Recorder.Event(prometheusRule, "Warning", "FailedRuleSyncUpdate", "Failed to update PrometheusRuleSync")
					return ctrl.Result{}, err
				}

				log.Info("PrometheusRuleSync updated")
				r.Recorder.Event(prometheusRule, "Normal", "RuleSyncUpdated", "PrometheusRuleSync updated")
			}

			return ctrl.Result{}, nil
		}
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *PrometheusRuleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("prometheusrule").
		For(&monitoringv1.PrometheusRule{}).
		Owns(&v1alpha1.PrometheusRuleSync{}).
		Complete(r)
}

// setFinalizer adds the finalizer to the PrometheusRule
func (r *PrometheusRuleReconciler) setFinalizer(prometheusRule *monitoringv1.PrometheusRule) error {
	if !controllerutil.ContainsFinalizer(prometheusRule, Finalizer) {
		controllerutil.AddFinalizer(prometheusRule, Finalizer)
		return r.Update(context.Background(), prometheusRule)
	}
	return nil
}

// removeFinalizer removes the finalizer from the PrometheusRule
func (r *PrometheusRuleReconciler) removeFinalizer(prometheusRule *monitoringv1.PrometheusRule) error {
	if controllerutil.ContainsFinalizer(prometheusRule, Finalizer) {
		controllerutil.RemoveFinalizer(prometheusRule, Finalizer)
		return r.Update(context.Background(), prometheusRule)
	}
	return nil
}

// buildPrometheusRuleSync maps the monitoringv1.PrometheusRule to the v1alpha1.PrometheusRuleSync
func buildPrometheusRuleSync(prometheusRule *monitoringv1.PrometheusRule) *v1alpha1.PrometheusRuleSync {
	return &v1alpha1.PrometheusRuleSync{
		ObjectMeta: v1.ObjectMeta{
			Name:        prometheusRule.Name,
			Namespace:   prometheusRule.Namespace,
			Labels:      prometheusRule.Labels,
			Annotations: prometheusRule.Annotations,
		},
		Spec: buildPrometheusRuleSyncSpec(prometheusRule.Spec),
	}
}

// buildPrometheusRuleSyncSpec maps the monitoringv1.PrometheusRuleSpec to the v1alpha1.PrometheusRuleSyncSpec
func buildPrometheusRuleSyncSpec(prometheusRuleSpec monitoringv1.PrometheusRuleSpec) v1alpha1.PrometheusRuleSyncSpec {
	groups := make([]v1alpha1.RuleGroup, len(prometheusRuleSpec.Groups))
	for i, group := range prometheusRuleSpec.Groups {
		groups[i] = v1alpha1.RuleGroup{
			Name:     group.Name,
			Interval: durationToV1Alpha1(group.Interval),
			Rules:    buildRules(group.Rules),
		}
	}

	return v1alpha1.PrometheusRuleSyncSpec{
		Groups: groups,
	}
}

// durationToV1Alpha1 maps the monitoringv1.Duration duration to the v1alpha1.Duration
func durationToV1Alpha1(duration *monitoringv1.Duration) *v1alpha1.Duration {
	if duration == nil {
		return nil
	}
	durationStr := string(*duration)
	return (*v1alpha1.Duration)(&durationStr)
}

// buildRules maps the monitoringv1.Rule list to the v1alpha1.Rule list
func buildRules(rules []monitoringv1.Rule) []v1alpha1.Rule {
	rulesList := make([]v1alpha1.Rule, len(rules))
	for i, rule := range rules {
		rulesList[i] = buildRule(rule)
	}
	return rulesList
}

// buildRule maps the monitoringv1.Rule to the v1alpha1.Rule
func buildRule(rule monitoringv1.Rule) v1alpha1.Rule {
	return v1alpha1.Rule{
		Alert:       rule.Alert,
		Expr:        rule.Expr.String(),
		For:         durationToV1Alpha1(rule.For),
		Labels:      rule.Labels,
		Annotations: rule.Annotations,
		Record:      rule.Record,
	}
}
