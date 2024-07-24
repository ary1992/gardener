// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package predicate

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	extensionscontroller "github.com/gardener/gardener/extensions/pkg/controller"
	v1beta1constants "github.com/gardener/gardener/pkg/apis/core/v1beta1/constants"
)

// IsInGardenNamespacePredicate is a predicate which returns true when the provided object is in the 'garden' namespace.
var IsInGardenNamespacePredicate = predicate.NewTypedPredicateFuncs[client.Object](func(obj client.Object) bool {
	return obj != nil && obj.GetNamespace() == v1beta1constants.GardenNamespace
})

func IsInGardenNamespace[T client.Object]() predicate.TypedPredicate[T] {
	return predicate.NewTypedPredicateFuncs[T](func(obj T) bool {
		return obj.GetNamespace() == v1beta1constants.GardenNamespace
	})
}

// ShootNotFailedPredicate returns a predicate which returns true when the Shoot's `.status.lastOperation.state` is not
// equals 'Failed'.
func ShootNotFailedPredicate[T client.Object](ctx context.Context, mgr manager.Manager) predicate.TypedPredicate[T] {
	return &shootNotFailedPredicate[T]{
		ctx:    ctx,
		reader: mgr.GetClient(),
	}
}

type shootNotFailedPredicate[T client.Object] struct {
	ctx    context.Context
	reader client.Reader
}

func (p *shootNotFailedPredicate[T]) Create(e event.TypedCreateEvent[T]) bool {
	if isNil(e.Object) {
		return false
	}

	cluster, err := extensionscontroller.GetCluster(p.ctx, p.reader, e.Object.GetNamespace())
	if err != nil {
		logger.Error(err, "Could not check if shoot is failed")
		return false
	}

	return !extensionscontroller.IsFailed(cluster)
}

func (p *shootNotFailedPredicate[T]) Update(e event.TypedUpdateEvent[T]) bool {
	return p.Create(event.TypedCreateEvent[T]{Object: e.ObjectNew})
}

func (p *shootNotFailedPredicate[T]) Delete(_ event.TypedDeleteEvent[T]) bool { return false }

func (p *shootNotFailedPredicate[T]) Generic(_ event.TypedGenericEvent[T]) bool { return false }
