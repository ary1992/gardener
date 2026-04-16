// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package imagepullsecret

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/gardener/gardener/imagevector"
	v1beta1constants "github.com/gardener/gardener/pkg/apis/core/v1beta1/constants"
	"github.com/gardener/gardener/pkg/client/kubernetes"
	"github.com/gardener/gardener/pkg/controllerutils"
	"github.com/gardener/gardener/pkg/utils"
	gardenerutils "github.com/gardener/gardener/pkg/utils/gardener"
	"github.com/gardener/gardener/pkg/utils/managedresources"
)

const managedResourceNameImagePullSecret = "image-pull-secret"

// Reconciler watches image pull secrets in the seed's garden namespace and propagates them to all
// extension namespaces and shoot control plane namespaces. For shoot namespaces it also updates
// the ManagedResource so gardener-resource-manager syncs the new credentials to the shoot cluster
// without requiring a full shoot reconciliation.
type Reconciler struct {
	GardenClient    client.Client
	SeedClient      client.Client
	GardenNamespace string
	SeedName        string
}

// Reconcile propagates an image pull secret to all target namespaces in the seed cluster.
func (r *Reconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log := logf.FromContext(ctx)

	// Sync the secret from the garden cluster's seed namespace into the seed cluster's garden
	// namespace. This ensures rotation is picked up automatically whenever the
	// gardener-controller-manager seed-secrets controller updates the secret in the garden cluster.
	if err := r.syncFromGarden(ctx, request.Name); err != nil {
		return reconcile.Result{}, err
	}

	secret := &corev1.Secret{}
	if err := r.SeedClient.Get(ctx, request.NamespacedName, secret); err != nil {
		if apierrors.IsNotFound(err) {
			log.V(1).Info("Secret is gone, stop reconciling")
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("error retrieving secret from store: %w", err)
	}

	// List all extension namespaces (gardener.cloud/role=extension)
	extensionNamespaces := &corev1.NamespaceList{}
	if err := r.SeedClient.List(ctx, extensionNamespaces, client.MatchingLabels{
		v1beta1constants.GardenRole: v1beta1constants.GardenRoleExtension,
	}); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to list extension namespaces: %w", err)
	}

	// List all shoot control plane namespaces (gardener.cloud/role=shoot)
	shootNamespaces := &corev1.NamespaceList{}
	if err := r.SeedClient.List(ctx, shootNamespaces, client.MatchingLabels{
		v1beta1constants.GardenRole: v1beta1constants.GardenRoleShoot,
	}); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to list shoot control plane namespaces: %w", err)
	}

	var targetNamespaces []string
	for _, ns := range extensionNamespaces.Items {
		targetNamespaces = append(targetNamespaces, ns.Name)
	}
	var shootNamespaceNames []string
	for _, ns := range shootNamespaces.Items {
		targetNamespaces = append(targetNamespaces, ns.Name)
		shootNamespaceNames = append(shootNamespaceNames, ns.Name)
	}

	log.Info("Propagating image pull secret", "secret", secret.Name, "targetNamespaces", len(targetNamespaces))

	for _, namespace := range targetNamespaces {
		targetSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secret.Name,
				Namespace: namespace,
			},
		}

		if _, err := controllerutils.GetAndCreateOrMergePatch(ctx, r.SeedClient, targetSecret, func() error {
			targetSecret.Type = secret.Type
			targetSecret.Data = secret.Data
			targetSecret.Labels = utils.MergeStringMaps(map[string]string{
				v1beta1constants.GardenRole: v1beta1constants.GardenRoleImagePullSecret,
			}, secret.Labels)
			return nil
		}); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to propagate image pull secret %q to namespace %q: %w", secret.Name, namespace, err)
		}
	}

	// Update the ManagedResource in each shoot CP namespace so gardener-resource-manager
	// syncs the new credentials to the shoot cluster. Only update if the ManagedResource
	// already exists — initial creation is handled by the shoot reconciler.
	for _, namespace := range shootNamespaceNames {
		if err := r.updateShootManagedResource(ctx, namespace); err != nil {
			return reconcile.Result{}, err
		}
	}

	return reconcile.Result{}, nil
}

// syncFromGarden reads the named secret from the garden cluster's seed-specific namespace
// (maintained by the gardener-controller-manager seed-secrets controller) and upserts it
// into the seed cluster's garden namespace. This ensures that credential rotations performed
// in the garden cluster are automatically reflected in the seed cluster without requiring a
// full seed reconciliation.
func (r *Reconciler) syncFromGarden(ctx context.Context, secretName string) error {
	gardenSeedNamespace := gardenerutils.ComputeGardenNamespace(r.SeedName)
	gardenSecret := &corev1.Secret{}
	if err := r.GardenClient.Get(ctx, client.ObjectKey{Namespace: gardenSeedNamespace, Name: secretName}, gardenSecret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get image pull secret %q from garden cluster namespace %q: %w", secretName, gardenSeedNamespace, err)
	}

	seedSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: r.GardenNamespace,
		},
	}
	_, err := controllerutils.GetAndCreateOrMergePatch(ctx, r.SeedClient, seedSecret, func() error {
		seedSecret.Type = gardenSecret.Type
		seedSecret.Data = gardenSecret.Data
		seedSecret.Labels = utils.MergeStringMaps(gardenSecret.Labels, map[string]string{
			v1beta1constants.GardenRole: v1beta1constants.GardenRoleImagePullSecret,
		})
		return nil
	})
	return err
}

// updateShootManagedResource creates or updates the image-pull-secret ManagedResource in the
// given shoot CP namespace, causing gardener-resource-manager to apply the secrets to the shoot cluster.
func (r *Reconciler) updateShootManagedResource(ctx context.Context, namespace string) error {
	var secretNames []string
	for _, cred := range imagevector.AllContainerImagePullCredentials() {
		if cred.Type == "StaticSecret" && cred.SecretName != nil {
			secretNames = append(secretNames, *cred.SecretName)
		}
	}
	if len(secretNames) == 0 {
		return nil
	}

	registry := managedresources.NewRegistry(kubernetes.ShootScheme, kubernetes.ShootCodec, kubernetes.ShootSerializer)

	for _, secretName := range secretNames {
		seedSecret := &corev1.Secret{}
		if err := r.SeedClient.Get(ctx, client.ObjectKey{Name: secretName, Namespace: namespace}, seedSecret); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("failed to get image pull secret %q from namespace %q: %w", secretName, namespace, err)
		}

		shootSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: metav1.NamespaceSystem,
			},
			Type: seedSecret.Type,
			Data: seedSecret.Data,
		}
		if err := registry.Add(shootSecret); err != nil {
			return fmt.Errorf("failed to add secret %q to registry: %w", secretName, err)
		}
	}

	serializedObjects, err := registry.SerializedObjects()
	if err != nil {
		return fmt.Errorf("failed to serialize objects for ManagedResource in namespace %q: %w", namespace, err)
	}

	if len(serializedObjects) == 0 {
		return nil
	}

	return managedresources.CreateForShoot(ctx, r.SeedClient, namespace, managedResourceNameImagePullSecret, managedresources.LabelValueGardener, false, serializedObjects)
}
