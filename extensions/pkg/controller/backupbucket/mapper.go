// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package backupbucket

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	extensionsv1alpha1 "github.com/gardener/gardener/pkg/apis/extensions/v1alpha1"
	"github.com/gardener/gardener/pkg/controllerutils/mapper"
	predicateutils "github.com/gardener/gardener/pkg/controllerutils/predicate"
)

type secretToBackupBucketMapper struct {
	predicates []predicate.TypedPredicate[*extensionsv1alpha1.BackupBucket]
}

func (m *secretToBackupBucketMapper) Map(ctx context.Context, _ logr.Logger, reader client.Reader, obj client.Object) []reconcile.Request {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	backupBucketList := &extensionsv1alpha1.BackupBucketList{}
	if err := reader.List(ctx, backupBucketList); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, backupBucket := range backupBucketList.Items {
		if backupBucket.Spec.SecretRef.Name == obj.GetName() && backupBucket.Spec.SecretRef.Namespace == obj.GetNamespace() {
			if predicateutils.TypedEvalGeneric(&backupBucket, m.predicates...) {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name: backupBucket.Name,
					},
				})
			}
		}
	}
	return requests
}

// SecretToBackupBucketMapper returns a mapper that returns requests for BackupBucket whose
// referenced secrets have been modified.
func SecretToBackupBucketMapper(predicates []predicate.TypedPredicate[*extensionsv1alpha1.BackupBucket]) mapper.Mapper {
	return &secretToBackupBucketMapper{predicates: predicates}
}
