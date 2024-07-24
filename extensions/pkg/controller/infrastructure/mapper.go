// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package infrastructure

import (
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	extensionsv1alpha1 "github.com/gardener/gardener/pkg/apis/extensions/v1alpha1"
	"github.com/gardener/gardener/pkg/controllerutils/mapper"
)

// ClusterToInfrastructureMapper returns a mapper that returns requests for Infrastructures whose
// referenced clusters have been modified.
func ClusterToInfrastructureMapper(mgr manager.Manager, predicates []predicate.TypedPredicate[*extensionsv1alpha1.Infrastructure]) mapper.Mapper {
	return mapper.ClusterToObjectMapper(mgr, func() client.ObjectList { return &extensionsv1alpha1.InfrastructureList{} }, predicates)
}
