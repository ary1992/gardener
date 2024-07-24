// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package secrets_test

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
	"github.com/gardener/gardener/pkg/client/kubernetes"
	. "github.com/gardener/gardener/pkg/controllermanager/controller/seed/secrets"
)

var _ = Describe("Add", func() {
	var (
		reconciler *Reconciler
		secret     *corev1.Secret
	)

	BeforeEach(func() {
		reconciler = &Reconciler{
			GardenNamespace: "garden",
		}
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "garden",
				Labels: map[string]string{
					"gardener.cloud/role": "foo",
				},
			},
		}
	})

	Describe("GardenSecretPredicate", func() {
		var p predicate.TypedPredicate[*corev1.Secret]

		BeforeEach(func() {
			p = reconciler.GardenSecretPredicate()
		})

		tests := func(f func(obj *corev1.Secret) bool) {
			It("should return false because object is not in garden namespace", func() {
				secret.Namespace = "foo"
				Expect(f(secret)).To(BeFalse())
			})

			It("should return false because object has no gardener.cloud/role label", func() {
				secret.Labels = nil
				Expect(f(secret)).To(BeFalse())
			})

			It("should return false because object has control plane label", func() {
				secret.Labels["gardener.cloud/role"] = "kubeconfig"
				Expect(f(secret)).To(BeFalse())
			})

			It("should return true because object matches all conditions", func() {
				Expect(f(secret)).To(BeTrue())
			})
		}

		Describe("#Create", func() {
			tests(func(obj *corev1.Secret) bool { return p.Create(event.TypedCreateEvent[*corev1.Secret]{Object: obj}) })
		})

		Describe("#Update", func() {
			tests(func(obj *corev1.Secret) bool { return p.Update(event.TypedUpdateEvent[*corev1.Secret]{ObjectNew: obj}) })
		})

		Describe("#Delete", func() {
			tests(func(obj *corev1.Secret) bool { return p.Delete(event.TypedDeleteEvent[*corev1.Secret]{Object: obj}) })
		})

		Describe("#Generic", func() {
			tests(func(obj *corev1.Secret) bool { return p.Generic(event.TypedGenericEvent[*corev1.Secret]{Object: obj}) })
		})
	})

	Describe("SecretPredicate", func() {
		var p predicate.TypedPredicate[*corev1.Secret]

		BeforeEach(func() {
			p = reconciler.SecretPredicate()
		})

		Describe("#Create", func() {
			It("should return true", func() {
				Expect(p.Create(event.TypedCreateEvent[*corev1.Secret]{})).To(BeTrue())
			})
		})

		Describe("#Update", func() {
			It("should return false because object is no Secret", func() {
				Expect(p.Update(event.TypedUpdateEvent[*corev1.Secret]{})).To(BeFalse())
			})

			It("should return false because old object is no Secret", func() {
				Expect(p.Update(event.TypedUpdateEvent[*corev1.Secret]{ObjectNew: secret})).To(BeFalse())
			})

			It("should return false because there is no relevant change", func() {
				Expect(p.Update(event.TypedUpdateEvent[*corev1.Secret]{ObjectNew: secret, ObjectOld: secret})).To(BeFalse())
			})

			It("should return true because there is a relevant change", func() {
				oldSecret := secret.DeepCopy()
				secret.ResourceVersion = "2"
				Expect(p.Update(event.TypedUpdateEvent[*corev1.Secret]{ObjectNew: secret, ObjectOld: oldSecret})).To(BeTrue())
			})
		})

		Describe("#Delete", func() {
			It("should return true", func() {
				Expect(p.Delete(event.TypedDeleteEvent[*corev1.Secret]{})).To(BeTrue())
			})
		})

		Describe("#Generic", func() {
			It("should return true", func() {
				Expect(p.Generic(event.TypedGenericEvent[*corev1.Secret]{})).To(BeTrue())
			})
		})
	})

	Describe("#MapToAllSeeds", func() {
		var (
			ctx          = context.TODO()
			log          logr.Logger
			fakeClient   client.Client
			seed1, seed2 *gardencorev1beta1.Seed
		)

		BeforeEach(func() {
			log = logr.Discard()
			fakeClient = fakeclient.NewClientBuilder().WithScheme(kubernetes.GardenScheme).Build()

			seed1 = &gardencorev1beta1.Seed{ObjectMeta: metav1.ObjectMeta{Name: "seed1"}}
			seed2 = &gardencorev1beta1.Seed{ObjectMeta: metav1.ObjectMeta{Name: "seed2"}}

			Expect(fakeClient.Create(ctx, seed1)).To(Succeed())
			Expect(fakeClient.Create(ctx, seed2)).To(Succeed())
		})

		It("should map to all seeds", func() {
			Expect(reconciler.MapToAllSeeds(ctx, log, fakeClient, nil)).To(ConsistOf(
				reconcile.Request{NamespacedName: types.NamespacedName{Name: seed1.Name}},
				reconcile.Request{NamespacedName: types.NamespacedName{Name: seed2.Name}},
			))
		})
	})
})
