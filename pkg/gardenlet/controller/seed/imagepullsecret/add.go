// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package imagepullsecret

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	v1beta1constants "github.com/gardener/gardener/pkg/apis/core/v1beta1/constants"
	predicateutils "github.com/gardener/gardener/pkg/controllerutils/predicate"
	gardenerutils "github.com/gardener/gardener/pkg/utils/gardener"
)

// ControllerName is the name of this controller.
const ControllerName = "seed-image-pull-secret"

// AddToManager adds Reconciler to the given manager.
func (r *Reconciler) AddToManager(mgr manager.Manager, gardenCluster cluster.Cluster, seedCluster cluster.Cluster) error {
	if r.GardenClient == nil {
		r.GardenClient = gardenCluster.GetClient()
	}
	if r.SeedClient == nil {
		r.SeedClient = seedCluster.GetClient()
	}

	gardenSeedNamespace := gardenerutils.ComputeGardenNamespace(r.SeedName)

	return builder.
		ControllerManagedBy(mgr).
		Named(ControllerName).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 1,
		}).
		// Watch image pull secrets in the seed cluster's garden namespace.
		WatchesRawSource(source.Kind[client.Object](
			seedCluster.GetCache(),
			&corev1.Secret{},
			&handler.EnqueueRequestForObject{},
			predicate.NewPredicateFuncs(func(obj client.Object) bool {
				return obj.GetNamespace() == r.GardenNamespace &&
					obj.GetLabels()[v1beta1constants.GardenRole] == v1beta1constants.GardenRoleImagePullSecret
			}),
			predicateutils.ForEventTypes(predicateutils.Create, predicateutils.Update),
		)).
		// Watch image pull secrets in the garden cluster's seed-specific namespace.
		// The gardener-controller-manager seed-secrets controller keeps this namespace in sync
		// with the garden namespace, so any rotation there triggers reconciliation here.
		WatchesRawSource(source.Kind[client.Object](
			gardenCluster.GetCache(),
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
				return []reconcile.Request{{
					NamespacedName: types.NamespacedName{
						Name:      obj.GetName(),
						Namespace: r.GardenNamespace,
					},
				}}
			}),
			predicate.NewPredicateFuncs(func(obj client.Object) bool {
				return obj.GetNamespace() == gardenSeedNamespace &&
					obj.GetLabels()[v1beta1constants.GardenRole] == v1beta1constants.GardenRoleImagePullSecret
			}),
			predicateutils.ForEventTypes(predicateutils.Create, predicateutils.Update),
		)).
		// Watch for Namespace creation to propagate existing secrets to new namespaces.
		WatchesRawSource(source.Kind[client.Object](
			seedCluster.GetCache(),
			&corev1.Namespace{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				secretList := &corev1.SecretList{}
				if err := r.SeedClient.List(ctx, secretList,
					client.InNamespace(r.GardenNamespace),
					client.MatchingLabels{v1beta1constants.GardenRole: v1beta1constants.GardenRoleImagePullSecret},
				); err != nil {
					return nil
				}

				var requests []reconcile.Request
				for _, secret := range secretList.Items {
					requests = append(requests, reconcile.Request{
						NamespacedName: types.NamespacedName{
							Name:      secret.Name,
							Namespace: secret.Namespace,
						},
					})
				}
				return requests
			}),
			predicate.NewPredicateFuncs(func(obj client.Object) bool {
				role := obj.GetLabels()[v1beta1constants.GardenRole]
				return role == v1beta1constants.GardenRoleExtension || role == v1beta1constants.GardenRoleShoot
			}),
			predicateutils.ForEventTypes(predicateutils.Create),
		)).
		Complete(r)
}
