// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package mapper

import (
	"context"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	extensionsv1alpha1 "github.com/gardener/gardener/pkg/apis/extensions/v1alpha1"
	predicateutils "github.com/gardener/gardener/pkg/controllerutils/predicate"
)

type clusterToObjectMapper[T client.Object] struct {
	reader         cache.Cache
	newObjListFunc func() client.ObjectList
	predicates     []predicate.TypedPredicate[T]
}

func (m *clusterToObjectMapper[T]) Map(ctx context.Context, _ logr.Logger, reader client.Reader, obj client.Object) []reconcile.Request {
	cluster, ok := obj.(*extensionsv1alpha1.Cluster)
	if !ok {
		return nil
	}

	objList := m.newObjListFunc()
	if err := reader.List(ctx, objList, client.InNamespace(cluster.Name)); err != nil {
		return nil
	}

	return ObjectListToRequests(objList, func(o client.Object) bool {
		return predicateutils.TypedEvalGeneric[T](o.(T), m.predicates...)
	})
}

// ClusterToObjectMapper returns a mapper that returns requests for objects whose
// referenced clusters have been modified.
func ClusterToObjectMapper[T client.Object](mgr manager.Manager, newObjListFunc func() client.ObjectList, predicates []predicate.TypedPredicate[T]) Mapper {
	return &clusterToObjectMapper[T]{
		reader:         mgr.GetCache(),
		newObjListFunc: newObjListFunc,
		predicates:     predicates,
	}
}
