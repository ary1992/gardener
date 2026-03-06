// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package imagevector

import (
	"context"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1beta1constants "github.com/gardener/gardener/pkg/apis/core/v1beta1/constants"
	"github.com/gardener/gardener/pkg/controllerutils"
)

// SyncImagePullSecretsToNamespace syncs imagePullSecrets referenced in the image vector
// from the garden cluster (seed's namespace) to the target namespace in the seed cluster.
// This is useful for extension controllers that need to deploy workloads requiring private registry access.
//
// Usage in extension controllers:
//   - Call this function in your actuator's Reconcile method
//   - Provide the shoot namespace as targetNamespace
//   - ImagePullSecrets will be synced from seed-<seedname> namespace in garden to shoot namespace in seed
//
// Parameters:
//   - ctx: Context for API calls
//   - log: Logger for status messages
//   - gardenReader: Reader for garden cluster
//   - seedClient: Client for seed cluster
//   - imageVector: The image vector containing imagePullSecretName references
//   - seedName: Name of the seed (used to compute garden namespace)
//   - targetNamespace: Namespace in seed cluster where secrets should be synced (typically shoot namespace)
func SyncImagePullSecretsToNamespace(
	ctx context.Context,
	log logr.Logger,
	gardenReader client.Reader,
	seedClient client.Client,
	imagePullSecretName *string,
	seedName string,
	targetNamespace string,
) {
	if imagePullSecretName == nil || *imagePullSecretName == "" {
		log.V(1).Info("No imagePullSecret configured in image vector, skipping sync")
		return
	}

	secretName := *imagePullSecretName
	// Compute garden namespace: "seed-<seedname>"
	gardenNamespace := "seed-" + seedName

	// Fetch secret from garden cluster
	gardenSecret := &corev1.Secret{}
	if err := gardenReader.Get(ctx, client.ObjectKey{
		Namespace: gardenNamespace,
		Name:      secretName,
	}, gardenSecret); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("ImagePullSecret not found in garden namespace, skipping", "secret", secretName, "namespace", gardenNamespace)
		} else {
			log.Error(err, "Failed to get imagePullSecret from garden namespace, skipping", "secret", secretName, "namespace", gardenNamespace)
		}
		return
	}

	// Create or update secret in seed cluster target namespace
	seedSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: targetNamespace,
		},
	}

	if _, err := controllerutils.GetAndCreateOrMergePatch(ctx, seedClient, seedSecret, func() error {
		seedSecret.Type = gardenSecret.Type
		seedSecret.Data = gardenSecret.Data
		// Add label to track this as an imagePullSecret (must match what DeployImagePullSecretToShoot expects)
		metav1.SetMetaDataLabel(&seedSecret.ObjectMeta, v1beta1constants.GardenRole, "image-pull-secret")
		return nil
	}); err != nil {
		log.Error(err, "Failed to sync imagePullSecret to target namespace", "secret", secretName, "targetNamespace", targetNamespace)
		return
	}

	log.V(1).Info("Successfully synced imagePullSecret", "secret", secretName, "from", gardenNamespace, "to", targetNamespace)
}
