# Shoot Force Deletion in Gardener — Architecture & Deep Dive

A complete reference for **shoot force deletion** — the escape hatch that lets an operator
destroy a stuck Shoot cluster even when normal graceful cleanup is permanently blocked by an
infrastructure or configuration error. It bypasses all shoot-API-level resource cleanup,
reaches directly into the seed to strip finalizers and delete every resource, and completes
even if the underlying cloud provider is unreachable.

---

## Table of Contents

1. [Problem & Motivation](#1-problem--motivation)
2. [The Force-Deletion Annotation](#2-the-force-deletion-annotation)
3. [Preconditions](#3-preconditions)
4. [How the Gardenlet Detects & Dispatches Force Deletion](#4-how-the-gardenlet-detects--dispatches-force-deletion)
5. [Force-Delete Flow Graph](#5-force-delete-flow-graph)
6. [The Cleaner — Seed-Side Cleanup Utilities](#6-the-cleaner--seed-side-cleanup-utilities)
7. [Extension ForceDelete Actuator Interface](#7-extension-forcedelete-actuator-interface)
8. [Machine Resource Cleanup](#8-machine-resource-cleanup)
9. [ManagedResource Cleanup](#9-managedresource-cleanup)
10. [Etcd Cleanup](#10-etcd-cleanup)
11. [DNS Record Cleanup](#11-dns-record-cleanup)
12. [Bastion Handling](#12-bastion-handling)
13. [Normal Delete vs Force Delete — Side-by-Side](#13-normal-delete-vs-force-delete--side-by-side)
14. [Admission & Validation](#14-admission--validation)
15. [API Server Strategy: Generation Bump](#15-api-server-strategy-generation-bump)
16. [End-to-End Sequence](#16-end-to-end-sequence)
17. [Constraints & Edge Cases](#17-constraints--edge-cases)
18. [Summary](#18-summary)

---

## 1. Problem & Motivation

Normal shoot deletion works by **starting the control plane back up** (etcd, kube-apiserver,
gardener-resource-manager), draining shoot-API-level resources (Pods, Services, PVCs, etc.)
via real API calls into the shoot cluster, and then gracefully triggering each extension's
`Delete` actuator — waiting for every cloud resource (VMs, load balancers, VPCs) to be
deprovisioned before removing the seed-side namespace and the Shoot object.

This process can become **permanently stuck** when:

- Cloud credentials have expired or been revoked (`ErrorInfraUnauthenticated`,
  `ErrorInfraUnauthorized`) — the infrastructure extension cannot delete cloud resources.
- Provider quota or dependency constraints block teardown (`ErrorInfraDependencies`).
- The shoot cluster's own resources cannot be cleaned up (`ErrorCleanupClusterResources`).
- A misconfiguration prevents the reconcile loop from proceeding
  (`ErrorConfigurationProblem`).

In these situations the operator cannot fix the underlying issue and simply needs the
Shoot object gone — accepting that some orphaned cloud resources may remain. **Force deletion**
is the answer: it abandons the graceful cleanup, strips all finalizers in the seed, destroys
everything Gardener directly controls, and removes the Shoot, delegating cleanup of
remaining cloud resources to the operator.

There is **no feature gate** for force deletion — it is always available, gated only by
the preconditions below.

---

## 2. The Force-Deletion Annotation

```
confirmation.gardener.cloud/force-deletion: "true"
```

**Constant:** `v1beta1constants.AnnotationConfirmationForceDeletion`
(`pkg/apis/core/v1beta1/constants/types_constants.go:770`)

Detection:

```go
// pkg/api/core/v1beta1/helper/shoot.go:73
func ShootNeedsForceDeletion(shoot *gardencorev1beta1.Shoot) bool {
    value, ok := shoot.Annotations[v1beta1constants.AnnotationConfirmationForceDeletion]
    if !ok {
        return false
    }
    forceDelete, _ := strconv.ParseBool(value)
    return forceDelete
}
```

The annotation value is parsed with `strconv.ParseBool`, so `"true"`, `"TRUE"`, `"1"` all
activate force deletion. An absent annotation or any value that does not parse as `true`
means normal deletion.

---

## 3. Preconditions

The API server enforces three preconditions before the annotation may be set
(`pkg/api/core/validation/shoot.go:3623`):

### 3.1 DeletionTimestamp must be set

Force deletion can only be applied to a shoot that is already being deleted. The operator
must first `kubectl delete shoot <name>` (setting the `DeletionTimestamp`), wait for the
shoot to enter a stuck state, and then add the annotation.

### 3.2 A qualifying error code must be present in status

The shoot's `status.lastErrors` must contain at least one error from the allowed set
(`shoot.go:160`):

| Error code | Meaning |
|---|---|
| `ErrorInfraUnauthenticated` | Cloud credentials rejected (expired/revoked) |
| `ErrorInfraUnauthorized` | Insufficient cloud IAM permissions |
| `ErrorInfraDependencies` | Cloud resource dependency blocking teardown |
| `ErrorCleanupClusterResources` | Cannot clean up shoot cluster resources |
| `ErrorConfigurationProblem` | Shoot or extension misconfiguration |

Any other error code (network timeouts, rate limits, etc.) does not qualify — force deletion
is reserved for *permanent* failures where the graceful path can never succeed.

### 3.3 The annotation is a one-way door

Once `confirmation.gardener.cloud/force-deletion=true` is set it **cannot be removed**.
Attempting to remove it is explicitly forbidden by the validation (`shoot.go:3632`):

```
force-deletion annotation cannot be removed once set
```

This prevents accidental reversal mid-flight and ensures that once force deletion starts,
it runs to completion.

---

## 4. How the Gardenlet Detects & Dispatches Force Deletion

Entry point: `deleteShoot` (`pkg/gardenlet/controller/shoot/shoot/reconciler.go:198`).

Every reconcile for a shoot with a `DeletionTimestamp` goes through this function. The
key dispatch (line 250):

```go
if v1beta1helper.ShootNeedsForceDeletion(shoot) {
    flowErr = r.runForceDeleteShootFlow(ctx, log, o)
} else {
    flowErr = r.runDeleteShootFlow(ctx, o)
}
```

**Before the dispatch**, the reconciler handles two early exits:

1. **No `status.uid`** (line 208): If the shoot never reconciled successfully, there is
   nothing to clean up — finalize immediately without running any flow.
2. **Bastions exist** (line 219): If force deletion is needed but bastions still exist,
   `patchBastions` marks each bastion with the force-deletion annotation and requeues
   after 15 seconds (see §12). If it is a normal deletion with bastions, an error is
   returned and the deletion waits for bastions to be cleaned up first.

---

## 5. Force-Delete Flow Graph

`runForceDeleteShootFlow`
(`pkg/gardenlet/controller/shoot/shoot/reconciler_force_delete.go`)

### 5.1 Self-hosted shoots are excluded

```go
if v1beta1helper.IsShootSelfHosted(o.Shoot.GetInfo().Spec.Provider.Workers) {
    return nil
}
```

Force deletion is **not supported for self-hosted shoots** — it returns `nil` (success)
immediately, which causes `finalizeShootDeletion` to run and remove the finalizer without
any cleanup.

### 5.2 Setup phase

Before the flow graph runs, three steps execute serially:

1. **Create botanist** — retried every 10 s for up to 10 min.
2. **Check required extensions exist** — waits for required `ControllerRegistration` objects.
3. **Check if seed namespace exists** — if the seed namespace is already gone, all resources
   are assumed deleted; the flow is skipped and deletion is finalized.

### 5.3 Flow graph

The `nonTerminatingNamespace` flag controls DNS tasks: `true` when the seed namespace
still exists and is not in `Terminating` phase; DNS record tasks are skipped if the
namespace is already gone.

```
                         PARALLEL START
                              │
          ┌───────────────────┼──────────────────┐
          ▼                   ▼                   ▼
  DeleteExtensionObjects  DestroyIngress*    DeleteMCM*
          │               DestroyExternal*   WaitMCMDel*
  WaitExtObjectsDel       DestroyInternal*        │
          │                   │            DeleteMachineResources*
          ├───────────────────┘            WaitMachineResourcesDel*
          │                                        │
          ▼                                        │
  SetKeepObjectsForMRs                             │
          │                                        │
  DeleteManagedResources                           │
          │                                        │
  WaitManagedResourcesDel                          │
          │                                        │
  DeleteCluster ◀─────────────────────────────────┘
          │
   (sync point: all above complete)
          │
          ├──────────────────┐
          ▼                  ▼
      DeleteEtcds     DeletePublicSAKeys
          │
  WaitEtcdsDeleted
          │
  DeleteKubernetesResources
          │
      DeleteNamespace
          │
    ┌─────┴──────────┐
    ▼                ▼
WaitNamespaceDel  DeleteShootState
```

`*` tasks marked with `SkipIf: botanist.Shoot.IsWorkerless` for workerless shoots.

**Default intervals/timeouts:**
- Retry interval: `5 s`
- Task timeout: `30 s`
- Extension object wait: `5 min` (per kind, `DefaultTimeout` in `cleaner.go`)

---

## 6. The Cleaner — Seed-Side Cleanup Utilities

`pkg/gardenlet/controller/shoot/shoot/cleaner.go`

The `cleaner` struct holds seed and garden clients plus the shoot namespace and handles
all the raw object manipulation during force deletion.

### Extension kinds handled

```go
extensionKindToObjectList = map[string]client.ObjectList{
    extensionsv1alpha1.ContainerRuntimeResource:        &ContainerRuntimeList{},
    extensionsv1alpha1.ControlPlaneResource:            &ControlPlaneList{},
    extensionsv1alpha1.ExtensionResource:               &ExtensionList{},
    extensionsv1alpha1.InfrastructureResource:          &InfrastructureList{},
    extensionsv1alpha1.NetworkResource:                 &NetworkList{},
    extensionsv1alpha1.OperatingSystemConfigResource:   &OperatingSystemConfigList{},
    extensionsv1alpha1.SelfHostedShootExposureResource: &SelfHostedShootExposureList{},
    extensionsv1alpha1.WorkerResource:                  &WorkerList{},
}
```

`DeleteExtensionObjects` calls `extensions.DeleteExtensionObjects` for each kind, which
marks the objects for deletion. Because the `Cluster` resource in the seed still has the
`confirmation.gardener.cloud/force-deletion=true` annotation at this point, each
extension's reconciler switches to its `ForceDelete` actuator path (see §7).

### Machine kinds handled

```go
machineKindToObjectList = map[string]client.ObjectList{
    "MachineDeployment": &MachineDeploymentList{},
    "MachineSet":        &MachineSetList{},
    "Machine":           &MachineList{},
    "MachineClass":      &MachineClassList{},
}
```

`DeleteMachineResources` calls `utilclient.ForceDeleteObjects` for each kind — this
directly strips all finalizers and deletes the objects, completely bypassing MCM (see §8).

### Kubernetes resource kinds

`DeleteKubernetesResources` force-deletes all `ConfigMap` and `Secret` objects in the
shoot namespace, again stripping finalizers first.

---

## 7. Extension ForceDelete Actuator Interface

Every extension reconciler checks for force deletion before dispatching to its actuator:

```go
// Infrastructure (extensions/pkg/controller/infrastructure/reconciler.go:153)
if cluster != nil && v1beta1helper.ShootNeedsForceDeletion(cluster.Shoot) {
    err = r.actuator.ForceDelete(ctx, log, infrastructure, cluster)
} else {
    err = r.actuator.Delete(ctx, log, infrastructure, cluster)
}
```

The same pattern exists for Worker, ControlPlane, Network, Extension, ContainerRuntime,
OperatingSystemConfig, DNSRecord, Bastion, and SelfHostedShootExposure reconcilers.

### Actuator contract

The `ForceDelete` method is defined on each actuator interface:

```go
// Infrastructure actuator (extensions/pkg/controller/infrastructure/actuator.go:35)
ForceDelete(ctx context.Context, log logr.Logger,
    infra *extensionsv1alpha1.Infrastructure,
    cluster *extensionscontroller.Cluster) error

// Worker actuator (extensions/pkg/controller/worker/actuator.go:37)
ForceDelete(ctx context.Context, log logr.Logger,
    worker *extensionsv1alpha1.Worker,
    cluster *extensionscontroller.Cluster) error
```

The contract says: **skip all waits, remove finalizers, succeed even if cloud resources
remain orphaned.**

### Worker generic actuator — ForceDelete is a no-op

`extensions/pkg/controller/worker/genericactuator/actuator_delete.go:110`:

```go
// ForceDelete simply returns nil in case of forceful deletion since cleaning up the
// machines would never succeed in this case.
func (a *genericActuator) ForceDelete(...) error {
    return nil
}
```

Machine cleanup during force deletion is handled entirely by the cleaner's
`DeleteMachineResources` step (§8), not by the Worker extension's actuator.

### ControlPlane generic actuator — ForceDelete skips waits

`extensions/pkg/controller/controlplane/genericactuator/actuator.go:327`:

```go
func (a *actuator) ForceDelete(...) error {
    return a.Delete(ctx, log, cp, cluster)
}
```

Internally `Delete` checks `forceDelete := v1beta1helper.ShootNeedsForceDeletion(...)` and
when `true`:
- Does **not** wait for `ControlPlaneShootCRDsChartResourceName` ManagedResource deletion.
- Does **not** wait for `StorageClassesChartResourceName` deletion.
- Does **not** wait for `ControlPlaneShootChartResourceName` deletion.

These 2-minute waits are skipped, allowing the ControlPlane extension to complete
immediately.

---

## 8. Machine Resource Cleanup

During normal deletion the Worker extension's `Delete` actuator:
1. Labels all `Machine` objects with `force-deletion: "True"` to hint MCM.
2. Deletes `MachineDeployment` objects and waits for MCM to decommission VMs.
3. Waits for `MachineClass` cleanup.

During **force deletion** none of that happens. Instead the cleaner calls
`utilclient.ForceDeleteObjects` directly:

```go
// cleaner.go:96
func (c *cleaner) DeleteMachineResources(ctx context.Context) error {
    return utilclient.ApplyToObjectKinds(ctx, func(kind string, objectList client.ObjectList) flow.TaskFn {
        return utilclient.ForceDeleteObjects(c.seedClient, c.seedNamespace, objectList)
    }, machineKindToObjectList)
}
```

`ForceDeleteObjects` (`pkg/utils/kubernetes/client/client.go`):
1. Lists all objects of that kind in the namespace.
2. Calls `controllerutils.RemoveAllFinalizers` on each — a direct API `patch` that wipes
   the `finalizers` list, unblocking deletion.
3. Deletes the object.

The sequence is:

```
MCM is destroyed first (deleteMachineControllerManager)
          │
          ▼
ForceDelete MachineDeployments  ← strip finalizers + delete
ForceDelete MachineSets         ← strip finalizers + delete
ForceDelete Machines            ← strip finalizers + delete (VMs may remain orphaned in cloud)
ForceDelete MachineClasses      ← strip finalizers + delete
          │
          ▼
WaitUntilMachineResourcesDeleted (DefaultTimeout = 5 min)
```

**Consequence:** the underlying VMs are **not deprovisioned** during force deletion.
They remain in the cloud and must be cleaned up by the operator manually.

---

## 9. ManagedResource Cleanup

`ManagedResource` objects managed by gardener-resource-manager hold a `keepObjects`
field that controls whether the managed objects (e.g. Deployments, Services in the
shoot cluster) are deleted when the ManagedResource is removed.

During **normal deletion** (seed-to-seed migration) some ManagedResources are set to
`keepObjects=true` to preserve objects in the target cluster.

During **force deletion**, the flow explicitly sets all of them to `false` first:

```go
// cleaner.go:112
func (c *cleaner) SetKeepObjectsForManagedResources(ctx context.Context) error {
    // lists all MRs → patches each: keepObjects = false
}
```

Then `DeleteManagedResources`:
1. `DeleteAllOf` ManagedResources in the shoot namespace.
2. Calls `finalizeShootManagedResources` — removes all finalizers from **shoot-class**
   ManagedResources only (those without `spec.class != nil`). Class-based ManagedResources
   (runtime MRs managed by the seed gardenlet) are left to their own lifecycle.

Because gardener-resource-manager has been forcibly stopped (not deployed/scaled-up during
force deletion), the ManagedResource finalizers would block deletion — removing them
manually unblocks the cascade.

---

## 10. Etcd Cleanup

In the **normal delete flow** etcd is only destroyed after kube-apiserver, all shoot
resources, and all extensions have been cleaned up. An etcd backup/snapshot may be taken.

In the **force delete flow**, etcd is destroyed **directly** after the sync point
(extensions, machines, managed resources, cluster all gone), with no snapshot:

```go
// reconciler_force_delete.go:163
deleteEtcds = g.Add(flow.Task{
    Name: "Deleting Etcd resources",
    Fn:   flow.TaskFn(botanist.DestroyEtcd).RetryUntilTimeout(defaultInterval, defaultTimeout),
    Dependencies: flow.NewTaskIDs(syncPoint),
})
```

`DestroyEtcd` (`botanist/etcd.go:206`) destroys etcd-main and etcd-events in parallel:

```go
func (b *Botanist) DestroyEtcd(ctx context.Context) error {
    return flow.Parallel(
        b.Shoot.Components.ControlPlane.EtcdMain.Destroy,
        b.Shoot.Components.ControlPlane.EtcdEvents.Destroy,
    )(ctx)
}
```

**Consequence:** the etcd data (and any pending backup) is lost. No snapshot is taken
before destruction.

---

## 11. DNS Record Cleanup

DNS records are destroyed only when `nonTerminatingNamespace=true` (the seed namespace
still exists and is not in `Terminating` phase):

```go
destroyIngressDomainDNSRecord = g.Add(flow.Task{
    Name:   "Destroying nginx ingress DNS record",
    Fn:     botanist.DestroyIngressDNSRecord,
    SkipIf: !nonTerminatingNamespace,
})
destroyExternalDomainDNSRecord = g.Add(flow.Task{
    Fn:     botanist.DestroyExternalDNSRecord,
    SkipIf: !nonTerminatingNamespace,
})
destroyInternalDomainDNSRecord = g.Add(flow.Task{
    Fn:     botanist.DestroyInternalDNSRecord,
    SkipIf: !nonTerminatingNamespace,
})
```

These three tasks run in **parallel** with the extension-object deletion — they have no
dependency on it. If the namespace is already `Terminating`, they are skipped on the
assumption that they were already cleaned up in a previous attempt.

DNS `DNSRecord` extension objects are included in `DeleteExtensionObjects` via
`extensionsv1alpha1.ExtensionResource`, so any DNSRecord extension resources in the
seed are also removed through the `ForceDelete` actuator path on the DNS provider extension.

---

## 12. Bastion Handling

Bastions are `extensions.gardener.cloud/v1alpha1.Bastion` objects created when a user
starts an SSH session to a shoot node. They must be cleaned up before deletion can
complete.

**Normal deletion:** if bastions exist, the reconciler returns an error and waits for the
user to clean them up manually.

**Force deletion** (`reconciler.go:219`):

1. `patchBastions` iterates over all bastions for the shoot and adds
   `confirmation.gardener.cloud/force-deletion: "true"` to any that do not already have it.
2. Returns `RequeueAfter: 15s` to give the gardenlet's bastion reconciler time to react.
3. The **gardenlet bastion reconciler** (`pkg/gardenlet/controller/bastion/reconciler.go:208`)
   detects the annotation and propagates it to the seed-side `Bastion` extension object.
4. The **extension bastion reconciler**
   (`extensions/pkg/controller/bastion/reconciler.go:143`) calls `actuator.ForceDelete`
   instead of `actuator.Delete`, which releases the bastion without waiting for the
   underlying cloud resources.

Once bastions are gone the next reconcile sees `hasBastions=false` and proceeds to the
main force-delete flow.

---

## 13. Normal Delete vs Force Delete — Side-by-Side

| Step | Normal deletion | Force deletion |
|---|---|---|
| Start kube-apiserver | ✓ — scales up to serve drain requests | **Skipped** |
| Clean webhooks | ✓ — removes admission webhooks in shoot | **Skipped** |
| Clean extended APIs | ✓ — removes APIServices | **Skipped** |
| Clean shoot resources | ✓ — drains Pods, PVCs, Services via shoot API | **Skipped** |
| Deploy gardener-resource-manager | ✓ | **Skipped** |
| Destroy observability stack | ✓ — Prometheus, Plutono, Alertmanager, Logging | **Skipped** |
| Worker extension `Delete` | ✓ — MCM drains/destroys VMs | **Skipped** (`ForceDelete` is a no-op) |
| ControlPlane extension `Delete` | ✓ — waits for shoot MR cleanup | `ForceDelete` — skips waits |
| Infrastructure extension `Delete` | ✓ — waits for cloud resource teardown | `ForceDelete` — removes finalizers immediately |
| Network extension `Delete` | ✓ — waits for CNI teardown | `ForceDelete` |
| Machine resources | Graceful via MCM | **Direct finalizer strip + delete** |
| ManagedResources | `keepObjects` honoured | All set to `false`, finalizers stripped |
| Etcd | After full cleanup, possible backup | **Directly destroyed, no snapshot** |
| ShootState | Preserved until GEP-22 migration | **Deleted** at end of flow |
| Cloud VMs | Deprovisioned by provider | **Orphaned** (must be cleaned up manually) |

---

## 14. Admission & Validation

`ValidateForceDeletion` (`pkg/api/core/validation/shoot.go:3623`) is called from
`ValidateShootUpdate` on every Shoot update.

### Rules enforced

```
Rule 1: annotation cannot be removed once set
    old.force-deletion=true, new.force-deletion=false → Forbidden

Rule 2: DeletionTimestamp must be non-nil
    new.force-deletion=true, new.DeletionTimestamp=nil → Forbidden

Rule 3: at least one qualifying error code in status.lastErrors
    errorCodesAllowingForceDeletion = {
        ErrorInfraUnauthenticated,
        ErrorInfraUnauthorized,
        ErrorInfraDependencies,
        ErrorCleanupClusterResources,
        ErrorConfigurationProblem,
    }
    none present → Forbidden
```

### Typical operator workflow

```
1. kubectl delete shoot <name> -n <project-ns>
   ← DeletionTimestamp is set; normal deletion starts

2. Wait for shoot to enter Failed state with a qualifying error code.

3. kubectl annotate shoot <name> -n <project-ns> \
       confirmation.gardener.cloud/force-deletion=true
   ← Admission validates: DeletionTimestamp present ✓, error code present ✓
   ← Generation is incremented (see §15) — gardenlet reconciles immediately

4. Gardenlet picks up the new annotation → runForceDeleteShootFlow()

5. Force deletion completes → Shoot object is garbage-collected.
```

---

## 15. API Server Strategy: Generation Bump

`pkg/apiserver/registry/core/shoot/strategy.go:130`:

When the force-deletion annotation transitions from unset to `true`, the API server's
`PrepareForUpdate` strategy increments the shoot's `generation`. This matters because
the gardenlet only re-reconciles a shoot when its `generation` changes — if the shoot is
stuck in `Failed` state (which stops automatic requeues), the generation bump triggers
an immediate reconcile without the operator having to manually retry.

---

## 16. End-to-End Sequence

```
Operator        API Server      Gardenlet                    Seed
   │                │               │                           │
   │ delete shoot   │               │                           │
   ├──────────────▶│               │                           │
   │               │ DeletionTimestamp set                      │
   │               │ → gardenlet reconciles (normal delete)     │
   │               │──────────────▶│ runDeleteShootFlow()       │
   │               │               │ ... STUCK (infra error) ...│
   │               │               │                           │
   │ annotate force-deletion=true   │                           │
   ├──────────────▶│               │                           │
   │               │ validate ✓    │                           │
   │               │ generation++  │                           │
   │               │──────────────▶│ runForceDeleteShootFlow()  │
   │               │               │                           │
   │               │               │ DeleteExtensionObjects ──▶│ ForceDelete actuators
   │               │               │ WaitExtObjectsDeleted ◀───│ finalizers removed
   │               │               │                           │
   │               │               │ DestroyDNSRecords ────────▶│ DNS teardown
   │               │               │                           │
   │               │               │ DeleteMCM ────────────────▶│ MCM destroyed
   │               │               │ DeleteMachineResources ───▶│ strip finalizers, delete
   │               │               │ WaitMachineResourcesDel ◀─│ (VMs orphaned in cloud)
   │               │               │                           │
   │               │               │ SetKeepObjects=false ─────▶│ MRs updated
   │               │               │ DeleteManagedResources ───▶│ MRs deleted
   │               │               │                           │
   │               │               │ DeleteCluster ────────────▶│ Cluster resource gone
   │               │               │                           │
   │               │               │ DestroyEtcd ──────────────▶│ etcd-main + events gone
   │               │               │ WaitEtcdsDeleted ◀────────│
   │               │               │                           │
   │               │               │ DeleteKubernetesResources ▶│ ConfigMaps + Secrets gone
   │               │               │ DeleteNamespace ──────────▶│ shoot namespace deleted
   │               │               │ DeletePublicSAKeys ───────▶│ garden SA keys removed
   │               │               │ DeleteShootState ─────────▶│ ShootState deleted
   │               │               │                           │
   │               │               │ InvalidateShootClient      │
   │               │               │ finalizeShootDeletion      │
   │               │ removeGardenerFinalizer                    │
   │               │◀──────────────│                           │
   │ Shoot GC'd    │               │                           │
```

---

## 17. Constraints & Edge Cases

### Self-hosted shoots

`runForceDeleteShootFlow` returns `nil` immediately for self-hosted shoots. This causes
`finalizeShootDeletion` to run, removing the Gardener finalizer without any seed-side
cleanup. Self-hosted shoots are responsible for their own resource cleanup.

### Workerless shoots

MCM and machine-resource tasks are `SkipIf: botanist.Shoot.IsWorkerless`. All other
tasks run normally.

### Namespace already gone

If the seed namespace is absent (previous force-delete attempt cleaned it up), the setup
phase detects this (`checkIfSeedNamespaceExists`) and the flow graph is not run.
`finalizeShootDeletion` proceeds directly.

### Namespace terminating

`nonTerminatingNamespace = false` when the namespace exists but is already in
`Terminating` phase. The three DNS record tasks are skipped in this case — the namespace
controller will handle DNS resource cleanup as part of the termination cascade.

### Orphaned cloud resources

Force deletion **intentionally** leaves VMs and other cloud resources orphaned. The
operator must:
1. Note the shoot's technical ID (the seed namespace name, e.g. `shoot--project--name`).
2. Search the cloud provider console for resources tagged with that ID.
3. Delete them manually.

### ShootState is always deleted

Unlike normal deletion (which may preserve ShootState for hibernation/migration), force
deletion always runs `DeleteShootState`, removing the persisted etcd + extension state.

### No etcd backup

Force deletion calls `DestroyEtcd` directly — there is no snapshot step. Any
application data stored in etcd is lost.

---

## 18. Summary

Shoot force deletion provides a **guaranteed exit path** for stuck shoot clusters by
abandoning the graceful cleanup protocol and directly manipulating seed-side resources.

**What triggers it:**
- Operator annotates a deleting shoot with `confirmation.gardener.cloud/force-deletion=true`
- Preconditions: DeletionTimestamp set, qualifying permanent error code in status

**What the force-delete flow does:**
1. Calls each extension's `ForceDelete` actuator — extensions remove their finalizers
   and succeed immediately, even if underlying cloud resources cannot be reached.
2. Destroys MCM, then directly strips finalizers from all MCM objects (VMs are orphaned).
3. Sets `keepObjects=false` on all ManagedResources, then deletes them and removes their
   finalizers — gardener-resource-manager does not need to be running.
4. Deletes the `Cluster` resource and DNS records.
5. Destroys etcd-main and etcd-events without any snapshot.
6. Force-deletes remaining ConfigMaps and Secrets, then deletes the seed namespace.
7. Deletes public service-account keys from the Garden cluster.
8. Deletes the ShootState.
9. Removes the Gardener finalizer from the Shoot → Kubernetes GCs the Shoot object.

**What is accepted as collateral:**
- VMs provisioned by MCM remain in the cloud — manual cleanup required.
- Infrastructure resources (VPCs, subnets, security groups) remain — manual cleanup required.
- No etcd backup is taken before destruction.

**Key design decisions:**
- **No feature gate** — force deletion is always available once preconditions are met.
- **One-way annotation** — cannot be removed once set, preventing mid-flight reversal.
- **Generation bump** — setting the annotation triggers an immediate reconcile even from
  `Failed` state, without requiring a manual retry.
- **Self-hosted excluded** — force deletion is not meaningful for self-hosted shoots.
- **Bastions automatically force-deleted** — the operator does not need to manually clean
  up SSH bastions before force deletion proceeds.
