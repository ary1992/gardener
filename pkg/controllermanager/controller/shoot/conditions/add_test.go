// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package conditions_test

import (
	"context"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	seedmanagementv1alpha1 "github.com/gardener/gardener/pkg/apis/seedmanagement/v1alpha1"
	"github.com/gardener/gardener/pkg/client/kubernetes"
	. "github.com/gardener/gardener/pkg/controllermanager/controller/shoot/conditions"
)

var _ = Describe("Add", func() {
	var reconciler *Reconciler

	BeforeEach(func() {
		reconciler = &Reconciler{}
	})

	Describe("SeedPredicate", func() {
		var p predicate.TypedPredicate[*gardencorev1beta1.Seed]

		BeforeEach(func() {
			p = reconciler.SeedPredicate()
		})

		Describe("#Create", func() {
			It("should return true", func() {
				Expect(p.Create(event.TypedCreateEvent[*gardencorev1beta1.Seed]{})).To(BeTrue())
			})
		})

		Describe("#Delete", func() {
			It("should return true", func() {
				Expect(p.Delete(event.TypedDeleteEvent[*gardencorev1beta1.Seed]{})).To(BeTrue())
			})
		})

		Describe("#Generic", func() {
			It("should return true", func() {
				Expect(p.Generic(event.TypedGenericEvent[*gardencorev1beta1.Seed]{})).To(BeTrue())
			})
		})

		Describe("#Update", func() {
			var (
				e event.TypedUpdateEvent[*gardencorev1beta1.Seed]

				oldSeed, newSeed *gardencorev1beta1.Seed
				gardenletReady   = []gardencorev1beta1.Condition{{
					Type:   gardencorev1beta1.SeedGardenletReady,
					Status: gardencorev1beta1.ConditionTrue,
				}}
				gardenletNotReady = []gardencorev1beta1.Condition{{
					Type:   gardencorev1beta1.SeedGardenletReady,
					Status: gardencorev1beta1.ConditionFalse,
				}}
			)

			BeforeEach(func() {
				oldSeed = &gardencorev1beta1.Seed{}
				newSeed = &gardencorev1beta1.Seed{}
				e = event.TypedUpdateEvent[*gardencorev1beta1.Seed]{ObjectOld: oldSeed, ObjectNew: newSeed}
			})

			It("should return true in case of cache resync update events", func() {
				newSeed.ResourceVersion = "1"
				oldSeed.ResourceVersion = "1"

				Expect(p.Update(e)).To(BeTrue())
			})

			It("should return true if conditions differ", func() {
				newSeed.ResourceVersion = "1"
				oldSeed.ResourceVersion = "2"
				newSeed.Status.Conditions = gardenletReady
				oldSeed.Status.Conditions = gardenletNotReady

				Expect(p.Update(e)).To(BeTrue())
			})

			It("should return false if conditions are the same", func() {
				newSeed.ResourceVersion = "1"
				oldSeed.ResourceVersion = "2"
				newSeed.Status.Conditions = gardenletReady
				oldSeed.Status.Conditions = gardenletReady

				Expect(p.Update(e)).To(BeFalse())
			})
		})
	})

	Describe("#MapSeedToShoot", func() {
		var (
			ctx        = context.TODO()
			log        logr.Logger
			fakeClient client.Client

			seed        *gardencorev1beta1.Seed
			shoot       *gardencorev1beta1.Shoot
			managedSeed *seedmanagementv1alpha1.ManagedSeed
		)

		BeforeEach(func() {
			log = logr.Discard()
			fakeClient = fakeclient.NewClientBuilder().WithScheme(kubernetes.GardenScheme).Build()

			seed = &gardencorev1beta1.Seed{
				ObjectMeta: metav1.ObjectMeta{
					Name: "shoot-registered-as-seed",
				},
			}
			shoot = &gardencorev1beta1.Shoot{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "some-shoot",
					Namespace: "garden",
				},
			}
			managedSeed = &seedmanagementv1alpha1.ManagedSeed{
				ObjectMeta: metav1.ObjectMeta{
					Name:      seed.Name,
					Namespace: "garden",
				},
				Spec: seedmanagementv1alpha1.ManagedSeedSpec{
					Shoot: &seedmanagementv1alpha1.Shoot{
						Name: shoot.Name,
					},
				},
			}
		})

		It("should do nothing if the object is no Seed", func() {
			Expect(reconciler.MapSeedToShoot(ctx, log, fakeClient, &corev1.Secret{})).To(BeEmpty())
		})

		It("should do nothing if there is no related ManagedSeed", func() {
			Expect(reconciler.MapSeedToShoot(ctx, log, fakeClient, seed)).To(BeEmpty())
		})

		It("should do nothing if the ManagedSeed does not reference a Shoot", func() {
			managedSeed.Spec.Shoot = nil
			Expect(fakeClient.Create(ctx, managedSeed)).To(Succeed())
			Expect(reconciler.MapSeedToShoot(ctx, log, fakeClient, seed)).To(BeEmpty())
		})

		It("should do nothing if there is no related Shoot", func() {
			Expect(fakeClient.Create(ctx, managedSeed)).To(Succeed())
			Expect(reconciler.MapSeedToShoot(ctx, log, fakeClient, seed)).To(BeEmpty())
		})

		It("should map the Seed to the Shoot", func() {
			Expect(fakeClient.Create(ctx, managedSeed)).To(Succeed())
			Expect(fakeClient.Create(ctx, shoot)).To(Succeed())

			Expect(reconciler.MapSeedToShoot(ctx, log, fakeClient, seed)).To(ConsistOf(
				reconcile.Request{NamespacedName: types.NamespacedName{Name: shoot.Name, Namespace: shoot.Namespace}},
			))
		})
	})
})
