// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package activity

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	v1beta1constants "github.com/gardener/gardener/pkg/apis/core/v1beta1/constants"
	"github.com/gardener/gardener/pkg/controllerutils/mapper"
	gardenerutils "github.com/gardener/gardener/pkg/utils/gardener"
)

// ControllerName is the name of this controller.
const ControllerName = "project-activity"

// AddToManager adds Reconciler to the given manager.
func (r *Reconciler) AddToManager(ctx context.Context, mgr manager.Manager) error {
	if r.Client == nil {
		r.Client = mgr.GetClient()
	}
	if r.Clock == nil {
		r.Clock = clock.RealClock{}
	}

	// It's not possible to call builder.Build() without adding atleast one watch, and without this, we can't get the controller logger.
	// Hence, we have to build up the controller manually.
	c, err := controller.New(
		ControllerName,
		mgr,
		controller.Options{
			Reconciler:              r,
			MaxConcurrentReconciles: ptr.Deref(r.Config.ConcurrentSyncs, 0),
		},
	)
	if err != nil {
		return err
	}

	if err := c.Watch(
		source.Kind(mgr.GetCache(),
			&gardencorev1beta1.Shoot{},
			mapper.TypedEnqueueRequestsFrom[*gardencorev1beta1.Shoot](ctx, mgr.GetCache(), mapper.MapFunc(r.MapObjectToProject), mapper.UpdateWithNew, c.GetLogger()),
			r.OnlyNewlyCreatedObjects(),
			predicate.GenerationChangedPredicate{}),
	); err != nil {
		return err
	}

	if err := c.Watch(
		source.Kind(mgr.GetCache(),
			&gardencorev1beta1.BackupEntry{},
			mapper.TypedEnqueueRequestsFrom[*gardencorev1beta1.BackupEntry](ctx, mgr.GetCache(), mapper.MapFunc(r.MapObjectToProject), mapper.UpdateWithNew, c.GetLogger()),
			r.OnlyNewlyCreatedObjects(),
			predicate.GenerationChangedPredicate{}),
	); err != nil {
		return err
	}

	if err := c.Watch(
		source.Kind(mgr.GetCache(),
			&gardencorev1beta1.Quota{},
			mapper.TypedEnqueueRequestsFrom[*gardencorev1beta1.Quota](ctx, mgr.GetCache(), mapper.MapFunc(r.MapObjectToProject), mapper.UpdateWithNew, c.GetLogger()),
			r.OnlyNewlyCreatedObjects(),
			r.NeedsSecretBindingReferenceLabelPredicate()),
	); err != nil {
		return err
	}

	return c.Watch(
		source.Kind(mgr.GetCache(),
			&corev1.Secret{},
			mapper.TypedEnqueueRequestsFrom[*corev1.Secret](ctx, mgr.GetCache(), mapper.MapFunc(r.MapObjectToProject), mapper.UpdateWithNew, c.GetLogger()),
			r.OnlyNewlyCreatedObjects(),
			r.NeedsSecretBindingReferenceLabelPredicate()),
	)
}

// OnlyNewlyCreatedObjects filters for objects which are created less than an hour ago for create events. This can be
// used to prevent unnecessary reconciliations in case of controller restarts.
func (r *Reconciler) OnlyNewlyCreatedObjects() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			objMeta, err := meta.Accessor(e.Object)
			if err != nil {
				return false
			}

			return r.Clock.Now().UTC().Sub(objMeta.GetCreationTimestamp().UTC()) <= time.Hour
		},
	}
}

// NeedsSecretBindingReferenceLabelPredicate returns a predicate which only returns true when the objects have the
// reference.gardener.cloud/secretbinding label.
func (r *Reconciler) NeedsSecretBindingReferenceLabelPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			objMeta, err := meta.Accessor(e.Object)
			if err != nil {
				return false
			}

			_, hasLabel := objMeta.GetLabels()[v1beta1constants.LabelSecretBindingReference]
			return hasLabel
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldObjMeta, err := meta.Accessor(e.ObjectOld)
			if err != nil {
				return false
			}

			objMeta, err := meta.Accessor(e.ObjectNew)
			if err != nil {
				return false
			}

			_, oldObjHasLabel := oldObjMeta.GetLabels()[v1beta1constants.LabelSecretBindingReference]
			_, newObjHasLabel := objMeta.GetLabels()[v1beta1constants.LabelSecretBindingReference]

			return oldObjHasLabel || newObjHasLabel
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			objMeta, err := meta.Accessor(e.Object)
			if err != nil {
				return false
			}

			_, hasLabel := objMeta.GetLabels()[v1beta1constants.LabelSecretBindingReference]
			return hasLabel
		},
	}
}

// MapObjectToProject is a mapper.MapFunc for mapping an object to the Project it belongs to.
func (r *Reconciler) MapObjectToProject(ctx context.Context, log logr.Logger, reader client.Reader, obj client.Object) []reconcile.Request {
	project, err := gardenerutils.ProjectForNamespaceFromReader(ctx, reader, obj.GetNamespace())
	if err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "Failed to get project for namespace", "namespace", obj.GetNamespace())
		}
		return nil
	}

	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: project.Name}}}
}
