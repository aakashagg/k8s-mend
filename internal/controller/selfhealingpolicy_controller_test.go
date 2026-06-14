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
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	reliabilityv1alpha1 "github.com/akashagg/k8s-mend/api/v1alpha1"
)

var _ = Describe("SelfHealingPolicy Controller", func() {
	const namespace = "default"

	ctx := context.Background()

	reconciler := func() *SelfHealingPolicyReconciler {
		return &SelfHealingPolicyReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	}

	cleanup := func(name types.NamespacedName, obj client.Object) {
		err := k8sClient.Get(ctx, name, obj)
		if err == nil {
			Expect(k8sClient.Delete(ctx, obj)).To(Succeed())
		}
	}

	Context("when a target Pod is unhealthy", func() {
		typeNamespacedName := types.NamespacedName{
			Name:      "restart-policy",
			Namespace: namespace,
		}
		podName := types.NamespacedName{
			Name:      "restart-pod",
			Namespace: namespace,
		}

		BeforeEach(func() {
			By("creating an unhealthy Pod target")
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      podName.Name,
					Namespace: podName.Namespace,
					Labels: map[string]string{
						"app": "restart-demo",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "app",
						Image: "busybox",
					}},
				},
			}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())

			Expect(k8sClient.Get(ctx, podName, pod)).To(Succeed())
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
				Name:         "app",
				RestartCount: 2,
			}}
			Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

			By("creating a dry-run policy for the Pod")
			policy := &reliabilityv1alpha1.SelfHealingPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      typeNamespacedName.Name,
					Namespace: typeNamespacedName.Namespace,
				},
				Spec: reliabilityv1alpha1.SelfHealingPolicySpec{
					Target: reliabilityv1alpha1.ResourceSelector{
						APIVersion: "v1",
						Kind:       "Pod",
						Namespace:  namespace,
						LabelSelector: map[string]string{
							"app": "restart-demo",
						},
					},
					Conditions:     []reliabilityv1alpha1.ConditionRule{{Type: "RestartCount", Threshold: 1}},
					AllowedActions: []reliabilityv1alpha1.HealingAction{reliabilityv1alpha1.HealingActionAnnotate},
					DryRun:         true,
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		})

		AfterEach(func() {
			cleanup(typeNamespacedName, &reliabilityv1alpha1.SelfHealingPolicy{})
			cleanup(podName, &corev1.Pod{})
		})

		It("updates status without mutating the Pod in dry-run mode", func() {
			_, err := reconciler().Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			policy := &reliabilityv1alpha1.SelfHealingPolicy{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, policy)).To(Succeed())
			Expect(policy.Status.ObservedGeneration).To(Equal(policy.Generation))
			Expect(policy.Status.HealedResources).To(Equal(int32(1)))
			Expect(policy.Status.LastAction).To(Equal(string(reliabilityv1alpha1.HealingActionAnnotate)))
			Expect(policy.Status.LastReason).To(Equal("selected first allowed action (AI disabled)"))
			Expect(policy.Status.LastEvaluatedTime).NotTo(BeNil())
			readyCondition := apimeta.FindStatusCondition(policy.Status.Conditions, "Ready")
			Expect(readyCondition).NotTo(BeNil())
			Expect(readyCondition.Status).To(Equal(metav1.ConditionTrue))
			Expect(readyCondition.Reason).To(Equal("PolicyEvaluated"))

			pod := &corev1.Pod{}
			Expect(k8sClient.Get(ctx, podName, pod)).To(Succeed())
			Expect(pod.Annotations).NotTo(HaveKey("reliability.platform.ai/healed-at"))
		})
	})

	Context("when annotations are allowed", func() {
		typeNamespacedName := types.NamespacedName{
			Name:      "annotate-policy",
			Namespace: namespace,
		}
		podName := types.NamespacedName{
			Name:      "annotate-pod",
			Namespace: namespace,
		}

		BeforeEach(func() {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      podName.Name,
					Namespace: podName.Namespace,
					Labels: map[string]string{
						"app": "annotate-demo",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "app",
						Image: "busybox",
					}},
				},
			}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())

			Expect(k8sClient.Get(ctx, podName, pod)).To(Succeed())
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
				Name:         "app",
				RestartCount: 3,
			}}
			Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

			policy := &reliabilityv1alpha1.SelfHealingPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      typeNamespacedName.Name,
					Namespace: typeNamespacedName.Namespace,
				},
				Spec: reliabilityv1alpha1.SelfHealingPolicySpec{
					Target: reliabilityv1alpha1.ResourceSelector{
						APIVersion: "v1",
						Kind:       "Pod",
						Namespace:  namespace,
						LabelSelector: map[string]string{
							"app": "annotate-demo",
						},
					},
					Conditions:     []reliabilityv1alpha1.ConditionRule{{Type: "RestartCount", Threshold: 1}},
					AllowedActions: []reliabilityv1alpha1.HealingAction{reliabilityv1alpha1.HealingActionAnnotate},
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		})

		AfterEach(func() {
			cleanup(typeNamespacedName, &reliabilityv1alpha1.SelfHealingPolicy{})
			cleanup(podName, &corev1.Pod{})
		})

		It("annotates the unhealthy Pod", func() {
			_, err := reconciler().Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			pod := &corev1.Pod{}
			Expect(k8sClient.Get(ctx, podName, pod)).To(Succeed())
			Expect(pod.Annotations).To(HaveKey("reliability.platform.ai/healed-at"))
		})
	})
})
