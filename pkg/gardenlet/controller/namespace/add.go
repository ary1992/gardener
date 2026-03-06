// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package namespace

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/gardener/gardener/pkg/controllerutils"
	predicateutils "github.com/gardener/gardener/pkg/controllerutils/predicate"
)

// ControllerName is the name of the controller.
const ControllerName = "namespace-imagepullsecret"

// AddToManager adds the namespace controller to the given manager.
func (r *Reconciler) AddToManager(
	_ context.Context,
	mgr manager.Manager,
	gardenClient client.Reader,
	seedCluster cluster.Cluster,
	_ string,
	seedName string,
) error {
	reconciler := &Reconciler{
		SeedClient:   seedCluster.GetClient(),
		GardenClient: gardenClient,
		SeedName:     seedName,
	}

	return builder.
		ControllerManagedBy(mgr).
		Named(ControllerName).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 0,
			ReconciliationTimeout:   controllerutils.DefaultReconciliationTimeout,
		}).
		WatchesRawSource(source.Kind[client.Object](
			seedCluster.GetCache(),
			&corev1.Namespace{},
			&handler.EnqueueRequestForObject{},
			predicateutils.ForEventTypes(predicateutils.Create),
		)).
		Complete(reconciler)
}
