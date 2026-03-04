/*
Copyright 2026.

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

// +kubebuilder:validation:Enum=Delete;RolloutRestart;Annotate;NoAction
// HealingAction defines the actions the controller is allowed to execute.
type HealingAction string

// Legacy API structs retained for backward compatibility with generated code.
type Target struct {
	Kind          string            `json:"kind,omitempty"`
	LabelSelector map[string]string `json:"labelSelector,omitempty"`
}

type Conditions struct {
	ConditionsType   string `json:"conditionstype,omitempty"`
	RestartThreshold string `json:"restartThreshold,omitempty"`
}

type Resource struct {
	Target     Target     `json:"target,omitempty"`
	Conditions Conditions `json:"conditions,omitempty"`
}

type AiOptions struct {
	Enabled bool   `json:"enabled,omitempty"`
	Mode    string `json:"mode,omitempty"`
}

const (
	HealingActionDelete         HealingAction = "Delete"
	HealingActionRolloutRestart HealingAction = "RolloutRestart"
	HealingActionAnnotate       HealingAction = "Annotate"
	HealingActionNoAction       HealingAction = "NoAction"
)

// ResourceSelector defines which resources are monitored by the policy.
type ResourceSelector struct {
	// APIVersion of the target resource, e.g. apps/v1.
	// +optional
	APIVersion string `json:"apiVersion,omitempty"`

	// Kind of the resource to monitor.
	// +kubebuilder:validation:Required
	Kind string `json:"kind"`

	// Namespace scope. If empty, the policy namespace is used.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// LabelSelector filters resources by labels.
	// +optional
	LabelSelector map[string]string `json:"labelSelector,omitempty"`
}

// ConditionRule configures when a resource should be considered unhealthy.
type ConditionRule struct {
	// Type identifies the metric being evaluated.
	// Supported values: RestartCount, UnavailableReplicas.
	// +kubebuilder:validation:Required
	Type string `json:"type"`

	// Threshold is the minimum metric value that marks a resource as unhealthy.
	// +kubebuilder:validation:Minimum=1
	Threshold int32 `json:"threshold"`

	// MinAgeSeconds prevents acting on brand-new resources.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MinAgeSeconds int64 `json:"minAgeSeconds,omitempty"`
}

// AIOptions controls if and how the AI service should influence decisions.
type AIOptions struct {
	// Enabled toggles AI-based decisioning.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Mode decides if AI is only advisory or can enforce actions.
	// +kubebuilder:validation:Enum=advisory;enforce
	// +optional
	Mode string `json:"mode,omitempty"`

	// Endpoint is the AI evaluator endpoint, e.g. http://ai-service:8081/evaluate.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// TimeoutSeconds for the AI HTTP call.
	// +kubebuilder:validation:Minimum=1
	// +optional
	TimeoutSeconds int32 `json:"timeoutSeconds,omitempty"`
}

// SelfHealingPolicySpec defines the desired state of SelfHealingPolicy.
type SelfHealingPolicySpec struct {

	// Legacy field (deprecated): use AI.
	// +optional
	AiOptions AiOptions `json:"aiOptions,omitempty"`

	// Target resource selector to evaluate.
	// +kubebuilder:validation:Required
	Target ResourceSelector `json:"target"`

	// Conditions determine when to trigger healing.
	// +kubebuilder:validation:MinItems=1
	Conditions []ConditionRule `json:"conditions"`

	// NotAllowedActions lets you blacklist certain healing actions, e.g. if RolloutRestart is not feasible.
	NotAllowedActions []HealingAction `json:"notAllowedActions"`

	// AI controls optional AI integration.
	// +optional
	AI AIOptions `json:"ai,omitempty"`

	// Alert configures optional alerting on unhealthy resources.
	// +optional
	Alert AlertOptions `json:"alert,omitempty"`

	// DryRun records decisions but does not mutate resources.
	// +optional
	DryRun bool `json:"dryRun,omitempty"`
}

type AlertOptions struct {

	// Enabled toggles AI-based decisioning.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	SlackEndpoint string `json:"slackEndpoint,omitempty"`
}

// SelfHealingPolicyStatus defines the observed state of SelfHealingPolicy.
type SelfHealingPolicyStatus struct {
	// ObservedGeneration is the latest generation reconciled by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastEvaluatedTime is when policy evaluation last completed.
	// +optional
	LastEvaluatedTime *metav1.Time `json:"lastEvaluatedTime,omitempty"`

	// LastAction is the last selected action.
	// +optional
	LastAction string `json:"lastAction,omitempty"`

	// LastReason explains why LastAction was chosen.
	// +optional
	LastReason string `json:"lastReason,omitempty"`

	// HealedResources is the number of resources acted on in the latest run.
	// +optional
	HealedResources int32 `json:"healedResources,omitempty"`

	// conditions represent the current state of the SelfHealingPolicy resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// SelfHealingPolicy is the Schema for the selfhealingpolicies API.
type SelfHealingPolicy struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of SelfHealingPolicy
	// +required
	Spec SelfHealingPolicySpec `json:"spec"`

	// status defines the observed state of SelfHealingPolicy
	// +optional
	Status SelfHealingPolicyStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// SelfHealingPolicyList contains a list of SelfHealingPolicy.
type SelfHealingPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []SelfHealingPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SelfHealingPolicy{}, &SelfHealingPolicyList{})
}
