// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package care

import (
	"context"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	v1beta1constants "github.com/gardener/gardener/pkg/apis/core/v1beta1/constants"
	v1beta1helper "github.com/gardener/gardener/pkg/apis/core/v1beta1/helper"
	resourcesv1alpha1 "github.com/gardener/gardener/pkg/apis/resources/v1alpha1"
	"github.com/gardener/gardener/pkg/controllerutils/mapper"
	predicateutils "github.com/gardener/gardener/pkg/controllerutils/predicate"
)

// ControllerName is the name of this controller.
const ControllerName = "seed-care"

// AddToManager adds Reconciler to the given manager.
func (r *Reconciler) AddToManager(ctx context.Context, mgr manager.Manager, gardenCluster, seedCluster cluster.Cluster) error {
	if r.GardenClient == nil {
		r.GardenClient = gardenCluster.GetClient()
	}
	if r.SeedClient == nil {
		r.SeedClient = seedCluster.GetClient()
	}
	if r.Clock == nil {
		r.Clock = clock.RealClock{}
	}

	predicates := []predicate.TypedPredicate[*gardencorev1beta1.Seed]{
		predicateutils.HasName[*gardencorev1beta1.Seed](r.SeedName),
		r.SeedPredicate(),
	}

	c, err := builder.
		ControllerManagedBy(mgr).
		Named(ControllerName).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 1,
			// if going into exponential backoff, wait at most the configured sync period
			RateLimiter: workqueue.NewWithMaxWaitRateLimiter(workqueue.DefaultControllerRateLimiter(), r.Config.SyncPeriod.Duration),
		}).
		WatchesRawSource(
			source.Kind(gardenCluster.GetCache(),
				&gardencorev1beta1.Seed{},
				&handler.TypedEnqueueRequestForObject[*gardencorev1beta1.Seed]{},
				predicates...,
			)).Build(r)
	if err != nil {
		return err
	}

	return c.Watch(
		source.Kind(seedCluster.GetCache(),
			&resourcesv1alpha1.ManagedResource{},
			mapper.TypedEnqueueRequestsFrom[*resourcesv1alpha1.ManagedResource](ctx, mgr.GetCache(), mapper.MapFunc(r.MapManagedResourceToSeed), mapper.UpdateWithNew, c.GetLogger()),
			r.IsSystemComponent(),
			predicateutils.ManagedResourceConditionsChanged()),
	)
}

// SeedPredicate is a predicate which returns 'true' for create events, and for update events in case the seed was
// successfully bootstrapped.
func (r *Reconciler) SeedPredicate() predicate.TypedPredicate[*gardencorev1beta1.Seed] {
	return predicate.TypedFuncs[*gardencorev1beta1.Seed]{
		CreateFunc: func(event.TypedCreateEvent[*gardencorev1beta1.Seed]) bool {
			return true
		},
		UpdateFunc: func(e event.TypedUpdateEvent[*gardencorev1beta1.Seed]) bool {
			if v1beta1helper.IsNil(e.ObjectNew) {
				return false
			}

			if v1beta1helper.IsNil(e.ObjectOld) {
				return false
			}

			return predicateutils.ReconciliationFinishedSuccessfully(e.ObjectOld.Status.LastOperation, e.ObjectNew.Status.LastOperation)
		},
		DeleteFunc:  func(event.TypedDeleteEvent[*gardencorev1beta1.Seed]) bool { return false },
		GenericFunc: func(event.TypedGenericEvent[*gardencorev1beta1.Seed]) bool { return false },
	}
}

// IsSystemComponent returns a predicate which evaluates to true in case the gardener.cloud/role=system-component label
// is present.
func (r *Reconciler) IsSystemComponent() predicate.TypedPredicate[*resourcesv1alpha1.ManagedResource] {
	return predicate.NewTypedPredicateFuncs(func(obj *resourcesv1alpha1.ManagedResource) bool {
		return obj.GetLabels()[v1beta1constants.GardenRole] == v1beta1constants.GardenRoleSeedSystemComponent
	})
}

// MapManagedResourceToSeed is a mapper.MapFunc for mapping a ManagedResource to the owning Seed.
func (r *Reconciler) MapManagedResourceToSeed(_ context.Context, _ logr.Logger, _ client.Reader, _ client.Object) []reconcile.Request {
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: r.SeedName}}}
}
