// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package bastion

import (
	"context"
	"reflect"

	"github.com/go-logr/logr"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/gardener/gardener/pkg/apis/operations"
	operationsv1alpha1 "github.com/gardener/gardener/pkg/apis/operations/v1alpha1"
	"github.com/gardener/gardener/pkg/controllerutils/mapper"
	predicateutils "github.com/gardener/gardener/pkg/controllerutils/predicate"
)

// ControllerName is the name of this controller.
const ControllerName = "bastion"

// AddToManager adds Reconciler to the given manager.
func (r *Reconciler) AddToManager(ctx context.Context, mgr manager.Manager) error {
	if r.Client == nil {
		r.Client = mgr.GetClient()
	}
	if r.Clock == nil {
		r.Clock = clock.RealClock{}
	}

	c, err := builder.
		ControllerManagedBy(mgr).
		Named(ControllerName).
		For(&operationsv1alpha1.Bastion{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: ptr.Deref(r.Config.ConcurrentSyncs, 0),
		}).
		Build(r)
	if err != nil {
		return err
	}

	return c.Watch(
		source.Kind(mgr.GetCache(),
			&gardencorev1beta1.Shoot{},
			mapper.TypedEnqueueRequestsFrom[*gardencorev1beta1.Shoot](ctx, mgr.GetCache(), mapper.MapFunc(r.MapShootToBastions), mapper.UpdateWithNew, c.GetLogger()),
			r.ShootPredicate()),
	)
}

// ShootPredicate returns the predicate for Shoot events.
func (r *Reconciler) ShootPredicate() predicate.TypedPredicate[*gardencorev1beta1.Shoot] {
	return predicate.Or[*gardencorev1beta1.Shoot](
		predicateutils.TypedIsDeleting[*gardencorev1beta1.Shoot](),
		predicate.TypedFuncs[*gardencorev1beta1.Shoot]{
			CreateFunc:  func(_ event.TypedCreateEvent[*gardencorev1beta1.Shoot]) bool { return false },
			DeleteFunc:  func(_ event.TypedDeleteEvent[*gardencorev1beta1.Shoot]) bool { return false },
			GenericFunc: func(_ event.TypedGenericEvent[*gardencorev1beta1.Shoot]) bool { return false },
			UpdateFunc: func(e event.TypedUpdateEvent[*gardencorev1beta1.Shoot]) bool {
				if isNil(e.ObjectOld) {
					return false
				}
				if isNil(e.ObjectNew) {
					return false
				}

				if e.ObjectOld.Spec.SeedName == nil {
					return false
				}

				return !apiequality.Semantic.DeepEqual(e.ObjectOld.Spec.SeedName, e.ObjectNew.Spec.SeedName)
			},
		},
	)
}

// MapShootToBastions is a mapper.MapFunc for mapping shoots to referencing Bastions.
func (r *Reconciler) MapShootToBastions(ctx context.Context, log logr.Logger, reader client.Reader, obj client.Object) []reconcile.Request {
	shoot, ok := obj.(*gardencorev1beta1.Shoot)
	if !ok {
		return nil
	}

	bastionList := &operationsv1alpha1.BastionList{}
	if err := reader.List(ctx, bastionList, client.InNamespace(shoot.Namespace), client.MatchingFields{operations.BastionShootName: shoot.Name}); err != nil {
		log.Error(err, "Failed to list Bastions for shoot", "shoot", client.ObjectKeyFromObject(shoot))
		return nil
	}

	return mapper.ObjectListToRequests(bastionList)
}

func isNil(arg any) bool {
	if v := reflect.ValueOf(arg); !v.IsValid() || ((v.Kind() == reflect.Ptr ||
		v.Kind() == reflect.Interface ||
		v.Kind() == reflect.Slice ||
		v.Kind() == reflect.Map ||
		v.Kind() == reflect.Chan ||
		v.Kind() == reflect.Func) && v.IsNil()) {
		return true
	}
	return false
}
