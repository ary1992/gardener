// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package predicate

import (
	"reflect"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/gardener/gardener/pkg/api/extensions"
	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	v1beta1constants "github.com/gardener/gardener/pkg/apis/core/v1beta1/constants"
	v1beta1helper "github.com/gardener/gardener/pkg/apis/core/v1beta1/helper"
	resourcesv1alpha1 "github.com/gardener/gardener/pkg/apis/resources/v1alpha1"
	gardenerutils "github.com/gardener/gardener/pkg/utils/gardener"
)

// TypedIsDeleting is a predicate for objects having a deletion timestamp.
func TypedIsDeleting[T client.Object]() predicate.TypedPredicate[T] {
	return predicate.NewTypedPredicateFuncs(func(obj T) bool {
		return obj.GetDeletionTimestamp() != nil
	})
}

// IsDeleting is a predicate for objects having a deletion timestamp.
func IsDeleting() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetDeletionTimestamp() != nil
	})
}

// HasName returns a predicate which returns true when the object has the provided name.
func HasName[T client.Object](name string) predicate.TypedPredicate[T] {
	return predicate.NewTypedPredicateFuncs(func(obj T) bool {
		return obj.GetName() == name
	})
}

// EventType is an alias for byte.
type EventType byte

const (
	// Create is a constant for an event of type 'create'.
	Create EventType = iota
	// Update is a constant for an event of type 'update'.
	Update
	// Delete is a constant for an event of type 'delete'.
	Delete
	// Generic is a constant for an event of type 'generic'.
	Generic
)

// ForEventTypes is a predicate which returns true only for the provided event types.
func TypedForEventTypes[T client.Object](events ...EventType) predicate.TypedPredicate[T] {
	has := func(event EventType) bool {
		for _, e := range events {
			if e == event {
				return true
			}
		}
		return false
	}

	return predicate.TypedFuncs[T]{
		CreateFunc:  func(_ event.TypedCreateEvent[T]) bool { return has(Create) },
		UpdateFunc:  func(_ event.TypedUpdateEvent[T]) bool { return has(Update) },
		DeleteFunc:  func(_ event.TypedDeleteEvent[T]) bool { return has(Delete) },
		GenericFunc: func(_ event.TypedGenericEvent[T]) bool { return has(Generic) },
	}
}

// ForEventTypes is a predicate which returns true only for the provided event types.
func ForEventTypes(events ...EventType) predicate.Predicate {
	has := func(event EventType) bool {
		for _, e := range events {
			if e == event {
				return true
			}
		}
		return false
	}

	return predicate.Funcs{
		CreateFunc:  func(_ event.CreateEvent) bool { return has(Create) },
		UpdateFunc:  func(_ event.UpdateEvent) bool { return has(Update) },
		DeleteFunc:  func(_ event.DeleteEvent) bool { return has(Delete) },
		GenericFunc: func(_ event.GenericEvent) bool { return has(Generic) },
	}
}

// TypedEvalGeneric returns true if all predicates match for the given object.
func TypedEvalGeneric[T client.Object](obj T, predicates ...predicate.TypedPredicate[T]) bool {
	e := event.TypedGenericEvent[T]{Object: obj}
	for _, p := range predicates {
		if !p.Generic(e) {
			return false
		}
	}

	return true
}

// EvalGeneric returns true if all predicates match for the given object.
func EvalGeneric[T client.Object](obj T, predicates ...predicate.Predicate) bool {
	e := event.GenericEvent{Object: obj}
	for _, p := range predicates {
		if !p.Generic(e) {
			return false
		}
	}

	return true
}

// RelevantConditionsChanged returns true for all events except for 'UPDATE'. Here, true is only returned when the
// status, reason or message of a relevant condition has changed.
func RelevantConditionsChanged[T client.Object](
	getConditionsFromObject func(obj client.Object) []gardencorev1beta1.Condition,
	relevantConditionTypes ...gardencorev1beta1.ConditionType,
) predicate.TypedPredicate[T] {
	wasConditionStatusReasonOrMessageUpdated := func(oldCondition, newCondition *gardencorev1beta1.Condition) bool {
		return (oldCondition == nil && newCondition != nil) ||
			(oldCondition != nil && newCondition == nil) ||
			(oldCondition != nil && newCondition != nil &&
				(oldCondition.Status != newCondition.Status || oldCondition.Reason != newCondition.Reason || oldCondition.Message != newCondition.Message))
	}

	return predicate.TypedFuncs[T]{
		UpdateFunc: func(e event.TypedUpdateEvent[T]) bool {
			var (
				oldConditions = getConditionsFromObject(e.ObjectOld)
				newConditions = getConditionsFromObject(e.ObjectNew)
			)

			for _, condition := range relevantConditionTypes {
				if wasConditionStatusReasonOrMessageUpdated(
					v1beta1helper.GetCondition(oldConditions, condition),
					v1beta1helper.GetCondition(newConditions, condition),
				) {
					return true
				}
			}

			return false
		},
	}
}

