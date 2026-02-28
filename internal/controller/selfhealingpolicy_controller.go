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

package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	reliabilityv1alpha1 "github.com/akashagg/k8s-mend/api/v1alpha1"
)

const (
	conditionTypeReady        = "Ready"
	reasonEvaluated           = "PolicyEvaluated"
	defaultAIEndpoint         = "http://ai-service:8081/evaluate"
	defaultAITimeoutSeconds   = 5
	defaultRequeueIntervalSec = 30
)

type aiEvaluationRequest struct {
	Policy struct {
		AllowedActions []string `json:"allowedActions"`
		Mode           string   `json:"mode"`
	} `json:"policy"`

	Resource struct {
		Kind         string `json:"kind"`
		Name         string `json:"name"`
		Namespace    string `json:"namespace"`
		RestartCount int32  `json:"restartCount"`
		Reason       string `json:"reason"`
		AgeSeconds   int64  `json:"ageSeconds"`
	} `json:"resource"`
}

type aiEvaluationResponse struct {
	Action     string  `json:"action"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// SelfHealingPolicyReconciler reconciles a SelfHealingPolicy object.
type SelfHealingPolicyReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	HTTPClient *http.Client
}

// +kubebuilder:rbac:groups=reliability.platform.ai,resources=selfhealingpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=reliability.platform.ai,resources=selfhealingpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=reliability.platform.ai,resources=selfhealingpolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete;patch;update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;patch;update

// Reconcile evaluates policy rules and heals matching resources.
func (r *SelfHealingPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var policy reliabilityv1alpha1.SelfHealingPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	actions := make(map[reliabilityv1alpha1.HealingAction]struct{}, len(policy.Spec.AllowedActions))
	for _, a := range policy.Spec.AllowedActions {
		actions[a] = struct{}{}
	}

	var healed int32
	lastAction := "NoAction"
	lastReason := "No unhealthy resources detected"

	switch policy.Spec.Target.Kind {
	case "Pod":
		h, action, reason, err := r.handlePods(ctx, &policy, actions)
		if err != nil {
			return ctrl.Result{}, err
		}
		healed = h
		lastAction = action
		lastReason = reason
	case "Deployment":
		h, action, reason, err := r.handleDeployments(ctx, &policy, actions)
		if err != nil {
			return ctrl.Result{}, err
		}
		healed = h
		lastAction = action
		lastReason = reason
	default:
		lastReason = fmt.Sprintf("unsupported target kind %q", policy.Spec.Target.Kind)
		logger.Info("unsupported kind in policy", "kind", policy.Spec.Target.Kind, "name", policy.Name)
	}

	now := metav1.Now()
	policy.Status.ObservedGeneration = policy.Generation
	policy.Status.LastEvaluatedTime = &now
	policy.Status.HealedResources = healed
	policy.Status.LastAction = lastAction
	policy.Status.LastReason = lastReason
	metav1.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionTrue,
		Reason:             reasonEvaluated,
		Message:            fmt.Sprintf("evaluated policy and healed %d resources", healed),
		ObservedGeneration: policy.Generation,
		LastTransitionTime: now,
	})

	if err := r.Status().Update(ctx, &policy); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: defaultRequeueIntervalSec * time.Second}, nil
}

func (r *SelfHealingPolicyReconciler) handlePods(
	ctx context.Context,
	policy *reliabilityv1alpha1.SelfHealingPolicy,
	allowed map[reliabilityv1alpha1.HealingAction]struct{},
) (int32, string, string, error) {
	ns := policy.Spec.Target.Namespace
	if ns == "" {
		ns = policy.Namespace
	}

	selector := labels.SelectorFromSet(policy.Spec.Target.LabelSelector)
	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.InNamespace(ns), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return 0, "NoAction", "failed to list pods", err
	}

	var healed int32
	lastAction := "NoAction"
	lastReason := "No unhealthy pods detected"
	for i := range podList.Items {
		pod := &podList.Items[i]
		unhealthy, reason := isPodUnhealthy(pod, policy.Spec.Conditions)
		if !unhealthy {
			continue
		}

		action, aiReason, err := r.pickAction(ctx, policy, allowed, pod.Kind, pod.Name, pod.Namespace, podTotalRestarts(pod), reason, time.Since(pod.CreationTimestamp.Time))
		if err != nil {
			return healed, lastAction, lastReason, err
		}

		if err := r.executePodAction(ctx, policy, pod, action); err != nil {
			return healed, string(action), aiReason, err
		}

		healed++
		lastAction = string(action)
		lastReason = aiReason
	}

	return healed, lastAction, lastReason, nil
}

func (r *SelfHealingPolicyReconciler) handleDeployments(
	ctx context.Context,
	policy *reliabilityv1alpha1.SelfHealingPolicy,
	allowed map[reliabilityv1alpha1.HealingAction]struct{},
) (int32, string, string, error) {
	ns := policy.Spec.Target.Namespace
	if ns == "" {
		ns = policy.Namespace
	}

	selector := labels.SelectorFromSet(policy.Spec.Target.LabelSelector)
	var deploymentList appsv1.DeploymentList
	if err := r.List(ctx, &deploymentList, client.InNamespace(ns), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return 0, "NoAction", "failed to list deployments", err
	}

	var healed int32
	lastAction := "NoAction"
	lastReason := "No unhealthy deployments detected"
	for i := range deploymentList.Items {
		deployment := &deploymentList.Items[i]
		unhealthy, reason := isDeploymentUnhealthy(deployment, policy.Spec.Conditions)
		if !unhealthy {
			continue
		}

		action, aiReason, err := r.pickAction(ctx, policy, allowed, "Deployment", deployment.Name, deployment.Namespace, deployment.Status.UnavailableReplicas, reason, time.Since(deployment.CreationTimestamp.Time))
		if err != nil {
			return healed, lastAction, lastReason, err
		}

		if err := r.executeDeploymentAction(ctx, policy, deployment, action); err != nil {
			return healed, string(action), aiReason, err
		}

		healed++
		lastAction = string(action)
		lastReason = aiReason
	}

	return healed, lastAction, lastReason, nil
}

func isPodUnhealthy(pod *corev1.Pod, rules []reliabilityv1alpha1.ConditionRule) (bool, string) {
	restarts := podTotalRestarts(pod)
	ageSeconds := int64(time.Since(pod.CreationTimestamp.Time).Seconds())
	for _, rule := range rules {
		if rule.Type != "RestartCount" {
			continue
		}
		if ageSeconds < rule.MinAgeSeconds {
			continue
		}
		if restarts >= rule.Threshold {
			return true, fmt.Sprintf("pod restart count %d >= %d", restarts, rule.Threshold)
		}
	}
	return false, ""
}

func isDeploymentUnhealthy(deployment *appsv1.Deployment, rules []reliabilityv1alpha1.ConditionRule) (bool, string) {
	unavailable := deployment.Status.UnavailableReplicas
	ageSeconds := int64(time.Since(deployment.CreationTimestamp.Time).Seconds())
	for _, rule := range rules {
		if rule.Type != "UnavailableReplicas" {
			continue
		}
		if ageSeconds < rule.MinAgeSeconds {
			continue
		}
		if unavailable >= rule.Threshold {
			return true, fmt.Sprintf("unavailable replicas %d >= %d", unavailable, rule.Threshold)
		}
	}
	return false, ""
}

func podTotalRestarts(pod *corev1.Pod) int32 {
	var count int32
	for _, status := range pod.Status.ContainerStatuses {
		count += status.RestartCount
	}
	return count
}

func (r *SelfHealingPolicyReconciler) pickAction(
	ctx context.Context,
	policy *reliabilityv1alpha1.SelfHealingPolicy,
	allowed map[reliabilityv1alpha1.HealingAction]struct{},
	kind, name, namespace string,
	metric int32,
	reason string,
	age time.Duration,
) (reliabilityv1alpha1.HealingAction, string, error) {
	fallback := policy.Spec.AllowedActions[0]
	if !policy.Spec.AI.Enabled {
		return fallback, "selected first allowed action (AI disabled)", nil
	}

	resp, err := r.callAIService(ctx, policy, kind, name, namespace, metric, reason, age)
	if err != nil {
		return fallback, fmt.Sprintf("AI unavailable, fallback action %s", fallback), nil
	}

	action := reliabilityv1alpha1.HealingAction(resp.Action)
	if _, ok := allowed[action]; !ok {
		return fallback, fmt.Sprintf("AI action %q not allowed, fallback to %s", resp.Action, fallback), nil
	}

	if policy.Spec.AI.Mode == "advisory" {
		return fallback, fmt.Sprintf("AI suggested %s (confidence %.2f): %s", action, resp.Confidence, resp.Reason), nil
	}

	return action, fmt.Sprintf("AI selected %s (confidence %.2f): %s", action, resp.Confidence, resp.Reason), nil
}

func (r *SelfHealingPolicyReconciler) executePodAction(ctx context.Context, policy *reliabilityv1alpha1.SelfHealingPolicy, pod *corev1.Pod, action reliabilityv1alpha1.HealingAction) error {
	if policy.Spec.DryRun || action == reliabilityv1alpha1.HealingActionNoAction {
		return nil
	}

	switch action {
	case reliabilityv1alpha1.HealingActionDelete:
		return r.Delete(ctx, pod)
	case reliabilityv1alpha1.HealingActionAnnotate:
		patched := pod.DeepCopy()
		if patched.Annotations == nil {
			patched.Annotations = map[string]string{}
		}
		patched.Annotations["reliability.platform.ai/healed-at"] = time.Now().UTC().Format(time.RFC3339)
		return r.Patch(ctx, patched, client.MergeFrom(pod))
	default:
		return nil
	}
}

func (r *SelfHealingPolicyReconciler) executeDeploymentAction(ctx context.Context, policy *reliabilityv1alpha1.SelfHealingPolicy, deployment *appsv1.Deployment, action reliabilityv1alpha1.HealingAction) error {
	if policy.Spec.DryRun || action == reliabilityv1alpha1.HealingActionNoAction {
		return nil
	}

	switch action {
	case reliabilityv1alpha1.HealingActionRolloutRestart:
		patched := deployment.DeepCopy()
		if patched.Spec.Template.Annotations == nil {
			patched.Spec.Template.Annotations = map[string]string{}
		}
		patched.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().UTC().Format(time.RFC3339)
		return r.Patch(ctx, patched, client.MergeFrom(deployment))
	case reliabilityv1alpha1.HealingActionAnnotate:
		patched := deployment.DeepCopy()
		if patched.Annotations == nil {
			patched.Annotations = map[string]string{}
		}
		patched.Annotations["reliability.platform.ai/healed-at"] = time.Now().UTC().Format(time.RFC3339)
		return r.Patch(ctx, patched, client.MergeFrom(deployment))
	default:
		return nil
	}
}

func (r *SelfHealingPolicyReconciler) callAIService(
	ctx context.Context,
	policy *reliabilityv1alpha1.SelfHealingPolicy,
	kind, name, namespace string,
	metric int32,
	reason string,
	age time.Duration,
) (aiEvaluationResponse, error) {
	reqBody := aiEvaluationRequest{}
	for _, action := range policy.Spec.AllowedActions {
		reqBody.Policy.AllowedActions = append(reqBody.Policy.AllowedActions, string(action))
	}
	mode := policy.Spec.AI.Mode
	if mode == "" {
		mode = "advisory"
	}
	reqBody.Policy.Mode = mode
	reqBody.Resource.Kind = kind
	reqBody.Resource.Name = name
	reqBody.Resource.Namespace = namespace
	reqBody.Resource.RestartCount = metric
	reqBody.Resource.Reason = reason
	reqBody.Resource.AgeSeconds = int64(age.Seconds())

	endpoint := policy.Spec.AI.Endpoint
	if endpoint == "" {
		endpoint = defaultAIEndpoint
	}

	timeoutSec := policy.Spec.AI.TimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = defaultAITimeoutSeconds
	}

	httpClient := r.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: time.Duration(timeoutSec) * time.Second}
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return aiEvaluationResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return aiEvaluationResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return aiEvaluationResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return aiEvaluationResponse{}, fmt.Errorf("ai-service returned status %d", resp.StatusCode)
	}

	var out aiEvaluationResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return aiEvaluationResponse{}, err
	}
	return out, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SelfHealingPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.HTTPClient == nil {
		r.HTTPClient = &http.Client{Timeout: defaultAITimeoutSeconds * time.Second}
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&reliabilityv1alpha1.SelfHealingPolicy{}).
		Owns(&corev1.Pod{}).
		Owns(&appsv1.Deployment{}).
		Named("selfhealingpolicy").
		Complete(r)
}
