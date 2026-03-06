// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package namespace

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/gardener/gardener/pkg/controllerutils"
	gardenerutils "github.com/gardener/gardener/pkg/utils/gardener"
)

// Reconciler reconciles namespace objects and replicates imagePullSecrets to them.
type Reconciler struct {
	SeedClient   client.Client
	GardenClient client.Reader
	SeedName     string
}

// Reconcile reconciles namespace objects and replicates imagePullSecrets to them.
func (r *Reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log := logf.FromContext(ctx)

	ctx, cancel := context.WithTimeout(ctx, controllerutils.DefaultReconciliationTimeout)
	defer cancel()

	namespace := &corev1.Namespace{}
	if err := r.SeedClient.Get(ctx, request.NamespacedName, namespace); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("Replicating imagePullSecrets to namespace")

	// Use the existing utility function to replicate secrets
	if err := gardenerutils.ReplicateImagePullSecretsToNamespace(
		ctx,
		log,
		r.GardenClient,
		r.SeedClient,
		gardenerutils.ComputeGardenNamespace(r.SeedName), // Source: seed-<name> in garden cluster
		namespace.Name, // Target: the namespace in seed cluster
	); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}
