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
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

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
// +kubebuilder:rbac:groups=*,resources=*,verbs=get;list;watch

// Reconcile evaluates policy rules and heals matching resources.
func (r *SelfHealingPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var policy reliabilityv1alpha1.SelfHealingPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	actions := make(map[reliabilityv1alpha1.HealingAction]struct{}, len(policy.Spec.AllowedActions))
	for _, a := range policy.Spec.AllowedActions {
		actions[a] = struct{}{}
	}

	healed, lastAction, lastReason, err := r.handleUnstructured(ctx, &policy, actions)
	if err != nil {
		return ctrl.Result{}, err
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

func (r *SelfHealingPolicyReconciler) handleUnstructured(
	ctx context.Context,
	policy *reliabilityv1alpha1.SelfHealingPolicy,
	allowed map[reliabilityv1alpha1.HealingAction]struct{},
) (int32, string, string, error) {
	ns := policy.Spec.Target.Namespace
	if ns == "" {
		ns = policy.Namespace
	}

	gvk := schema.FromAPIVersionAndKind(policy.Spec.Target.APIVersion, policy.Spec.Target.Kind)
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk)

	selector := labels.SelectorFromSet(policy.Spec.Target.LabelSelector)
	if err := r.List(ctx, list, client.InNamespace(ns), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return 0, "NoAction", "failed to list resources", err
	}

	var healed int32
	lastAction := "NoAction"
	lastReason := "No unhealthy resources detected"
	for i := range list.Items {
		resource := &list.Items[i]
		unhealthy, reason, err := r.isUnhealthy(ctx, resource, policy.Spec.Conditions)
		if err != nil {
			return 0, "NoAction", "failed to check resource health", err
		}
		if !unhealthy {
			continue
		}
		var metric int32
		if val, ok := resource.Object["restartCount"].(int32); ok {
			metric = val
		}
		action, aiReason, err := r.pickAction(ctx, policy, allowed, resource.GetKind(), resource.GetName(), resource.GetNamespace(), metric, reason, time.Since(resource.GetCreationTimestamp().Time))
		if err != nil {
			return healed, lastAction, lastReason, err
		}

		if err := r.executeAction(ctx, policy, resource, action); err != nil {
			return healed, string(action), aiReason, err
		}

		healed++
		lastAction = string(action)
		lastReason = aiReason
	}

	return healed, lastAction, lastReason, nil
}

func (r *SelfHealingPolicyReconciler) isUnhealthy(ctx context.Context, resource *unstructured.Unstructured, rules []reliabilityv1alpha1.ConditionRule) (bool, string, error) {
	ageSeconds := int64(time.Since(resource.GetCreationTimestamp().Time).Seconds())
	for _, rule := range rules {
		if ageSeconds < rule.MinAgeSeconds {
			continue
		}

		switch rule.Type {
		case "RestartCount":
			if rule.Threshold == 0 {
				continue
			}
			var restartCount int32
			containerStatuses, found, err := unstructured.NestedSlice(resource.Object, "status", "containerStatuses")
			if !found || err != nil {
				continue
			}
			for _, status := range containerStatuses {
				if containerStatus, ok := status.(map[string]interface{}); ok {
					if count, found, err := unstructured.NestedInt64(containerStatus, "restartCount"); found && err == nil {
						restartCount += int32(count)
					}
				}
			}

			if restartCount >= rule.Threshold {
				return true, fmt.Sprintf("pod restart count %d >= %d", restartCount, rule.Threshold), nil
			}
		case "UnavailableReplicas":
			if rule.Threshold == 0 {
				continue
			}
			unavailable, found, err := unstructured.NestedInt64(resource.Object, "status", "unavailableReplicas")
			if !found || err != nil {
				continue
			}
			if int32(unavailable) >= rule.Threshold {
				return true, fmt.Sprintf("unavailable replicas %d >= %d", unavailable, rule.Threshold), nil
			}
		case "HasWarningEvents":
			var eventList corev1.EventList
			opts := []client.ListOption{
				client.InNamespace(resource.GetNamespace()),
				client.MatchingFields{"involvedObject.name": resource.GetName()},
			}
			if err := r.List(ctx, &eventList, opts...); err != nil {
				return false, "", fmt.Errorf("failed to list events: %w", err)
			}

			for _, event := range eventList.Items {
				if event.InvolvedObject.UID == resource.GetUID() && event.Type == corev1.EventTypeWarning {
					// Consider events within the last hour to be recent.
					if event.LastTimestamp.Time.After(time.Now().Add(-1 * time.Hour)) {
						return true, fmt.Sprintf("found warning event: %s", event.Message), nil
					}
				}
			}
		}
	}
	return false, "", nil
}

func (r *SelfHealingPolicyReconciler) executeAction(ctx context.Context, policy *reliabilityv1alpha1.SelfHealingPolicy, resource *unstructured.Unstructured, action reliabilityv1alpha1.HealingAction) error {
	if policy.Spec.DryRun || action == reliabilityv1alpha1.HealingActionNoAction {
		return nil
	}

	switch action {
	case reliabilityv1alpha1.HealingActionDelete:
		return r.Delete(ctx, resource)
	case reliabilityv1alpha1.HealingActionAnnotate:
		patched := resource.DeepCopy()
		annotations := patched.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations["reliability.platform.ai/healed-at"] = time.Now().UTC().Format(time.RFC3339)
		patched.SetAnnotations(annotations)
		return r.Patch(ctx, patched, client.MergeFrom(resource))
	case reliabilityv1alpha1.HealingActionRolloutRestart:
		patched := resource.DeepCopy()
		annotations, found, err := unstructured.NestedStringMap(patched.Object, "spec", "template", "metadata", "annotations")
		if err != nil {
			return err
		}
		if !found {
			annotations = make(map[string]string)
		}
		annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().UTC().Format(time.RFC3339)
		unstructured.SetNestedStringMap(patched.Object, annotations, "spec", "template", "metadata", "annotations")
		return r.Patch(ctx, patched, client.MergeFrom(resource))
	}
	return nil
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
	allowedList := make([]reliabilityv1alpha1.HealingAction, 0, len(allowed))
	for action := range allowed {
		allowedList = append(allowedList, action)
	}
	sort.Slice(allowedList, func(i, j int) bool {
		return allowedList[i] < allowedList[j]
	})

	fallback := allowedList[0]
	if !policy.Spec.AI.Enabled {
		return fallback, "selected first allowed action (AI disabled)", nil
	}

	resp, err := r.callAIService(ctx, policy, allowed, kind, name, namespace, metric, reason, age)
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
func (r *SelfHealingPolicyReconciler) callAIService(
	ctx context.Context,
	policy *reliabilityv1alpha1.SelfHealingPolicy,
	allowed map[reliabilityv1alpha1.HealingAction]struct{},
	kind, name, namespace string,
	metric int32,
	reason string,
	age time.Duration,
) (aiEvaluationResponse, error) {
	reqBody := aiEvaluationRequest{}
	for action := range allowed {
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
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Event{}, "involvedObject.name", func(rawObj client.Object) []string {
		event := rawObj.(*corev1.Event)
		return []string{event.InvolvedObject.Name}
	}); err != nil {
		return err
	}

	if r.HTTPClient == nil {
		r.HTTPClient = &http.Client{Timeout: defaultAITimeoutSeconds * time.Second}
	}

	// This mapFunc will be used by the dynamic watches to enqueue policies that target the changed object
	mapFunc := func(ctx context.Context, obj client.Object) []reconcile.Request {
		var policies reliabilityv1alpha1.SelfHealingPolicyList
		if err := mgr.GetClient().List(ctx, &policies); err != nil {
			log.FromContext(ctx).Error(err, "failed to list SelfHealingPolicies to enqueue")
			return nil
		}

		var requests []reconcile.Request
		for _, policy := range policies.Items {
			if policy.Spec.Target.APIVersion == obj.GetObjectKind().GroupVersionKind().GroupVersion().String() &&
				policy.Spec.Target.Kind == obj.GetObjectKind().GroupVersionKind().Kind {
				selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{MatchLabels: policy.Spec.Target.LabelSelector})
				if err != nil {
					log.FromContext(ctx).Error(err, "failed to create selector from policy", "policy", policy.Name)
					continue
				}
				if selector.Matches(labels.Set(obj.GetLabels())) {
					requests = append(requests, reconcile.Request{
						NamespacedName: types.NamespacedName{
							Name:      policy.Name,
							Namespace: policy.Namespace,
						},
					})
				}
			}
		}
		return requests
	}

	// At startup, list all policies and create watches for their targets.
	// This has a limitation: policies created after startup will not have their targets watched
	// until the manager is restarted.
	var initialPolicies reliabilityv1alpha1.SelfHealingPolicyList
	uncachedClient, err := client.New(mgr.GetConfig(), client.Options{
		Scheme: mgr.GetScheme(),
		Mapper: mgr.GetRESTMapper(),
	})
	if err != nil {
		return fmt.Errorf("failed to create uncached client: %w", err)
	}

	if err := uncachedClient.List(context.Background(), &initialPolicies); err != nil {
		return fmt.Errorf("failed to list initial policies: %w", err)
	}

	builder := ctrl.NewControllerManagedBy(mgr).
		For(&reliabilityv1alpha1.SelfHealingPolicy{}).
		Named("selfhealingpolicy")

	watchedGVKs := make(map[schema.GroupVersionKind]bool)
	for _, policy := range initialPolicies.Items {
		gvk := schema.FromAPIVersionAndKind(policy.Spec.Target.APIVersion, policy.Spec.Target.Kind)
		if !watchedGVKs[gvk] {
			u := &unstructured.Unstructured{}
			u.SetGroupVersionKind(gvk)
			builder.Watches(
				u,
				handler.EnqueueRequestsFromMapFunc(mapFunc),
			)
			watchedGVKs[gvk] = true
		}
	}

	return builder.Complete(r)
}
