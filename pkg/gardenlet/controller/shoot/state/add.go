// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package state

import (
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	v1beta1helper "github.com/gardener/gardener/pkg/apis/core/v1beta1/helper"
)

// ControllerName is the name of this controller.
const ControllerName = "shoot-state"

// AddToManager adds Reconciler to the given manager.
func (r *Reconciler) AddToManager(mgr manager.Manager, gardenCluster, seedCluster cluster.Cluster) error {
	if r.GardenClient == nil {
		r.GardenClient = gardenCluster.GetClient()
	}
	if r.SeedClient == nil {
		r.SeedClient = seedCluster.GetClient()
	}
	if r.Clock == nil {
		r.Clock = clock.RealClock{}
	}

	predicates := []predicate.TypedPredicate[*gardencorev1beta1.Shoot]{
		r.SeedNameChangedPredicate(),
	}

	return builder.
		ControllerManagedBy(mgr).
		Named(ControllerName).
		WithOptions(controller.Options{MaxConcurrentReconciles: *r.Config.ConcurrentSyncs}).
		WatchesRawSource(
			source.Kind(gardenCluster.GetCache(),
				&gardencorev1beta1.Shoot{},
				&handler.TypedEnqueueRequestForObject[*gardencorev1beta1.Shoot]{},
				predicates...),
		).
		Complete(r)
}

// SeedNameChangedPredicate returns a predicate which returns true for all events except updates - here it only returns
// true when the seed name changed.
func (r *Reconciler) SeedNameChangedPredicate() predicate.TypedPredicate[*gardencorev1beta1.Shoot] {
	return predicate.TypedFuncs[*gardencorev1beta1.Shoot]{
		UpdateFunc: func(e event.TypedUpdateEvent[*gardencorev1beta1.Shoot]) bool {
			if v1beta1helper.IsNil(e.ObjectNew) {
				return false
			}

			if v1beta1helper.IsNil(e.ObjectOld) {
				return false
			}

			return ptr.Deref(e.ObjectNew.Spec.SeedName, "") != ptr.Deref(e.ObjectOld.Spec.SeedName, "")
		},
	}
}
