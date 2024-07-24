// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package vpaevictionrequirements

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	vpaautoscalingv1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/gardener/gardener/pkg/apis/core/v1beta1/constants"
	predicateutils "github.com/gardener/gardener/pkg/controllerutils/predicate"
)

// ControllerName is the name of this controller.
const ControllerName = "vpa-eviction-requirements"

// AddToManager adds Reconciler to the given manager.
func (r *Reconciler) AddToManager(mgr manager.Manager, seedCluster cluster.Cluster) error {
	if r.SeedClient == nil {
		r.SeedClient = seedCluster.GetClient()
	}
	if r.Clock == nil {
		r.Clock = clock.RealClock{}
	}

	vpaEvictionRequirementsManagedByControllerPredicate, err := labelSelectorPredicate[*vpaautoscalingv1.VerticalPodAutoscaler](metav1.LabelSelector{
		MatchLabels: map[string]string{constants.LabelVPAEvictionRequirementsController: constants.EvictionRequirementManagedByController},
	})
	if err != nil {
		return fmt.Errorf("failed computing label selector predicate for eviction requirements managed by controller: %w", err)
	}

	predicates := []predicate.TypedPredicate[*vpaautoscalingv1.VerticalPodAutoscaler]{
		vpaEvictionRequirementsManagedByControllerPredicate,
		predicateutils.TypedForEventTypes[*vpaautoscalingv1.VerticalPodAutoscaler](predicateutils.Create, predicateutils.Update),
		predicate.Or[*vpaautoscalingv1.VerticalPodAutoscaler](predicate.TypedGenerationChangedPredicate[*vpaautoscalingv1.VerticalPodAutoscaler]{}, predicate.TypedAnnotationChangedPredicate[*vpaautoscalingv1.VerticalPodAutoscaler]{}),
	}

	return builder.
		ControllerManagedBy(mgr).
		Named(ControllerName).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: ptr.Deref(r.ConcurrentSyncs, 0),
		}).
		WatchesRawSource(
			source.Kind(seedCluster.GetCache(),
				&vpaautoscalingv1.VerticalPodAutoscaler{},
				&handler.TypedEnqueueRequestForObject[*vpaautoscalingv1.VerticalPodAutoscaler]{},
				predicates...),
		).
		Complete(r)
}

// LabelSelectorPredicate constructs a Predicate from a LabelSelector.
// Only objects matching the LabelSelector will be admitted.
func labelSelectorPredicate[T client.Object](s metav1.LabelSelector) (predicate.TypedPredicate[T], error) {
	selector, err := metav1.LabelSelectorAsSelector(&s)
	if err != nil {
		return predicate.TypedFuncs[T]{}, err
	}
	return predicate.NewTypedPredicateFuncs[T](func(o T) bool {
		return selector.Matches(labels.Set(o.GetLabels()))
	}), nil
}
