// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package care

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	v1beta1helper "github.com/gardener/gardener/pkg/apis/core/v1beta1/helper"
	predicateutils "github.com/gardener/gardener/pkg/controllerutils/predicate"
	"github.com/gardener/gardener/pkg/utils"
)

// ControllerName is the name of this controller.
const ControllerName = "shoot-care"

// AddToManager adds Reconciler to the given manager.
func (r *Reconciler) AddToManager(mgr manager.Manager, gardenCluster cluster.Cluster) error {
	if r.GardenClient == nil {
		r.GardenClient = gardenCluster.GetClient()
	}
	if r.Clock == nil {
		r.Clock = clock.RealClock{}
	}

	predicates := []predicate.TypedPredicate[*gardencorev1beta1.Shoot]{r.ShootPredicate()}

	return builder.
		ControllerManagedBy(mgr).
		Named(ControllerName).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: ptr.Deref(r.Config.Controllers.ShootCare.ConcurrentSyncs, 0),
			// if going into exponential backoff, wait at most the configured sync period
			RateLimiter: workqueue.NewWithMaxWaitRateLimiter(workqueue.DefaultControllerRateLimiter(), r.Config.Controllers.ShootCare.SyncPeriod.Duration),
		}).
		WatchesRawSource(
			source.Kind(gardenCluster.GetCache(),
				&gardencorev1beta1.Shoot{},
				r.EventHandler(),
				predicates...),
		).
		Complete(r)
}

// RandomDurationWithMetaDuration is an alias for utils.RandomDurationWithMetaDuration.
var RandomDurationWithMetaDuration = utils.RandomDurationWithMetaDuration

// EventHandler returns a handler for Shoot events.
func (r *Reconciler) EventHandler() handler.TypedEventHandler[*gardencorev1beta1.Shoot] {
	return &handler.TypedFuncs[*gardencorev1beta1.Shoot]{
		CreateFunc: func(_ context.Context, e event.TypedCreateEvent[*gardencorev1beta1.Shoot], q workqueue.RateLimitingInterface) {
			shoot := e.Object
			if v1beta1helper.IsNil(shoot) {
				return
			}

			req := reconcile.Request{NamespacedName: types.NamespacedName{
				Name:      e.Object.GetName(),
				Namespace: e.Object.GetNamespace(),
			}}

			if shoot.Generation == shoot.Status.ObservedGeneration {
				// spread shoot health checks across sync period to avoid checking on all Shoots roughly at the same
				// time after startup of the gardenlet
				q.AddAfter(req, RandomDurationWithMetaDuration(r.Config.Controllers.ShootCare.SyncPeriod))
				return
			}

			// don't add random duration for enqueueing new Shoots which have never been health checked yet
			q.Add(req)
		},
		UpdateFunc: func(_ context.Context, e event.TypedUpdateEvent[*gardencorev1beta1.Shoot], q workqueue.RateLimitingInterface) {
			q.Add(reconcile.Request{NamespacedName: types.NamespacedName{
				Name:      e.ObjectNew.GetName(),
				Namespace: e.ObjectNew.GetNamespace(),
			}})
		},
	}
}

// ShootPredicate is a predicate which returns 'true' for create events, and for update events in case the shoot was
// successfully reconciled.
func (r *Reconciler) ShootPredicate() predicate.TypedPredicate[*gardencorev1beta1.Shoot] {
	return predicate.TypedFuncs[*gardencorev1beta1.Shoot]{
		CreateFunc: func(event.TypedCreateEvent[*gardencorev1beta1.Shoot]) bool {
			return true
		},
		UpdateFunc: func(e event.TypedUpdateEvent[*gardencorev1beta1.Shoot]) bool {
			shoot := e.ObjectNew
			if v1beta1helper.IsNil(e.ObjectNew) {
				return false
			}

			oldShoot := e.ObjectOld
			if v1beta1helper.IsNil(e.ObjectOld) {
				return false
			}

			// re-evaluate shoot health status right after a reconciliation operation has succeeded
			return predicateutils.ReconciliationFinishedSuccessfully(oldShoot.Status.LastOperation, shoot.Status.LastOperation) || seedGotAssigned(oldShoot, shoot)
		},
		DeleteFunc:  func(event.TypedDeleteEvent[*gardencorev1beta1.Shoot]) bool { return false },
		GenericFunc: func(event.TypedGenericEvent[*gardencorev1beta1.Shoot]) bool { return false },
	}
}

func seedGotAssigned(oldShoot, newShoot *gardencorev1beta1.Shoot) bool {
	return oldShoot.Spec.SeedName == nil && newShoot.Spec.SeedName != nil
}
