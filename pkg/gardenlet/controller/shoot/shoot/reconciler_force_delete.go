// Copyright 2023 SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package shoot

import (
	"context"
	"fmt"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	v1beta1helper "github.com/gardener/gardener/pkg/apis/core/v1beta1/helper"
	"github.com/gardener/gardener/pkg/client/kubernetes/clientmap/keys"
	"github.com/gardener/gardener/pkg/operation"
	botanistpkg "github.com/gardener/gardener/pkg/operation/botanist"
	"github.com/gardener/gardener/pkg/utils/errors"
	"github.com/gardener/gardener/pkg/utils/flow"
	"github.com/gardener/gardener/pkg/utils/gardener/shootstate"
	retryutils "github.com/gardener/gardener/pkg/utils/retry"
)

// runForceDeleteShootFlow force deletes a Shoot cluster.
// It receives an Operation object <o> which stores the Shoot object and an ErrorContext which contains error from the previous operation.
func (r *Reconciler) runForceDeleteShootFlow(ctx context.Context, o *operation.Operation) *v1beta1helper.WrappedLastErrors {
	var (
		botanist        *botanistpkg.Botanist
		tasksWithErrors []string
		err             error
	)

	for _, lastError := range o.Shoot.GetInfo().Status.LastErrors {
		if lastError.TaskID != nil {
			tasksWithErrors = append(tasksWithErrors, *lastError.TaskID)
		}
	}

	errorContext := errors.NewErrorContext("Shoot cluster force deletion", tasksWithErrors)

	err = errors.HandleErrors(errorContext,
		func(errorID string) error {
			o.CleanShootTaskError(ctx, errorID)
			return nil
		},
		nil,
		errors.ToExecute("Create botanist", func() error {
			return retryutils.UntilTimeout(ctx, 10*time.Second, 10*time.Minute, func(context.Context) (done bool, err error) {
				botanist, err = botanistpkg.New(ctx, o)
				if err != nil {
					return retryutils.MinorError(err)
				}
				return retryutils.Ok()
			})
		}),
		errors.ToExecute("Check required extensions exist", func() error {
			return botanist.WaitUntilRequiredExtensionsReady(ctx)
		}),
		// We first check whether the namespace in the Seed cluster does exist - if it does not, then we assume that
		// all resources have already been deleted. We can delete the Shoot resource as a consequence.
		errors.ToExecute("Retrieve the Shoot namespace in the Seed cluster", func() error {
			return checkIfSeedNamespaceExists(ctx, o, botanist)
		}),
	)

	if err != nil {
		return v1beta1helper.NewWrappedLastErrors(v1beta1helper.FormatLastErrDescription(err), err)
	}

	var (
		defaultInterval = 5 * time.Second
		defaultTimeout  = 30 * time.Second

		cleaner = NewCleaner(botanist.SeedClientSet.Client(), r.GardenClient, botanist.Shoot.SeedNamespace, botanist.Shoot.GetInfo().Namespace, botanist.Shoot.BackupEntryName, logf.FromContext(ctx))

		g = flow.NewGraph("Shoot cluster force deletion")

		deleteControlPlanes = g.Add(flow.Task{
			Name: "Deleting ControlPlane resources",
			Fn:   flow.TaskFn(cleaner.DeleteControlPlanes).RetryUntilTimeout(defaultInterval, defaultTimeout),
		})
		waitUntilControlPlanesDeleted = g.Add(flow.Task{
			Name:         "Waiting until ControlPlane resources have been deleted",
			Fn:           cleaner.WaitUntilControlPlanesDeleted,
			Dependencies: flow.NewTaskIDs(deleteControlPlanes),
		})
		deleteDNSRecords = g.Add(flow.Task{
			Name: "Deleting DNSRecord resources",
			Fn:   flow.TaskFn(cleaner.DeleteDNSRecords).RetryUntilTimeout(defaultInterval, defaultTimeout),
		})
		waitUntilDNSRecordsDeleted = g.Add(flow.Task{
			Name:         "Waiting until DNSRecord resources have been deleted",
			Fn:           cleaner.WaitUntilDNSRecordsDeleted,
			Dependencies: flow.NewTaskIDs(deleteDNSRecords),
		})
		deleteExtensionObjects = g.Add(flow.Task{
			Name: "Deleting extension resources",
			Fn:   flow.TaskFn(cleaner.DeleteExtensionObjects).RetryUntilTimeout(defaultInterval, defaultTimeout),
		})
		waitUntilExtensionObjectsDeleted = g.Add(flow.Task{
			Name:         "Waiting until extension resources have been deleted",
			Fn:           cleaner.WaitUntilExtensionObjectsDeleted,
			Dependencies: flow.NewTaskIDs(deleteExtensionObjects),
		})
		deleteMCMResources = g.Add(flow.Task{
			Name: "Deleting MCM resources",
			Fn:   flow.TaskFn(cleaner.DeleteMCMResources).RetryUntilTimeout(defaultInterval, defaultTimeout),
		})
		waitUntilMCMResourcesDeleted = g.Add(flow.Task{
			Name:         "Waiting until MCM resources have been deleted",
			Fn:           cleaner.WaitUntilMCMResourcesDeleted,
			Dependencies: flow.NewTaskIDs(deleteMCMResources),
		})
		deleteBackupEntry = g.Add(flow.Task{
			Name: "Deleting BackupEntry resource",
			Fn:   flow.TaskFn(cleaner.DeleteBackupEntry).RetryUntilTimeout(defaultInterval, defaultTimeout),
		})
		waitUntilBackupEntryDeleted = g.Add(flow.Task{
			Name:         "Waiting until BackupEntry resource has been deleted",
			Fn:           cleaner.WaitUntilBackupEntryDeleted,
			Dependencies: flow.NewTaskIDs(deleteBackupEntry),
		})
		deleteCluster = g.Add(flow.Task{
			Name:         "Deleting Cluster resource",
			Fn:           flow.TaskFn(cleaner.DeleteCluster).RetryUntilTimeout(defaultInterval, defaultTimeout),
			Dependencies: flow.NewTaskIDs(waitUntilExtensionObjectsDeleted, waitUntilBackupEntryDeleted, waitUntilControlPlanesDeleted, waitUntilDNSRecordsDeleted),
		})
		_ = g.Add(flow.Task{
			Name:         "Waiting until Cluster resource has been deleted",
			Fn:           cleaner.WaitUntilClusterDeleted,
			Dependencies: flow.NewTaskIDs(deleteCluster),
		})
		setKeepObjectsForManagedResources = g.Add(flow.Task{
			Name:         "Configuring managed resources to keep their objects when deleted",
			Fn:           flow.TaskFn(cleaner.SetKeepObjectsForManagedResources).RetryUntilTimeout(defaultInterval, defaultTimeout),
			Dependencies: flow.NewTaskIDs(),
		})
		deleteManagedResources = g.Add(flow.Task{
			Name:         "Deleting managed resources",
			Fn:           flow.TaskFn(cleaner.DeleteManagedResources).RetryUntilTimeout(defaultInterval, defaultTimeout),
			Dependencies: flow.NewTaskIDs(setKeepObjectsForManagedResources),
		})
		waitUntilManagedResourcesDeleted = g.Add(flow.Task{
			Name:         "Waiting until managed resources have been deleted",
			Fn:           cleaner.WaitUntilManagedResourcesDeleted,
			Dependencies: flow.NewTaskIDs(deleteManagedResources),
		})
		deleteEtcds = g.Add(flow.Task{
			Name:         "Deleting Etcd resources",
			Fn:           flow.TaskFn(botanist.DestroyEtcd).RetryUntilTimeout(defaultInterval, defaultTimeout),
			Dependencies: flow.NewTaskIDs(),
		})
		waitUntilEtcdsDeleted = g.Add(flow.Task{
			Name:         "Waiting until Etcd resources have been deleted",
			Fn:           botanist.WaitUntilEtcdsDeleted,
			Dependencies: flow.NewTaskIDs(deleteEtcds),
		})
		deleteSecrets = g.Add(flow.Task{
			Name:         "Deleting secrets",
			Fn:           flow.TaskFn(cleaner.DeleteSecrets).RetryUntilTimeout(defaultInterval, defaultTimeout),
			Dependencies: flow.NewTaskIDs(waitUntilExtensionObjectsDeleted, waitUntilEtcdsDeleted, waitUntilMCMResourcesDeleted, waitUntilManagedResourcesDeleted),
		})
		deleteNamespace = g.Add(flow.Task{
			Name:         "Deleting shoot namespace",
			Fn:           flow.TaskFn(botanist.DeleteSeedNamespace).RetryUntilTimeout(defaultInterval, defaultTimeout),
			Dependencies: flow.NewTaskIDs(waitUntilExtensionObjectsDeleted, waitUntilEtcdsDeleted, waitUntilMCMResourcesDeleted, waitUntilManagedResourcesDeleted, deleteSecrets),
		})
		_ = g.Add(flow.Task{
			Name:         "Waiting until shoot namespace has been deleted",
			Fn:           botanist.WaitUntilSeedNamespaceDeleted,
			Dependencies: flow.NewTaskIDs(deleteNamespace),
		})
		_ = g.Add(flow.Task{
			Name: "Deleting Shoot State",
			Fn: func(ctx context.Context) error {
				return shootstate.Delete(ctx, botanist.GardenClient, botanist.Shoot.GetInfo())
			},
			Dependencies: flow.NewTaskIDs(deleteNamespace),
		})

		f = g.Compile()
	)

	if err := f.Run(ctx, flow.Opts{
		Log:              o.Logger,
		ProgressReporter: r.newProgressReporter(o.ReportShootProgress),
		ErrorCleaner:     o.CleanShootTaskError,
		ErrorContext:     errorContext,
	}); err != nil {
		return v1beta1helper.NewWrappedLastErrors(v1beta1helper.FormatLastErrDescription(err), flow.Errors(err))
	}

	// ensure that shoot client is invalidated after it has been deleted
	if err := o.ShootClientMap.InvalidateClient(keys.ForShoot(o.Shoot.GetInfo())); err != nil {
		err = fmt.Errorf("failed to invalidate shoot client: %w", err)
		return v1beta1helper.NewWrappedLastErrors(v1beta1helper.FormatLastErrDescription(err), err)
	}

	o.Logger.Info("Successfully deleted Shoot cluster")
	return nil
}
