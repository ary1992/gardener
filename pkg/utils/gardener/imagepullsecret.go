// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package gardener

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// ReplicateImagePullSecretsToNamespace replicates all secrets with the label gardener.cloud/role=image-pull-secret
// from the source namespace in the garden cluster to the target namespace in the seed cluster.
func ReplicateImagePullSecretsToNamespace(
	ctx context.Context,
	log logr.Logger,
	gardenReader client.Reader,
	seedClient client.Client,
	sourceNamespace string,
	targetNamespace string,
) error {
	// List all secrets with the image-pull-secret label from the source namespace in garden cluster
	secretList := &corev1.SecretList{}
	if err := gardenReader.List(ctx, secretList,
		client.InNamespace(sourceNamespace),
		client.MatchingLabels{"gardener.cloud/role": "image-pull-secret"},
	); err != nil {
		return fmt.Errorf("failed to list imagePullSecrets from namespace %s: %w", sourceNamespace, err)
	}

	if len(secretList.Items) == 0 {
		log.V(1).Info("No imagePullSecrets found to replicate", "sourceNamespace", sourceNamespace)
		return nil
	}

	log.V(1).Info("Found imagePullSecrets to replicate", "count", len(secretList.Items), "sourceNamespace", sourceNamespace, "targetNamespace", targetNamespace)

	// Replicate each secret to the target namespace
	for _, secret := range secretList.Items {
		targetSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secret.Name,
				Namespace: targetNamespace,
			},
		}

		log.V(1).Info("Replicating imagePullSecret to namespace",
			"secret", secret.Name,
			"sourceNamespace", sourceNamespace,
			"targetNamespace", targetNamespace,
		)

		_, err := controllerutil.CreateOrUpdate(ctx, seedClient, targetSecret, func() error {
			// Preserve existing labels and add the image-pull-secret label
			if targetSecret.Labels == nil {
				targetSecret.Labels = make(map[string]string)
			}
			for k, v := range secret.Labels {
				targetSecret.Labels[k] = v
			}

			// Preserve existing annotations
			if len(secret.Annotations) > 0 {
				if targetSecret.Annotations == nil {
					targetSecret.Annotations = make(map[string]string)
				}
				for k, v := range secret.Annotations {
					targetSecret.Annotations[k] = v
				}
			}

			// Copy secret type and data
			targetSecret.Type = secret.Type
			targetSecret.Data = secret.Data

			return nil
		})

		if err != nil {
			if apierrors.IsConflict(err) {
				log.V(1).Info("Conflict while replicating secret, will retry",
					"secret", secret.Name,
					"targetNamespace", targetNamespace,
				)
				continue
			}
			return fmt.Errorf("failed to replicate secret %s to namespace %s: %w", secret.Name, targetNamespace, err)
		}
	}

	return nil
}

// ReplicateImagePullSecretsToAllNamespaces replicates all secrets with the label gardener.cloud/role=image-pull-secret
// from the source namespace in the garden cluster to all namespaces in the seed cluster.
// This ensures that pods in any namespace can pull images from private registries.
func ReplicateImagePullSecretsToAllNamespaces(
	ctx context.Context,
	log logr.Logger,
	gardenReader client.Reader,
	seedClient client.Client,
	sourceNamespace string,
) error {
	// List all secrets with the image-pull-secret label from the source namespace in garden cluster
	secretList := &corev1.SecretList{}
	if err := gardenReader.List(ctx, secretList,
		client.InNamespace(sourceNamespace),
		client.MatchingLabels{"gardener.cloud/role": "image-pull-secret"},
	); err != nil {
		return fmt.Errorf("failed to list imagePullSecrets from namespace %s: %w", sourceNamespace, err)
	}

	if len(secretList.Items) == 0 {
		log.Info("No imagePullSecrets found to replicate", "sourceNamespace", sourceNamespace)
		return nil
	}

	log.Info("Found imagePullSecrets to replicate", "count", len(secretList.Items), "sourceNamespace", sourceNamespace)

	// List all namespaces in the seed cluster
	namespaceList := &corev1.NamespaceList{}
	if err := seedClient.List(ctx, namespaceList); err != nil {
		return fmt.Errorf("failed to list namespaces in seed cluster: %w", err)
	}

	log.Info("Found namespaces in seed cluster", "count", len(namespaceList.Items))

	// Replicate each secret to all namespaces
	for _, secret := range secretList.Items {
		for _, namespace := range namespaceList.Items {
			targetNamespace := namespace.Name

			if err := ReplicateImagePullSecretsToNamespace(ctx, log, gardenReader, seedClient, sourceNamespace, targetNamespace); err != nil {
				return err
			}
		}

		log.Info("Successfully replicated imagePullSecret to all namespaces",
			"secret", secret.Name,
			"namespaceCount", len(namespaceList.Items),
		)
	}

	return nil
}
