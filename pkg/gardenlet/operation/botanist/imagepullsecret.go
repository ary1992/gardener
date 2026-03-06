// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package botanist

import (
	"context"

	"github.com/gardener/gardener/imagevector"
	"github.com/gardener/gardener/pkg/client/kubernetes"
	imagevectorutils "github.com/gardener/gardener/pkg/utils/imagevector"
	"github.com/gardener/gardener/pkg/utils/managedresources"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ReplicateImagePullSecretToSeed replicates the imagePullSecret from the garden cluster's
// seed namespace to the seed cluster's shoot namespace (Stage 2).
// This ensures that components deployed in the shoot namespace can pull images from private registries.
func (b *Botanist) ReplicateImagePullSecretToSeed(ctx context.Context) error {
	imagePullSecretName := imagevector.ContainerImagePullSecretName()
	if imagePullSecretName == nil {
		b.Logger.V(1).Info("No imagePullSecret configured in image vector, skipping replication")
		return nil
	}

	imagevectorutils.SyncImagePullSecretsToNamespace(
		ctx,
		b.Logger,
		b.GardenClient,                // Garden cluster reader
		b.SeedClientSet.Client(),      // Seed cluster client
		imagePullSecretName,           // Secret name from image vector
		b.Seed.GetInfo().Name,         // Seed name (to compute garden namespace)
		b.Shoot.ControlPlaneNamespace, // Target namespace in seed (shoot namespace)
	)

	return nil
}

func (b *Botanist) DeployImagePullSecretToShoot(ctx context.Context) error {
	// List secrets with the image-pull-secret label from shoot namespace (seed cluster)
	secretList := &corev1.SecretList{}
	if err := b.SeedClientSet.Client().List(ctx, secretList,
		client.InNamespace(b.Shoot.ControlPlaneNamespace),
		client.MatchingLabels{"gardener.cloud/role": "image-pull-secret"},
	); err != nil {
		return err
	}

	// If no imagePullSecrets found, skip
	if len(secretList.Items) == 0 {
		b.Logger.V(1).Info("No imagePullSecrets found in shoot control plane namespace, skipping deployment")
		return nil
	}

	// Prepare secrets for kube-system
	var shootSecrets []client.Object
	for _, secret := range secretList.Items {
		shootSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secret.Name,
				Namespace: metav1.NamespaceSystem, // kube-system
				Labels:    secret.Labels,
			},
			Type: secret.Type,
			Data: secret.Data,
		}
		shootSecrets = append(shootSecrets, shootSecret)
	}

	// Serialize and create ManagedResource
	registry := managedresources.NewRegistry(kubernetes.ShootScheme, kubernetes.ShootCodec, kubernetes.ShootSerializer)
	data, err := registry.AddAllAndSerialize(shootSecrets...)
	if err != nil {
		return err
	}

	return managedresources.CreateForShoot(ctx, b.SeedClientSet.Client(), b.Shoot.ControlPlaneNamespace,
		"shoot-core-imagepullsecret", managedresources.LabelValueGardener, false, data)
}
