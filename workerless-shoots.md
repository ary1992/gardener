# Workerless Shoots in Gardener — Architecture & Deep Dive

A complete reference for **workerless shoots** — Gardener-managed Kubernetes clusters that have
**no worker nodes at all**. The entire value proposition is a fully-functional Kubernetes API server
(plus its backing etcd) without any compute nodes — no VMs, no Machines, no kubelets.

---

## Table of Contents

1. [Problem & Motivation](#1-problem--motivation)
2. [What Makes a Shoot Workerless](#2-what-makes-a-shoot-workerless)
3. [Components & Responsibilities](#3-components--responsibilities)
4. [Key API Resources & Types](#4-key-api-resources--types)
5. [High-Level Architecture](#5-high-level-architecture)
6. [Reconciliation Flow](#6-reconciliation-flow)
7. [kube-apiserver Adaptations](#7-kube-apiserver-adaptations)
8. [Secrets & PKI](#8-secrets--pki)
9. [Extension Handling](#9-extension-handling)
10. [Health Conditions](#10-health-conditions)
11. [Monitoring & Observability](#11-monitoring--observability)
12. [Validation & Constraints](#12-validation--constraints)
13. [Admission Plugins](#13-admission-plugins)
14. [Hibernation & Migration](#14-hibernation--migration)
15. [Summary](#15-summary)

---

## 1. Problem & Motivation

A regular Gardener shoot provisions a full Kubernetes cluster: infrastructure (VMs, networks, load
balancers), a control plane on the seed, and one or more pools of worker nodes that run workload
pods. That model is powerful but heavy — you need cloud credentials, an infrastructure extension,
a network plugin, MCM, VPN tunnels, and a host of node-related add-ons just to schedule a single
pod.

Sometimes you need **only the API**, not the nodes:

- **Extension operators** that use the shoot's API server as a storage/control backend without
  scheduling workloads onto it.
- **Gardener-managed operators** deployed inside the shoot API server that manage resources in
  *other* clusters via federated controllers.
- **Custom resource registries** — a shoot with only CRDs and controllers that live elsewhere.
- **Development/testing stubs** — a lightweight API server to explore RBAC, CRDs, or webhooks
  without paying for VMs.

A **workerless shoot** provides exactly that: a Kubernetes control plane (API server + etcd +
controller manager + resource manager) managed and reconciled by Gardener, with no infrastructure,
no nodes, and no node-related components.

---

## 2. What Makes a Shoot Workerless

A shoot is considered workerless when its `spec.provider.workers` list is **empty** (length zero).
There is no feature gate — the behavior is purely a function of the `Workers` field.

```go
// pkg/api/core/helper/shoot.go
func IsWorkerless(shoot *core.Shoot) bool {
    return len(shoot.Spec.Provider.Workers) == 0
}
```

The same helper exists for the versioned API (`pkg/api/core/v1beta1/helper/shoot.go`) and the
result is stored on the internal operation object at
`pkg/gardenlet/operation/shoot/types.go:97 — shoot.IsWorkerless`.

**Minimal valid workerless Shoot spec:**

```yaml
apiVersion: core.gardener.cloud/v1beta1
kind: Shoot
metadata:
  name: my-workerless
  namespace: garden-mynamespace
spec:
  secretBindingName: ""        # forbidden — omit entirely
  provider:
    type: ""                   # only the type is relevant; no workers
  kubernetes:
    version: "1.31.0"
  # spec.networking is optional
```

The key constraint is that `spec.provider.workers` is absent or empty. Because there are no workers
there is also no cloud account needed (`spec.secretBindingName` / `spec.credentialsBindingName` are
forbidden).

---

## 3. Components & Responsibilities

### 3.1 What IS deployed for a workerless shoot

| Component | Notes |
|---|---|
| **etcd** (main + events) | Identical to a regular shoot |
| **kube-apiserver** | Deployed with node-specific APIs/flags/certs removed — see §7 |
| **kube-controller-manager** | Deployed without CIDR/kubelet/node flags; no kubelet CA |
| **gardener-resource-manager** | Deployed with a subset of controllers and webhooks — see §3.2 |
| **DNS records** (internal + external) | Deployed normally |
| **Extensions** (`WorkerlessSupported: true` only) | Only auto-enabled extensions that explicitly declare `WorkerlessSupported: true` in their `ControllerRegistration` |
| **Shoot system resources** (subset) | No network-policy rules for DNS/kubelet; no RegistryCABundle |
| **Prometheus** (workerless ruleset) | Separate, smaller set of alert/recording rules |
| **Plutono** (workerless dashboards) | Workerless-specific dashboards; worker dashboards excluded |

### 3.2 What is NOT deployed

The following components return `nil` from their `Default*` functions when `IsWorkerless=true`,
meaning they are never instantiated or reconciled:

| Component | Botanist file |
|---|---|
| Cloud provider secret | `flow.go` — `SkipIf: IsWorkerless` |
| Infrastructure extension | `infrastructure.go` |
| ControlPlane extension | `controlplane.go` |
| Worker extension | `worker.go` |
| Network extension | `network.go` |
| MachineControllerManager | `machinecontrollermanager.go` |
| kube-scheduler | `kubescheduler.go` |
| VPA | `vpa.go` |
| ClusterAutoscaler | `clusterautoscaler.go` |
| ContainerRuntime extension | `containerruntime.go` |
| OperatingSystemConfig | `operatingsystemconfig.go` |
| VPN seed server | `vpnseedserver.go` |
| VPN shoot | `vpnshoot.go` |
| APIServerProxy | `apiserverproxy.go` |
| DependencyWatchdogAccess | `dependency_watchdog.go` |
| CoreDNS | `coredns.go` |
| kube-proxy | `kubeproxy.go` |
| NodeLocalDNS | `nodelocaldns.go` |
| MetricsServer | `metricsserver.go` |
| NodeProblemDetector | `nodeproblemdetector.go` |
| BlackboxExporter (cluster) | `blackboxexporter.go` |
| NginxIngress | `nginxingress.go` |
| KubernetesDashboard | `kubernetesdashboard.go` |

### 3.3 gardener-resource-manager: disabled controllers & webhooks

`disableControllersAndWebhooksForWorkerlessShoot` (
`pkg/component/gardener/resourcemanager/resource_manager.go:2193`) removes:

**Controllers disabled:**
- `CSRApprover` — approves kubelet CSRs (no kubelets)
- `NodeCriticalComponents` — manages node-critical daemonset scheduling (no nodes)

**Webhooks disabled:**
- `PodSchedulerName`, `SystemComponentsConfig`, `ProjectedTokenMount`
- `HighAvailabilityConfig`, `PodTopologySpreadConstraints`
- `KubernetesServiceHost`, `SeccompProfile`
- `PodKubeAPIServerLoadBalancing`, `VPAInPlaceUpdates`
- `NodeAgentAuthorizer` — node agents only run on worker nodes

---

## 4. Key API Resources & Types

### Shoot (`core.gardener.cloud/v1beta1`)

- `spec.provider.workers` — **empty list** → workerless
- `spec.provider.infrastructureConfig` — **forbidden**
- `spec.provider.controlPlaneConfig` — **forbidden**
- `spec.provider.workersSettings` — **forbidden**
- `spec.secretBindingName`, `spec.credentialsBindingName` — **forbidden**
- `spec.networking` — **optional** (required for normal shoots); `type`, `providerConfig`, `pods`, `nodes` fields **forbidden**
- `spec.kubernetes.kubeScheduler`, `.kubeProxy`, `.kubelet`, `.clusterAutoScaler`, `.verticalPodAutoScaler` — **forbidden**
- `spec.systemComponents` — **forbidden**
- `spec.addons` — **forbidden**
- `spec.maintenance.autoUpdate.machineImageVersion`, `.enableBasicAuthentication` — **forbidden**

### IsWorkerless detection function

```go
// Two variants — internal and versioned
func IsWorkerless(shoot *core.Shoot) bool         // pkg/api/core/helper/shoot.go:92
func IsWorkerless(shoot *gardencorev1beta1.Shoot) bool // pkg/api/core/v1beta1/helper/shoot.go:120
```

### ControllerRegistration (`core.gardener.cloud/v1beta1`)

- `spec.resources[].workerlessSupported bool` — must be `true` for an extension to be auto-enabled
  in a workerless shoot (`pkg/apis/core/v1beta1/types_controllerregistration.go:90`).

---

## 5. High-Level Architecture

```
                           GARDEN CLUSTER
 ┌─────────────────────────────────────────────────────────────────────┐
 │   Shoot spec                                                          │
 │   spec.provider.workers = []   ──▶  IsWorkerless = true              │
 │           │                                                           │
 │           ▼                                                           │
 │   Gardenlet (shoot reconciler)                                        │
 │           │  schedules the shoot onto a seed                          │
 │           ▼                                                           │
 └────────────────────────────────────────────────────────────────────-─┘
                            │
                            │ reconcile
                            ▼
                       SEED CLUSTER
 ┌────────────────────────────────────────────────────────────────────┐
 │                                                                      │
 │   ┌────────────────────────────────────────────────────────────┐    │
 │   │  Shoot control plane namespace (seed)                        │    │
 │   │                                                              │    │
 │   │   etcd-main  etcd-events   kube-apiserver                    │    │
 │   │   kube-controller-manager  gardener-resource-manager         │    │
 │   │   Prometheus (workerless rules) Plutono (workerless boards)  │    │
 │   │                                                              │    │
 │   │   NO: kube-scheduler, MCM, VPN, Infrastructure, ControlPlane │    │
 │   │   NO: Worker, Network, OSC, CRI, ClusterAutoScaler, VPA     │    │
 │   └────────────────────────────────────────────────────────────┘    │
 │                                                                      │
 └────────────────────────────────────────────────────────────────────┘
                            │
                            │  kube-apiserver endpoint exposed (DNS record)
                            ▼
                    WORKERLESS SHOOT CLUSTER
 ┌────────────────────────────────────────────────────────────────────┐
 │                                                                      │
 │   Kubernetes API  (no Nodes, no Pods running on nodes)               │
 │   Ready for: CRDs, webhooks, namespaces, RBAC, workload from         │
 │   remote controllers — but no local scheduler or node capacity       │
 │                                                                      │
 └────────────────────────────────────────────────────────────────────┘
```

---

## 6. Reconciliation Flow

The gardenlet's botanist builds a DAG of `TaskGroup`s. For workerless shoots the vast majority of
groups are gated with `SkipIf: b.Shoot.IsWorkerless`. The following groups run unchanged or with
minor modifications:

**Always executed:**
- Deploy namespaces
- Initialize secrets management (no kubelet/vpn/metrics-server CAs — see §8)
- Deploy etcd (main + events)
- Deploy kube-apiserver (with workerless adaptations)
- Deploy kube-controller-manager (without kubelet/CIDR flags)
- Deploy gardener-resource-manager (with reduced controllers/webhooks)
- Reconcile DNS records
- Deploy referenced resources
- Extensions before/after kube-apiserver (`WorkerlessSupported=true` only)
- Monitoring stack (workerless ruleset)

**Skipped entirely (`SkipIf: IsWorkerless`):**

| Task group | Reason |
|---|---|
| `DeployCloudProviderSecretTaskGroup` | No cloud credentials |
| `DeployMachineControllerManagerTaskGroup` | No machines |
| `ReconcileInfrastructureTaskGroup` | No cloud infrastructure |
| `ReconcileControlPlaneTaskGroup` | No provider ControlPlane extension |
| `ReconcileExtensionsAfterWorkerTaskGroup` | No Worker resource exists |
| `ReconcileContainerRuntimeTaskGroup` | No nodes to run CRI on |
| `DeployOperatingSystemConfigTaskGroup` | No nodes to configure |
| `ReconcileWorkerTaskGroup` | No worker pools |
| `DeployShootNetworkPluginTaskGroup` | No pod network needed |
| `DeployKubeProxyTaskGroup` | No nodes |
| `DeployCoreDNSTaskGroup` | No pods to do DNS for |
| `DeployNodeLocalDNSTaskGroup` | No nodes |
| `DeployMetricsServerTaskGroup` | No nodes/pods |
| `DeployNodeProblemDetectorTaskGroup` | No nodes |
| `DeployNginxIngressTaskGroup` | No load-balanced ingress |
| `ReconcileVPNComponentsTaskGroup` | No tunnel to nodes needed |

### 6.1 Sequence overview

```
Gardenlet
   │
   ├─ Deploy namespaces
   ├─ Initialize secrets management     (cluster CA, etcd CA, front-proxy CA — no kubelet/vpn CAs)
   ├─ Deploy etcd (main + events)
   ├─ Deploy extensions (WorkerlessSupported=true, before kube-apiserver)
   ├─ Deploy kube-apiserver             (node authorizer off, node APIs off by default)
   ├─ Deploy kube-controller-manager    (no kubelet flags, no CIDR allocation)
   ├─ Deploy gardener-resource-manager  (reduced controllers + webhooks)
   ├─ Deploy extensions (WorkerlessSupported=true, after kube-apiserver)
   ├─ Deploy DNS records
   └─ Deploy monitoring (workerless Prometheus rules + Plutono dashboards)
```

---

## 7. kube-apiserver Adaptations

The kube-apiserver is deployed with several node-specific capabilities removed
(`pkg/component/kubernetes/apiserver/`).

### 7.1 Authorization mode

For a regular shoot the authorization chain is `Node,RBAC`. For workerless it is **`RBAC` only**
(`authorization.go:53,94`): there are no kubelets, so the `Node` authorizer is irrelevant and is
dropped.

### 7.2 Disabled API groups

Node-oriented API groups are **disabled by default** (`deployment.go:439`):

| API group | Why disabled |
|---|---|
| `apps/v1` | No Deployments/DaemonSets to schedule onto nodes |
| `autoscaling/v2` | No HorizontalPodAutoscaler targets |
| `batch/v1` | No Jobs/CronJobs to run on nodes |
| `policy/v1` | No PodDisruptionBudgets / PodSecurityPolicies needed |
| `storage.k8s.io/v1/csinodes` | No CSI nodes |

Users may re-enable any of these explicitly via `spec.kubernetes.kubeAPIServer.runtimeConfig`
in the Shoot spec — the workerless defaults are merged with whatever the user specifies, with the
user's value winning.

### 7.3 Removed kubelet integration

- **No kubelet client certificate** generated (`secrets.go:160`).
- **No `kubernetes` DNS name** added to the server certificate SAN list (`secrets.go:144`).
- **No kubelet flags** passed to the API server: `handleKubeletSettings` returns `nil`
  (`deployment.go:1078`), so `--kubelet-certificate-authority`, `--kubelet-client-certificate`,
  `--kubelet-client-key`, and `--kubelet-preferred-address-types` are all absent.
- **No shoot ManagedResource** created by the apiserver component (`apiserver.go:476`).

### 7.4 kube-controller-manager adaptations

The controller manager runs but without node/kubelet-related configuration
(`controllermanager.go:675`):

- `--allocate-node-cidrs` — omitted (no pod network)
- `--cluster-cidr` — omitted
- `--node-cidr-mask-size` — omitted
- `--node-monitor-grace-period` — omitted
- `--horizontal-pod-autoscaler-*` flags — omitted
- `--cluster-signing-kubelet-*` flags — omitted
- No kubelet CA secret fetched or mounted (`controllermanager.go:218`)

---

## 8. Secrets & PKI

The secrets management layer generates a reduced set of CAs for workerless shoots
(`pkg/gardenlet/operation/botanist/secrets.go:152`).

### CAs always generated

| Secret name | Purpose |
|---|---|
| `ca` | Cluster CA (signs API server serving cert and client certs) |
| `ca-client` | Kubernetes client CA |
| `ca-etcd` | etcd client CA |
| `ca-etcd-peer` | etcd peer CA |
| `ca-front-proxy` | Front proxy (aggregation layer) CA |
| `ca-istio-basic-auth-server` | Istio basic auth CA |

### CAs NOT generated for workerless

| Secret name | Reason omitted |
|---|---|
| `ca-kubelet` | No kubelets |
| `ca-metrics-server` | No metrics server |
| `ca-vpn` | No VPN tunnel (self-hosted omits this too) |

The kubelet CA bundle is also **not synced back** to the Garden project namespace (the kubeconfig
that end-users get does not include a kubelet CA entry).

---

## 9. Extension Handling

### 9.1 Required extensions

For a normal shoot, the admission layer (`plugin/pkg/global/extensionvalidation/admission.go:283`)
requires that `ControlPlane`, `Infrastructure`, `Worker`, and `Network` extensions exist and are
registered. For a workerless shoot **none of these are required** — only DNS and user-specified
extensions are checked.

### 9.2 WorkerlessSupported flag

Extensions deployed into a workerless shoot must explicitly declare support via the
`ControllerRegistration`:

```yaml
apiVersion: core.gardener.cloud/v1beta1
kind: ControllerRegistration
spec:
  resources:
  - kind: Extension
    type: my-extension
    workerlessSupported: true   # ← required to be auto-enabled for workerless shoots
```

`pkg/apis/core/v1beta1/types_controllerregistration.go:90` — `WorkerlessSupported bool`.

At reconcile time (`pkg/component/shared/extension.go:96`), extensions without
`WorkerlessSupported: true` are silently skipped for workerless shoots.

### 9.3 Migration: no extension components

`shoot.go:475` — `GetExtensionComponentsForParallelMigration` returns an empty slice for
workerless shoots. There are no Infrastructure, ControlPlane, Worker, or Network extension
resources to migrate.

---

## 10. Health Conditions

For regular shoots, the care controller tracks four shoot conditions. For workerless shoots the
`ShootEveryNodeReady` condition is **never initialized or evaluated**
(`pkg/utils/gardener/shoot.go:828`):

| Condition | Regular shoot | Workerless shoot |
|---|---|---|
| `ShootAPIServerAvailable` | ✓ | ✓ |
| `ShootControlPlaneHealthy` | ✓ | ✓ |
| `ShootObservabilityComponentsHealthy` | ✓ | ✓ |
| `ShootSystemComponentsHealthy` | ✓ | ✓ |
| `ShootEveryNodeReady` | ✓ | **skipped** |

### Required control-plane deployments

`pkg/gardenlet/controller/shoot/care/health.go:1122` —
`ComputeRequiredControlPlaneDeployments` does not require kube-scheduler, MCM,
cluster-autoscaler, or VPA for workerless shoots.

### Extension health checks

The extension health check (`health.go:322`) only verifies `Extension` resources for workerless.
`ContainerRuntime`, `ControlPlane`, `Infrastructure`, `Network`, `OperatingSystemConfig`, and
`Worker` extension resources are not checked (they don't exist).

`kube-state-metrics` is also excluded from required monitoring deployments (`health.go:702`).

---

## 11. Monitoring & Observability

### 11.1 Prometheus rules

Prometheus uses a **separate, smaller rule set** for workerless shoots
(`pkg/component/observability/monitoring/prometheus/shoot/prometheusrules.go:88`).
The workerless rules live under `assets/prometheusrules/workerless/` and cover only control-plane
and API server alerts — no node, kubelet, or pod-scheduling rules.

### 11.2 Plutono dashboards

`pkg/component/observability/plutono/plutono.go:493` — for workerless shoots:
- Worker-specific dashboards are **excluded**.
- Workerless-specific dashboards are **included** instead.

### 11.3 Metrics label

Shoot reconciliation metrics (`pkg/gardenlet/metrics/metrics.go:28`) include a `"workerless"`
dimension label so operators can separate workerless reconcile latency from regular shoots in their
dashboards.

---

## 12. Validation & Constraints

### 12.1 Core validation (`pkg/api/core/validation/shoot.go`)

```
workerlessErrorMsg = "this field should not be set for workerless Shoot clusters"
```

**Fields forbidden for workerless:**

| Field | Validation function |
|---|---|
| `spec.kubernetes.kubeScheduler` | `validateKubernetesForWorkerlessShoot` (line 1237) |
| `spec.kubernetes.kubeProxy` | same |
| `spec.kubernetes.kubelet` | same |
| `spec.kubernetes.clusterAutoScaler` | same |
| `spec.kubernetes.verticalPodAutoscaler` | same |
| `spec.networking.type` | `validateNetworking` (line 1263) |
| `spec.networking.providerConfig` | same |
| `spec.networking.pods` | same |
| `spec.networking.nodes` | same |
| `spec.provider.infrastructureConfig` | `validateProvider` (line 2204) |
| `spec.provider.controlPlaneConfig` | same |
| `spec.provider.workersSettings` | same |
| `spec.systemComponents` | `ValidateSystemComponents` (line 3247) |
| `spec.addons` | `validateAddons` (line 667) |
| `spec.secretBindingName` | line 357 |
| `spec.credentialsBindingName` | line 357 |
| `spec.maintenance.autoUpdate.machineImageVersion` | line 2147 |
| `spec.maintenance.autoUpdate.sshKeypair` | line 2161 |

### 12.2 Immutability

`validateWorkerUpdate` (`shoot.go:503`) enforces that **you cannot switch between workerless and
non-workerless** after creation. The empty-workers / non-empty-workers state of a shoot is fixed
at creation time.

### 12.3 API version constraints

`pkg/utils/validation/apigroups/apigroups.go:295` — some API versions are marked
`RequiredForWorkerless: true`. Attempting to disable them via `spec.kubernetes.kubeAPIServer.runtimeConfig`
is rejected with a `Forbidden` error for workerless shoots. Examples of required APIs:
`networking.k8s.io/v1`, `rbac.authorization.k8s.io/v1`, `v1` (core).

### 12.4 ManagedSeed restriction

`plugin/pkg/managedseed/validator/admission.go:551` — a workerless shoot **cannot be used to
create a ManagedSeed**. Seeds need infrastructure and nodes.

---

## 13. Admission Plugins

Several admission plugins short-circuit for workerless shoots:

| Plugin | Behavior |
|---|---|
| `ShootValidator` (`plugin/pkg/shoot/validator`) | `validateShootNetworks`: pods CIDR only required for non-workerless (line 857) |
| `ShootMutator` (`plugin/pkg/shoot/mutator`) | Pod-network defaults skipped (line 306); for IPv6 workerless, auto-generates a random ULA `/112` CIDR for services (line 321) |
| `NodeLocalDNS` (`plugin/pkg/shoot/nodelocaldns`) | NodeLocalDNS default only applied for non-workerless (line 55) |
| `VPA` (`plugin/pkg/shoot/vpa`) | VPA default only applied for non-workerless (line 55) |
| `ExtensionValidator` (`plugin/pkg/global/extensionvalidation`) | Does not require ControlPlane/Infrastructure/Worker/Network extensions for workerless (line 283); validates that extensions used in workerless have `WorkerlessSupported: true` (line 418) |

---

## 14. Hibernation & Migration

### Hibernation

`pkg/gardenlet/operation/botanist/controlplane.go:85` — `HibernateControlPlane` skips several
steps that are irrelevant without nodes:
- **No node/pod/endpoint wait** — there are no Nodes or running Pods to drain.
- **No VolumeAttachment cleanup** — no persistent volumes attached to nodes.

The etcd and kube-apiserver shutdown sequence is the same as for a normal shoot.

### Shoot migration (seed-to-seed)

`cmd/gardenlet/app/migration.go:45` — VPN settings migration skips workerless shoots entirely
since they have no VPN components.

`GetExtensionComponentsForParallelMigration` returns an empty slice (`shoot.go:475`) — the
parallel migration loop has nothing to migrate for workerless shoots.

### Scheduler

`pkg/scheduler/controller/shoot/reconciler.go:643` — `networksAreDisjointed` skips the pods
network CIDR overlap check for workerless shoots (no pod network is defined or needed).

### Maintenance

`pkg/controllermanager/controller/shoot/maintenance/reconciler.go:124` — machine image
auto-update is skipped for workerless shoots during maintenance windows (no machine images, no
nodes).

### Network policy controller

`pkg/gardenlet/controller/networkpolicy/add.go:169` — reconciliation predicates handle workerless
differently: only the services CIDR is considered; pod and node CIDRs are absent.

---

## 15. Summary

A workerless shoot is a **Kubernetes control plane without any worker nodes**, useful when you need
only the API server and its ecosystem — CRDs, RBAC, webhooks, and extension controllers — without
the overhead of VMs, cloud infrastructure, or node-lifecycle management.

**What Gardener does for you:**

- Provisions and manages etcd, kube-apiserver, kube-controller-manager, and gardener-resource-manager
  on the seed, exactly as for a regular shoot.
- Rotates PKI, handles upgrades, manages hibernation, and reconciles health — just without the node
  half of the stack.

**What is stripped out:**

| Category | Not deployed |
|---|---|
| Cloud infrastructure | No Infrastructure extension, no cloud provider secret |
| Node provisioning | No Worker extension, no MCM, no OperatingSystemConfig |
| Node networking | No Network plugin, no kube-proxy, no CoreDNS, no NodeLocalDNS, no VPN |
| Node observability | No node-exporter, no metrics-server, no node-problem-detector |
| Node scheduling | No kube-scheduler, no VPA, no ClusterAutoscaler |
| Node PKI | No ca-kubelet, no ca-metrics-server, no ca-vpn |

**Key design points:**

1. **Detection is pure data** — `len(spec.provider.workers) == 0`. No feature gate, no annotation.
2. **Immutable** — you cannot add workers to a workerless shoot after creation, or remove all
   workers from a regular shoot.
3. **Extensions must opt in** — `ControllerRegistration.spec.resources[].workerlessSupported: true`
   is required; extensions not declaring support are never deployed.
4. **kube-apiserver is trimmed** — node APIs (`apps`, `batch`, `autoscaling`, `policy`,
   `storage.k8s.io/v1/csinodes`) are off by default, the Node authorizer is absent, and no
   kubelet client certificate is generated.
5. **Health monitoring is narrowed** — only four conditions are tracked; `ShootEveryNodeReady` is
   never set.