// ManagedResourceConditionsChanged returns a predicate which returns true if the status/reason/message of the
// Resources{Applied,Healthy,Progressing} condition of the ManagedResource changes.
func ManagedResourceConditionsChanged() predicate.TypedPredicate[*resourcesv1alpha1.ManagedResource] {
	return RelevantConditionsChanged[*resourcesv1alpha1.ManagedResource](
		func(obj client.Object) []gardencorev1beta1.Condition {
			managedResource, ok := obj.(*resourcesv1alpha1.ManagedResource)
			if !ok {
				return nil
			}
			return managedResource.Status.Conditions
		},
		resourcesv1alpha1.ResourcesApplied,
		resourcesv1alpha1.ResourcesHealthy,
		resourcesv1alpha1.ResourcesProgressing,
	)
}

// LastOperationChanged returns a predicate which returns true when the LastOperation of the passed object is changed.
func LastOperationChanged[T client.Object](getLastOperation func(client.Object) *gardencorev1beta1.LastOperation) predicate.TypedPredicate[T] {
	return predicate.TypedFuncs[T]{
		CreateFunc: func(e event.TypedCreateEvent[T]) bool {
			// If the object has the operation annotation reconcile, this means it's not picked up by the extension controller.
			// For restore and migrate operations, we remove the annotation only at the end, so we don't stop enqueueing it.
			if e.Object.GetAnnotations()[v1beta1constants.GardenerOperation] == v1beta1constants.GardenerOperationReconcile {
				return false
			}

			// If lastOperation State is failed then we admit reconciliation.
			// This is not possible during create but possible during a controller restart.
			return lastOperationStateFailed(getLastOperation(e.Object))
		},

		UpdateFunc: func(e event.TypedUpdateEvent[T]) bool {
			// If the object has the operation annotation, this means it's not picked up by the extension controller.
			// migrate and restore annotations are removed for the extensions only at the end of the operation,
			// so if the oldObject doesn't have the same annotation, don't enqueue it.
			if v1beta1helper.HasOperationAnnotation(e.ObjectNew.GetAnnotations()) {
				operation := e.ObjectNew.GetAnnotations()[v1beta1constants.GardenerOperation]

				if operation != v1beta1constants.GardenerOperationMigrate && operation != v1beta1constants.GardenerOperationRestore {
					return false
				}

				// if the oldObject doesn't have the same annotation skip
				if e.ObjectOld.GetAnnotations()[v1beta1constants.GardenerOperation] != operation {
					return false
				}
			}

			// If lastOperation State has changed to Succeeded or Error then we admit reconciliation.
			return lastOperationStateChanged(getLastOperation(e.ObjectOld), getLastOperation(e.ObjectNew))
		},

		DeleteFunc:  func(event.TypedDeleteEvent[T]) bool { return false },
		GenericFunc: func(event.TypedGenericEvent[T]) bool { return false },
	}
}

// ReconciliationFinishedSuccessfully is a helper function for checking whether the last operation indicates a
// successful reconciliation.
func ReconciliationFinishedSuccessfully(oldLastOperation, newLastOperation *gardencorev1beta1.LastOperation) bool {
	return oldLastOperation != nil &&
		oldLastOperation.Type != gardencorev1beta1.LastOperationTypeDelete &&
		oldLastOperation.State == gardencorev1beta1.LastOperationStateProcessing &&
		newLastOperation != nil &&
		newLastOperation.Type != gardencorev1beta1.LastOperationTypeDelete &&
		newLastOperation.State == gardencorev1beta1.LastOperationStateSucceeded
}

func lastOperationStateFailed(lastOperation *gardencorev1beta1.LastOperation) bool {
	if lastOperation == nil {
		return false
	}

	return lastOperation.State == gardencorev1beta1.LastOperationStateFailed
}

func lastOperationStateChanged(oldLastOp, newLastOp *gardencorev1beta1.LastOperation) bool {
	if newLastOp == nil {
		return false
	}

	newLastOperationStateSucceededOrErroneous := newLastOp.State == gardencorev1beta1.LastOperationStateSucceeded || newLastOp.State == gardencorev1beta1.LastOperationStateError || newLastOp.State == gardencorev1beta1.LastOperationStateFailed

	if newLastOperationStateSucceededOrErroneous {
		if oldLastOp != nil {
			return !reflect.DeepEqual(oldLastOp, newLastOp)
		}
		return true
	}

	return false
}

// GetExtensionLastOperation returns the LastOperation of the passed extension object.
func GetExtensionLastOperation(obj client.Object) *gardencorev1beta1.LastOperation {
	acc, err := extensions.Accessor(obj)
	if err != nil {
		return nil
	}

	return acc.GetExtensionStatus().GetLastOperation()
}

// SeedNamePredicate returns a predicate which returns true for objects that are being migrated to a different
// seed cluster.
func SeedNamePredicate[T client.Object](seedName string, getSeedNamesFromObject func(client.Object) (*string, *string)) predicate.TypedPredicate[T] {
	return predicate.NewTypedPredicateFuncs[T](func(obj T) bool {
		specSeedName, statusSeedName := getSeedNamesFromObject(obj)
		return gardenerutils.GetResponsibleSeedName(specSeedName, statusSeedName) == seedName
	})
}
