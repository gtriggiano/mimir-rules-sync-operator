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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Duration is a valid time duration that can be parsed by Prometheus model.ParseDuration() function.
// Supported units: y, w, d, h, m, s, ms
// Examples: `30s`, `1m`, `1h20m15s`, `15d`
// +kubebuilder:validation:Pattern:="^(0|(([0-9]+)y)?(([0-9]+)w)?(([0-9]+)d)?(([0-9]+)h)?(([0-9]+)m)?(([0-9]+)s)?(([0-9]+)ms)?)$"
type Duration string

// PrometheusRuleSyncSpec defines the desired state of PrometheusRuleSync.
type PrometheusRuleSyncSpec struct {
	Groups []RuleGroup `json:"groups,omitempty"`
}

// RuleGroup is a list of sequentially evaluated recording and alerting rules.
type RuleGroup struct {
	// Name of the rule group.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Interval determines how often rules in the group are evaluated.
	// +optional
	Interval *Duration `json:"interval,omitempty"`
	// List of alerting and recording rules.
	Rules []Rule `json:"rules"`
}

// Rule describes an alerting or recording rule
type Rule struct {
	// Name of the time series to output to. Must be a valid metric name.
	// Only one of `record` and `alert` must be set.
	Record string `json:"record,omitempty"`
	// Name of the alert. Must be a valid label value.
	// Only one of `record` and `alert` must be set.
	Alert string `json:"alert,omitempty"`
	// PromQL expression to evaluate.
	Expr string `json:"expr"`
	// Alerts are considered firing once they have been returned for this long.
	// +optional
	For *Duration `json:"for,omitempty"`
	// Labels to add or overwrite.
	Labels map[string]string `json:"labels,omitempty"`
	// Annotations to add to each alert.
	// Only valid for alerting rules.
	Annotations map[string]string `json:"annotations,omitempty"`
}

// PrometheusRuleSyncStatus defines the observed state of PrometheusRuleSync.
type PrometheusRuleSyncStatus struct {
	MimirTenant    string                `json:"mimirTenant,omitempty"`
	MimirNamespace string                `json:"mimirNamespace"`
	RuleGroups     []RuleGroupSyncStatus `json:"ruleGroups"`
}

type RuleGroupSyncStatus struct {
	Name         string      `json:"name"`
	LastSyncTime metav1.Time `json:"lastSyncTime"`
	Synchronized bool        `json:"synchronized"`
	Error        string      `json:"error,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// PrometheusRuleSync is the Schema for the prometheusrulesyncs API.
type PrometheusRuleSync struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PrometheusRuleSyncSpec   `json:"spec,omitempty"`
	Status PrometheusRuleSyncStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PrometheusRuleSyncList contains a list of PrometheusRuleSync.
type PrometheusRuleSyncList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PrometheusRuleSync `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PrometheusRuleSync{}, &PrometheusRuleSyncList{})
}
