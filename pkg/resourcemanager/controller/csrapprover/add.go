// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package csrapprover

import (
	certificatesv1 "k8s.io/api/certificates/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	predicateutils "github.com/gardener/gardener/pkg/controllerutils/predicate"
)

// ControllerName is the name of the controller.
const ControllerName = "kubelet-csr-approver"

// AddToManager adds Reconciler to the given manager.
func (r *Reconciler) AddToManager(mgr manager.Manager, sourceCluster, targetCluster cluster.Cluster) error {
	if r.SourceClient == nil {
		r.SourceClient = sourceCluster.GetClient()
	}
	if r.TargetClient == nil {
		r.TargetClient = targetCluster.GetClient()
	}

	predicates := []predicate.TypedPredicate[*certificatesv1.CertificateSigningRequest]{
		predicateutils.TypedForEventTypes[*certificatesv1.CertificateSigningRequest](predicateutils.Create, predicateutils.Update),
		predicate.NewTypedPredicateFuncs[*certificatesv1.CertificateSigningRequest](func(obj *certificatesv1.CertificateSigningRequest) bool {
			return obj.Spec.SignerName == certificatesv1.KubeletServingSignerName
		}),
	}

	return builder.
		ControllerManagedBy(mgr).
		Named(ControllerName).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: ptr.Deref(r.Config.ConcurrentSyncs, 0),
		}).
		WatchesRawSource(
			source.Kind(targetCluster.GetCache(),
				&certificatesv1.CertificateSigningRequest{},
				&handler.TypedEnqueueRequestForObject[*certificatesv1.CertificateSigningRequest]{},
				// TODO(ashish): remove this
				// builder.WithPredicates(
				// 	predicateutils.ForEventTypes(predicateutils.Create, predicateutils.Update),
				// 	predicate.NewPredicateFuncs(func(obj client.Object) bool {
				// 		csr, ok := obj.(*certificatesv1.CertificateSigningRequest)
				// 		return ok && csr.Spec.SignerName == certificatesv1.KubeletServingSignerName
				// 	}),
				// )
				predicates...,
			),
		).Complete(r)
}
