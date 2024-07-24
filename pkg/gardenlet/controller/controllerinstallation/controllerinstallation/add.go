// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package controllerinstallation

import (
	"context"
	"reflect"

	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	gardencorev1 "github.com/gardener/gardener/pkg/apis/core/v1"
	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	v1beta1helper "github.com/gardener/gardener/pkg/apis/core/v1beta1/helper"
	"github.com/gardener/gardener/pkg/utils/oci"
)

// ControllerName is the name of this controller.
const ControllerName = "controllerinstallation"

// AddToManager adds Reconciler to the given manager.
func (r *Reconciler) AddToManager(ctx context.Context, mgr manager.Manager, gardenCluster cluster.Cluster) error {
	if r.GardenClient == nil {
		r.GardenClient = gardenCluster.GetClient()
	}
	if r.GardenConfig == nil {
		r.GardenConfig = gardenCluster.GetConfig()
	}
	if r.Clock == nil {
		r.Clock = clock.RealClock{}
	}
	if r.HelmRegistry == nil {
		helmRegisty, err := oci.NewHelmRegistry()
		if err != nil {
			return err
		}
		r.HelmRegistry = helmRegisty
	}

	predicates := []predicate.TypedPredicate[*gardencorev1beta1.ControllerInstallation]{
		r.ControllerInstallationPredicate(),
		r.HelmTypePredicate(ctx, gardenCluster.GetClient()),
	}

	return builder.
		ControllerManagedBy(mgr).
		Named(ControllerName).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: ptr.Deref(r.Config.Controllers.ControllerInstallation.ConcurrentSyncs, 0),
		}).
		WatchesRawSource(
			source.Kind(gardenCluster.GetCache(),
				&gardencorev1beta1.ControllerInstallation{},
				&handler.TypedEnqueueRequestForObject[*gardencorev1beta1.ControllerInstallation]{},
				predicates...),
		).
		Complete(r)
}

// ControllerInstallationPredicate returns a predicate that evaluates to true in all cases except for 'Update' events.
// Here, it only returns true if the references change or the deletion timestamp gets set.
func (r *Reconciler) ControllerInstallationPredicate() predicate.TypedPredicate[*gardencorev1beta1.ControllerInstallation] {
	return predicate.TypedFuncs[*gardencorev1beta1.ControllerInstallation]{
		UpdateFunc: func(e event.TypedUpdateEvent[*gardencorev1beta1.ControllerInstallation]) bool {
			// enqueue on periodic cache resyncs
			if e.ObjectOld.GetResourceVersion() == e.ObjectNew.GetResourceVersion() {
				return true
			}

			controllerInstallation := e.ObjectNew
			if v1beta1helper.IsNil(controllerInstallation) {
				return false
			}

			oldControllerInstallation := e.ObjectOld
			if v1beta1helper.IsNil(oldControllerInstallation) {
				return false
			}

			return (oldControllerInstallation.DeletionTimestamp == nil && controllerInstallation.DeletionTimestamp != nil) ||
				!reflect.DeepEqual(oldControllerInstallation.Spec.DeploymentRef, controllerInstallation.Spec.DeploymentRef) ||
				oldControllerInstallation.Spec.RegistrationRef.ResourceVersion != controllerInstallation.Spec.RegistrationRef.ResourceVersion ||
				oldControllerInstallation.Spec.SeedRef.ResourceVersion != controllerInstallation.Spec.SeedRef.ResourceVersion
		},
	}
}

// HelmTypePredicate is a predicate which checks whether the ControllerDeployment referenced in the
// ControllerInstallation has .type=helm.
func (r *Reconciler) HelmTypePredicate(ctx context.Context, reader client.Reader) predicate.TypedPredicate[*gardencorev1beta1.ControllerInstallation] {
	return &helmTypePredicate{
		ctx:    ctx,
		reader: reader,
	}
}

type helmTypePredicate struct {
	ctx    context.Context
	reader client.Reader
}

func (p *helmTypePredicate) Create(e event.TypedCreateEvent[*gardencorev1beta1.ControllerInstallation]) bool {
	return p.isResponsible(e.Object)
}
func (p *helmTypePredicate) Update(e event.TypedUpdateEvent[*gardencorev1beta1.ControllerInstallation]) bool {
	return p.isResponsible(e.ObjectNew)
}
func (p *helmTypePredicate) Delete(e event.TypedDeleteEvent[*gardencorev1beta1.ControllerInstallation]) bool {
	return p.isResponsible(e.Object)
}
func (p *helmTypePredicate) Generic(e event.TypedGenericEvent[*gardencorev1beta1.ControllerInstallation]) bool {
	return p.isResponsible(e.Object)
}

func (p *helmTypePredicate) isResponsible(obj client.Object) bool {
	controllerInstallation, ok := obj.(*gardencorev1beta1.ControllerInstallation)
	if !ok {
		return false
	}

	if deploymentName := controllerInstallation.Spec.DeploymentRef; deploymentName != nil {
		controllerDeployment := &gardencorev1.ControllerDeployment{}
		if err := p.reader.Get(p.ctx, client.ObjectKey{Name: deploymentName.Name}, controllerDeployment); err != nil {
			return false
		}
		return controllerDeployment.Helm != nil
	}

	return false
}
