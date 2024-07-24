// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package state_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	. "github.com/gardener/gardener/pkg/gardenlet/controller/shoot/state"
)

var _ = Describe("Add", func() {
	var (
		reconciler *Reconciler
		shoot      *gardencorev1beta1.Shoot

		seedName = "seed"
	)

	BeforeEach(func() {
		reconciler = &Reconciler{SeedName: seedName}
		shoot = &gardencorev1beta1.Shoot{}
	})

	Describe("#SeedNameChangedPredicate", func() {
		var p predicate.TypedPredicate[*gardencorev1beta1.Shoot]

		BeforeEach(func() {
			p = reconciler.SeedNameChangedPredicate()
		})

		Describe("#Create", func() {
			It("should return true", func() {
				Expect(p.Create(event.TypedCreateEvent[*gardencorev1beta1.Shoot]{})).To(BeTrue())
			})
		})

		Describe("#Update", func() {
			It("should return false because new object is no shoot", func() {
				Expect(p.Update(event.TypedUpdateEvent[*gardencorev1beta1.Shoot]{})).To(BeFalse())
			})

			It("should return false because old object is no shoot", func() {
				Expect(p.Update(event.TypedUpdateEvent[*gardencorev1beta1.Shoot]{ObjectNew: shoot})).To(BeFalse())
			})

			It("should return false because seed name is equal", func() {
				Expect(p.Update(event.TypedUpdateEvent[*gardencorev1beta1.Shoot]{ObjectNew: shoot, ObjectOld: shoot})).To(BeFalse())
			})

			It("should return true because seed name changed", func() {
				oldShoot := shoot.DeepCopy()
				shoot.Spec.SeedName = ptr.To("new-seed")

				Expect(p.Update(event.TypedUpdateEvent[*gardencorev1beta1.Shoot]{ObjectNew: shoot, ObjectOld: oldShoot})).To(BeTrue())
			})
		})

		Describe("#Delete", func() {
			It("should return true", func() {
				Expect(p.Delete(event.TypedDeleteEvent[*gardencorev1beta1.Shoot]{})).To(BeTrue())
			})
		})

		Describe("#Generic", func() {
			It("should return true", func() {
				Expect(p.Generic(event.TypedGenericEvent[*gardencorev1beta1.Shoot]{})).To(BeTrue())
			})
		})
	})
})
