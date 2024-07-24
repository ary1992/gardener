// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package networkpolicy

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	gardencore "github.com/gardener/gardener/pkg/apis/core"
	v1beta1helper "github.com/gardener/gardener/pkg/apis/core/v1beta1/helper"
	extensionsv1alpha1 "github.com/gardener/gardener/pkg/apis/extensions/v1alpha1"
	"github.com/gardener/gardener/pkg/controller/networkpolicy"
	"github.com/gardener/gardener/pkg/controller/networkpolicy/hostnameresolver"
	"github.com/gardener/gardener/pkg/controllerutils/mapper"
	"github.com/gardener/gardener/pkg/extensions"
	"github.com/gardener/gardener/pkg/gardenlet/apis/config"
	gardenletutils "github.com/gardener/gardener/pkg/utils/gardener/gardenlet"
)

// SeedIsGardenCheckInterval is the interval how often it should be checked whether the seed cluster has been registered
// as garden cluster.
var SeedIsGardenCheckInterval = time.Minute

// AddToManager adds all Seed controllers to the given manager.
func AddToManager(
	ctx context.Context,
	mgr manager.Manager,
	gardenletCancel context.CancelFunc,
	seedCluster cluster.Cluster,
	cfg config.NetworkPolicyControllerConfiguration,
	networks gardencore.SeedNetworks,
	resolver hostnameresolver.HostResolver,
) error {
	seedIsGarden, err := gardenletutils.SeedIsGarden(ctx, seedCluster.GetAPIReader())
	if err != nil {
		return fmt.Errorf("failed checking whether the seed is the garden cluster: %w", err)
	}
	if seedIsGarden {
		return nil // When the seed is the garden cluster at the same time, the gardener-operator runs this controller.
	}

	reconciler := &networkpolicy.Reconciler{
		ConcurrentSyncs:              cfg.ConcurrentSyncs,
		AdditionalNamespaceSelectors: cfg.AdditionalNamespaceSelectors,
		Resolver:                     resolver,
		RuntimeNetworks: networkpolicy.RuntimeNetworkConfig{
			IPFamilies: networks.IPFamilies,
			Pods:       networks.Pods,
			Services:   networks.Services,
			Nodes:      networks.Nodes,
			BlockCIDRs: networks.BlockCIDRs,
		},
	}

	reconciler.WatchRegisterers = append(reconciler.WatchRegisterers, func(c controller.Controller) error {
		return c.Watch(
			source.Kind(seedCluster.GetCache(),
				&extensionsv1alpha1.Cluster{},
				mapper.TypedEnqueueRequestsFrom[*extensionsv1alpha1.Cluster](ctx, mgr.GetCache(), mapper.MapFunc(reconciler.MapObjectToName), mapper.UpdateWithNew, mgr.GetLogger()),
				ClusterPredicate()),
		)
	})

	if err := reconciler.AddToManager(ctx, mgr, seedCluster); err != nil {
		return err
	}

	// At this point, the seed is not the garden cluster at the same time. However, this could change during the runtime
	// of gardenlet. If so, gardener-operator will take over responsibility of the NetworkPolicy management and will run
	// this controller. Since there is no way to stop a controller after it started, we cancel the manager context in
	// case the seed is registered as garden during runtime. This way, gardenlet will restart and not add the controller
	// again.
	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		wait.Until(func() {
			seedIsGarden, err = gardenletutils.SeedIsGarden(ctx, seedCluster.GetClient())
			if err != nil {
				mgr.GetLogger().Error(err, "Failed checking whether the seed cluster is the garden cluster at the same time")
				return
			}
			if !seedIsGarden {
				return
			}

			mgr.GetLogger().Info("Terminating gardenlet since seed cluster has been registered as garden cluster. " +
				"This effectively stops the NetworkPolicy controller (gardener-operator takes over now).")
			gardenletCancel()
		}, SeedIsGardenCheckInterval, ctx.Done())
		return nil
	}))
}

// ClusterPredicate is a predicate which returns 'true' when the network CIDRs of a shoot cluster change.
func ClusterPredicate() predicate.TypedPredicate[*extensionsv1alpha1.Cluster] {
	return predicate.TypedFuncs[*extensionsv1alpha1.Cluster]{
		UpdateFunc: func(e event.TypedUpdateEvent[*extensionsv1alpha1.Cluster]) bool {
			cluster := e.ObjectNew
			if v1beta1helper.IsNil(cluster) {
				return false
			}
			shoot, err := extensions.ShootFromCluster(cluster)
			if err != nil || shoot == nil {
				return false
			}

			oldCluster := e.ObjectOld
			if v1beta1helper.IsNil(oldCluster) {
				return false
			}
			oldShoot, err := extensions.ShootFromCluster(oldCluster)
			if err != nil || oldShoot == nil {
				return false
			}

			// if the shoot has no networking field, return false
			if shoot.Spec.Networking == nil {
				return false
			}

			if v1beta1helper.IsWorkerless(shoot) {
				// if the shoot has networking field set and the old shoot has nil, then we cannot compare services, so return true right away
				return oldShoot.Spec.Networking == nil || !ptr.Equal(shoot.Spec.Networking.Services, oldShoot.Spec.Networking.Services)
			}

			return !ptr.Equal(shoot.Spec.Networking.Pods, oldShoot.Spec.Networking.Pods) ||
				!ptr.Equal(shoot.Spec.Networking.Services, oldShoot.Spec.Networking.Services) ||
				!ptr.Equal(shoot.Spec.Networking.Nodes, oldShoot.Spec.Networking.Nodes)
		},
		CreateFunc:  func(event.TypedCreateEvent[*extensionsv1alpha1.Cluster]) bool { return false },
		DeleteFunc:  func(event.TypedDeleteEvent[*extensionsv1alpha1.Cluster]) bool { return false },
		GenericFunc: func(event.TypedGenericEvent[*extensionsv1alpha1.Cluster]) bool { return false },
	}
}
