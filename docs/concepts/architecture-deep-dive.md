---
title: Architecture Deep Dive
description: Detailed breakdown of Gardener architecture, networking, component interactions, controller-runtime usage, and Kubernetes API extensibility
categories:
  - Users
  - Developers
---

# Gardener Architecture Deep Dive

## The Core Concept: Kubernetes Manages Kubernetes

Gardener treats Kubernetes clusters as cattle, not pets. The **Garden cluster** (the management control plane) is itself a Kubernetes cluster, and all Shoot clusters are declaratively described as API objects within it. The key abstraction is that Gardener re-uses Kubernetes' own machinery — controllers, webhooks, CRDs, aggregated API servers — to manage other Kubernetes clusters.

```
┌────────────────────────────────────────────────────────────────────────┐
│  GARDEN CLUSTER (virtual cluster, backed by etcd on runtime cluster)    │
│                                                                          │
│  ┌──────────────────────┐  ┌────────────────┐  ┌──────────────────────┐ │
│  │ gardener-apiserver   │  │ gardener-ctrl- │  │ gardener-scheduler   │ │
│  │ (aggregated apisvr)  │  │ manager        │  │                      │ │
│  └──────────────────────┘  └────────────────┘  └──────────────────────┘ │
│         ▲                                                                 │
│  Serves: Shoot, Seed, CloudProfile,                                       │
│          Project, BackupBucket, etc.                                      │
└──────────────────────────┬─────────────────────────────────────────────┘
                           │ gardenlet watches Garden cluster objects
        ┌──────────────────┼──────────────────────┐
        ▼                  ▼                       ▼
  ┌─────────────┐   ┌─────────────┐         ┌─────────────┐
  │  SEED-A     │   │  SEED-B     │   ...   │  SEED-N     │
  │  cluster    │   │  cluster    │         │  cluster    │
  │ (gardenlet) │   │ (gardenlet) │         │ (gardenlet) │
  └─────┬───────┘   └─────────────┘         └─────────────┘
        │ Shoot control planes run as Pods in Seed namespaces
        ▼
  ┌─────────────────────────────────────┐
  │  shoot--project--clustername ns     │
  │  ┌──────────┐ ┌────┐ ┌──────────┐  │
  │  │ kube-api │ │etcd│ │scheduler │  │
  │  │  server  │ │    │ │ctrl-mgr  │  │
  │  └──────────┘ └────┘ └──────────┘  │
  └─────────────────────┬───────────────┘
                        │ VPN tunnel
                        ▼
              ┌──────────────────────┐
              │  SHOOT worker nodes  │
              │  (real VMs / nodes)  │
              └──────────────────────┘
```

---

## Runtime Cluster vs Virtual Garden Cluster

This is one of Gardener's most important architectural subtleties and the source of most confusion for newcomers.

### The Two Clusters

**Runtime cluster** is a pre-existing Kubernetes cluster where the `gardener-operator` runs. It is the physical substrate: it has real worker nodes, a real kube-apiserver, real etcd, real PVCs. The operator runs as a `Deployment` in the `garden` namespace here. All Gardener control plane processes (including the virtual garden's own API server) run as Pods on this cluster's nodes.

**Virtual garden cluster** is a synthetic Kubernetes cluster bootstrapped entirely *inside* the runtime cluster. It has no worker nodes of its own. Its sole purpose is to hold Gardener API resources: `Shoot`, `Seed`, `CloudProfile`, `Project`, etc. **This is the cluster users and gardenlets talk to.**

```
Runtime cluster (physical)
  garden namespace:
    ┌──────────────────────────────────────────────────────────┐
    │  virtual-garden-etcd-main       (StatefulSet, real PVC)  │
    │  virtual-garden-etcd-events     (StatefulSet, real PVC)  │
    │  virtual-garden-kube-apiserver  (Deployment)             │  ← Virtual Garden
    │  virtual-garden-kube-ctrl-mgr   (Deployment)             │    kube-apiserver
    │  virtual-garden-resource-mgr    (Deployment)             │
    │                                                           │
    │  gardener-apiserver             (Deployment, aggregated) │
    │  gardener-controller-manager    (Deployment)             │
    │  gardener-scheduler             (Deployment)             │
    │  gardener-admission-controller  (Deployment)             │
    │                                                           │
    │  etcd-druid                     (Deployment)             │
    │  istiod + IngressGateway        (Deployments)            │
    └──────────────────────────────────────────────────────────┘
```

### Why Two Clusters?

The virtual garden cluster needs its own identity, its own CA, its own RBAC, and its own etcd namespace — completely isolated from the runtime cluster. Users must never have access to the runtime cluster; the virtual garden is the only API surface they touch.

At the same time, running the virtual garden as Pods in the runtime cluster means: no extra VMs, Kubernetes manages availability and restarts, rolling updates work via standard Deployment mechanics, and persistent data lives in PVCs backed by the runtime cluster's storage.

### Separate etcd

The virtual garden kube-apiserver is started with:

```
--etcd-servers=https://virtual-garden-etcd-main-client.<garden-ns>.svc.cluster.local:2379
--etcd-servers-overrides=events.k8s.io/events#https://virtual-garden-etcd-events-client:2379,...
```

This is **entirely separate** from the runtime cluster's etcd. Virtual garden data (`Shoot` objects, `Seed` objects, etc.) is stored in `virtual-garden-etcd-main`. If the runtime cluster's etcd is wiped, the virtual garden etcd is unaffected (and vice versa). The etcd pods are managed by etcd-druid running in the runtime cluster.

### How Operator Bootstraps the Virtual Garden

The operator's `Garden` reconciler builds a flow graph with these ordered steps:

```
1. Generate CAs           → ca-garden-runtime (runtime cluster)
                          → ca-cluster        (virtual garden)
2. Deploy runtime infra   → etcd-druid, Istio, VPA, NGINX, resource-manager (runtime)
3. Deploy virtual etcds   → virtual-garden-etcd-main + virtual-garden-etcd-events
4. Wait for etcds         → (etcd-druid reconciles them into StatefulSets)
5. Deploy virtual APIServer Service  → LoadBalancer or ClusterIP
6. Deploy virtual kube-apiserver     → wired to etcd-main service address
7. Wait for virtual kube-apiserver   → health check passes
8. Deploy virtual kube-ctrl-mgr      → connects to virtual kube-apiserver
9. Deploy virtual resource-manager  → manages tokens inside virtual cluster
10. Deploy gardener-apiserver        → aggregated server in virtual cluster
11. Deploy gardener-{controller,scheduler,admission}
12. Initialize virtual cluster client → GardenClientMap.GetClient(...)
13. Deploy extensions, configure DNS records, etc.
```

### Communication Between Operator and Both Clusters

The operator Pod uses **two distinct clients**:

| Client | Config source | Target |
|---|---|---|
| `r.RuntimeClient` | In-cluster (`KUBECONFIG` env, mounted service account) | Runtime cluster kube-apiserver |
| `r.GardenClientMap.GetClient(...)` | Generated access secret (see kubeconfig section) | Virtual garden kube-apiserver |

The virtual garden kube-apiserver is reachable at `api.<virtual-cluster-domain>`, exposed via the Istio IngressGateway on the runtime cluster with SNI routing.

### Comparison Table

| Property | Runtime cluster kube-apiserver | Virtual garden kube-apiserver |
|---|---|---|
| Physical location | Native cluster infrastructure | Pod in `garden` namespace of runtime cluster |
| etcd | Runtime cluster's own etcd | `virtual-garden-etcd-main/events` Pods |
| CA | `ca-garden-runtime` | `ca-cluster` |
| Who talks to it | `gardener-operator`, etcd-druid, Istio | Users, gardenlets, gardener-{apiserver,controller-manager,scheduler} |
| External DNS | Internal cluster API | `api.<garden-domain>` via Istio IngressGateway |
| RBAC principals | Operator service account | Gardenlets (`system:seed:<name>`), users, Gardener components |

---

## Layer 1: The Garden Control Plane

### `gardener-apiserver` — Aggregated API Server

This is a **full Kubernetes-style API server** built using `k8s.io/apiserver`, not just a webhook or CRD. It registers with the runtime cluster's `kube-apiserver` via `APIService` objects so that `kubectl` speaking to the runtime cluster transparently reaches Gardener's own API groups:

```
APIService: v1beta1.core.gardener.cloud          → gardener-apiserver Service
APIService: v1alpha1.seedmanagement.gardener.cloud
APIService: v1alpha1.operations.gardener.cloud
APIService: v1alpha1.security.gardener.cloud
```

Objects like `Shoot`, `Seed`, `CloudProfile`, `Project`, `BackupBucket` are **stored in etcd** (the runtime cluster's etcd or a dedicated one), served by this aggregated server, and protected by built-in admission plugins registered in [`plugin/pkg/plugins.go`](../../plugin/pkg/plugins.go).

Admission plugins run **in-process** (not via webhooks) and include ~25 plugins: `ShootValidator`, `SeedValidator`, `ShootDNS`, `ShootQuotaValidator`, `ResourceReferenceManager`, `DeletionConfirmation`, `FinalizerRemoval`, and others.

### `gardener-controller-manager`

A controller-runtime manager watching Garden-cluster objects. Manages cross-cutting concerns:
- `Project` lifecycle (namespace creation, member RBAC)
- `CloudProfile` / `NamespacedCloudProfile` garbage collection
- `Seed` care (conditions, capacity)
- `ShootState` cleanup
- `ControllerRegistration` / `ControllerDeployment` lifecycle

### `gardener-scheduler`

Watches unscheduled `Shoot` objects (those with no `.spec.seedName`) and assigns them a Seed using strategies like `MinimumDistance` or `SameRegion`. Updates `Shoot.spec.seedName`. Simple reconcile loop via controller-runtime.

### `gardener-admission-controller`

Separate webhook server (also controller-runtime). Registers `MutatingWebhookConfiguration` and `ValidatingWebhookConfiguration` on the Garden cluster. Key webhooks:

| Webhook | Purpose |
|---|---|
| `SeedRestriction` | Gardenlets only see their own Seed's objects |
| `ShootRestriction` | Validates Shoot mutations based on project membership |
| `DomainValidation` | Internal domain secret integrity |
| `ResourceSizeLimit` | Rejects oversized API objects |
| `AuditPolicyValidation` | ConfigMap-based audit policy validity |

### `gardener-operator`

The **bootstrap operator** — it lives on the **runtime cluster** and reconciles the `Garden` CRD (`gardens.operator.gardener.cloud`). When a `Garden` object is created, the operator:

1. Deploys the virtual cluster etcd
2. Deploys `gardener-apiserver` (as an aggregated server)
3. Deploys `gardener-controller-manager`, `gardener-scheduler`, `gardener-admission-controller`
4. Wires up the `APIService` objects pointing to the aggregated server
5. Deploys `gardener-resource-manager` into the runtime cluster

This gives you a full Garden control plane from a single CRD. The operator's controllers are registered lazily — it uses a `controllerregistrar.Reconciler` that adds more controllers only after the virtual cluster is available.

---

## Layer 2: The Seed Layer — gardenlet

Every Seed cluster runs a **gardenlet** — essentially a kubelet for the Gardener control plane. It runs as a Deployment on the Seed cluster.

### Multi-Cluster Setup

The gardenlet operates across **two clusters simultaneously** using controller-runtime's multi-cluster support:

```go
// Primary manager: Seed cluster (watches extensions.gardener.cloud, resources.gardener.cloud)
mgr, _ := manager.New(seedRESTConfig, manager.Options{Scheme: SeedScheme})

// Secondary cluster: Garden cluster (watches Shoot, Seed, BackupBucket, etc.)
gardenCluster, _ := cluster.New(gardenRESTConfig, func(o *cluster.Options) {
    o.Scheme = GardenScheme
})
mgr.Add(gardenCluster)
```

Controllers use `gardenCluster.GetClient()` to read Garden objects and `mgr.GetClient()` to write to the Seed.

### Gardenlet Bootstrap

On first startup, gardenlet doesn't have Garden cluster credentials. It uses a **TLS bootstrap** mechanism similar to kubelet's:

1. Gardenlet starts with a bootstrap kubeconfig (limited permissions)
2. Creates a `CertificateSigningRequest` in the Garden cluster
3. The `gardener-controller-manager` approves it
4. Gardenlet receives the signed certificate, stores it as a Secret in the Seed cluster
5. Future restarts use this certificate — the `GardenKubeconfig` runnable rotates it

This is implemented in [`pkg/gardenlet/bootstrap/`](../../pkg/gardenlet/bootstrap/).

### Shoot Reconciliation Flow — The Botanist

The core gardenlet pattern for reconciling a Shoot is the **Botanist** ([`pkg/gardenlet/operation/botanist/`](../../pkg/gardenlet/operation/botanist/)):

- Each file in `botanist/` handles one Shoot sub-component: `etcd.go`, `kubeapiserver.go`, `worker.go`, `infrastructure.go`, `vpnseedserver.go`, `network.go`, `coredns.go`, etc.
- Each file exposes a `New<Component>(b *Botanist)` factory that wires component values from the Shoot spec + Seed config + secrets into a `component.Deployer` interface
- The flow graph assembles these into a dependency DAG using the flow package

The reconciliation has distinct flow phases:

1. **Infrastructure** — creates cloud resources (VPC, subnets) via the `Infrastructure` extension CRD
2. **ControlPlane (initial)** — provider CCM etc. via the `ControlPlane` extension CRD
3. **Kubernetes control plane** — deploys etcd, kube-apiserver, kube-controller-manager, kube-scheduler as Pods in the Seed
4. **VPN** — establishes the tunnel between Seed and Shoot
5. **Network** — deploys CNI (Calico/Cilium) via the `Network` extension CRD
6. **Worker** — creates MachineDeployments via `Worker` extension CRD → machine-controller-manager
7. **ControlPlane (final)** — after nodes are ready

### Extension CRDs in the Seed

The gardenlet deploys CRDs for the extension framework into Seed clusters. Extension providers (e.g., `provider-aws`, `provider-gcp`) run as controllers in the Seed and watch these CRDs:

```
extensions.gardener.cloud/v1alpha1:
  Infrastructure        ← cloud VPC/subnets/routes
  ControlPlane          ← CCM, CSI, provider components
  Worker                ← MachineDeployments (MCM)
  Network               ← CNI plugin
  OperatingSystemConfig ← node bootstrap scripts
  DNSRecord             ← DNS A/CNAME records
  BackupBucket          ← provider-specific backup storage
  BackupEntry           ← references into a BackupBucket
  Bastion               ← SSH jump host infrastructure
  Extension             ← generic lifecycle hook
  Cluster               ← read-only snapshot for extensions
```

The `Cluster` object is special — gardenlet writes a denormalized snapshot of `Shoot` + `Seed` + `CloudProfile` into the Seed cluster so extension providers can read everything they need without Garden cluster access.

---

## Layer 3: `gardener-resource-manager`

This is the **manifest applier**. It runs in every cluster that needs managed manifests (Garden runtime cluster, each Seed, each Shoot control plane namespace).

The `ManagedResource` CRD (`resources.gardener.cloud/v1alpha1`) is the key:

```yaml
kind: ManagedResource
spec:
  secretRefs:
    - name: shoot-core-coredns   # Secret containing Kubernetes YAML manifests
  injectLabels:
    resources.gardener.cloud/managed: "true"
```

The resource-manager controller:
1. Reads the referenced Secrets (which contain gzipped YAML of k8s objects)
2. Applies/updates each object to the target cluster
3. Tracks health conditions on the `ManagedResource` status
4. Garbage-collects objects that were removed from the Secret

Gardenlet generates Helm-rendered manifests → stores them in Secrets → creates `ManagedResource` objects. The resource-manager then applies them. This decouples "what should exist" from "make it exist."

The resource-manager also runs **webhooks** for Shoot cluster objects:
- `projected-token-mount` — injects short-lived tokens into Pods
- `high-availability-config` — sets replica counts for HA topology
- `system-components-config` — sets node selectors/tolerations
- `endpoint-slice-hints` — topology-aware routing hints

---

## API Types

### `core.gardener.cloud` — Primary API Group

Served by `gardener-apiserver` and stored in etcd. Versioned as `v1beta1` and `v1`.

| Type | Purpose |
|---|---|
| `Shoot` | The central resource; describes a managed Kubernetes cluster |
| `Seed` | Represents a Seed cluster; capacity, networking, backup config |
| `CloudProfile` | Provider capabilities: machine types, images, Kubernetes versions, regions |
| `NamespacedCloudProfile` | Namespaced variant of CloudProfile, inherits from a parent |
| `Project` | Organizational unit; namespace + members + tolerations |
| `ControllerRegistration` | Declares that a provider extension exists |
| `ControllerDeployment` | How to deploy a registered extension |
| `ControllerInstallation` | Tracks an installed extension on a specific Seed |
| `BackupBucket` | Object-store bucket for etcd backups |
| `BackupEntry` | A scoped backup entry within a BackupBucket |
| `SecretBinding` | Links a Shoot to cloud credentials via a Secret |
| `CredentialsBinding` | Links a Shoot to cloud credentials via WorkloadIdentity |
| `ExposureClass` | Named exposure strategy for Shoot API servers |
| `Quota` | Resource quota for projects |
| `ShootState` | Persists infrastructure state across hibernation cycles |
| `InternalSecret` | Garden-internal secret type |

Key `Shoot` spec fields:
```
ShootSpec:
  .dns              → DNSProvider + user-visible domain
  .networking       → Type (CNI), Pods/Services/Nodes CIDRs, IPFamilies
  .provider         → Type (cloud), ControlPlaneConfig, InfrastructureConfig, Workers[]
  .kubernetes        → version + KubeAPIServer/ControllerManager/Scheduler/Kubelet config
  .controlPlane     → HighAvailability config
  .extensions       → per-extension configs
  .hibernation      → schedule + enabled flag
```

### `seedmanagement.gardener.cloud`

| Type | Purpose |
|---|---|
| `ManagedSeed` | A Shoot that also acts as a Seed; bundles a Shoot ref and GardenletConfig |
| `ManagedSeedSet` | Set of ManagedSeeds with a template (analogous to StatefulSet) |
| `Gardenlet` | Configuration for the gardenlet deployment inside a ManagedSeed |

### `operations.gardener.cloud`

| Type | Purpose |
|---|---|
| `Bastion` | Short-lived SSH bastion host for Shoot node debugging |

### `security.gardener.cloud`

| Type | Purpose |
|---|---|
| `WorkloadIdentity` | OIDC-based workload identity |
| `CredentialsBinding` | Binding a WorkloadIdentity to cloud credentials |

### `operator.gardener.cloud`

| Type | Purpose |
|---|---|
| `Garden` | Bootstraps the entire Garden control plane on a runtime cluster |
| `Extension` | Operator-managed extensions for the Garden |

### `extensions.gardener.cloud/v1alpha1` — Seed-side Extension CRDs

Deployed into Seed clusters. Extension providers watch and reconcile these:

| Type | Purpose |
|---|---|
| `Infrastructure` | Cloud VPC, subnets, routes |
| `ControlPlane` | Provider-specific control-plane components (CCM, CSI) |
| `Worker` | Machine pool provisioning via machine-controller-manager |
| `Network` | CNI plugin configuration |
| `OperatingSystemConfig` | OS bootstrap config for worker nodes |
| `DNSRecord` | DNS record lifecycle |
| `BackupBucket` / `BackupEntry` | Provider-specific backup storage |
| `Bastion` | SSH bastion infrastructure |
| `Extension` | Generic lifecycle hook |
| `Cluster` | Denormalized snapshot of Shoot+Seed+CloudProfile for extensions |
| `ContainerRuntime` | Custom container runtime per worker pool |

### `resources.gardener.cloud/v1alpha1`

| Type | Purpose |
|---|---|
| `ManagedResource` | Points to Secrets with k8s manifests; resource-manager applies them |

---

## Networking Architecture

### The Central Challenge

A Shoot's **control plane runs in the Seed** (as Pods), but the **worker nodes run in the cloud** (different network, potentially different region). The kube-apiserver Pod must reach worker nodes (for exec/logs/metrics) and worker nodes must reach the kube-apiserver.

### VPN Tunnel

```
Seed cluster:
  shoot--project--name/vpn-seed-server (StatefulSet/Deployment)
    │  ← OpenVPN or WireGuard tunnel
    ▼
Shoot worker nodes:
  kube-system/vpn-shoot (DaemonSet)
```

Implemented in [`pkg/component/networking/vpn/seedserver/`](../../pkg/component/networking/vpn/seedserver/) and [`pkg/component/networking/vpn/shoot/`](../../pkg/component/networking/vpn/shoot/).

The tunnel is configured with the Shoot's pod/service/node CIDRs. Traffic from kube-apiserver to pod IPs routes through the tunnel. For **HA Shoots**, multiple VPN paths are used (one per zone) with load balancing.

An **Envoy proxy sidecar** (`apiserverproxy`) runs on Shoot nodes and proxies kube-apiserver traffic back through the tunnel, so `kubectl exec` works even when the Shoot API server is not directly reachable from the node.

### Istio-based API Server Exposure

Shoot API servers are exposed externally via **Istio** in the Seed:

```
External client
      │
      ▼  HTTPS/TLS
Istio IngressGateway (Seed, one per zone in HA)
      │  SNI routing (TLS passthrough)
      ▼
Shoot kube-apiserver Pod (in Seed namespace)
```

Istio uses **SNI-based routing** (`VirtualService` + `DestinationRule`) to route traffic to the correct Shoot's API server based on the TLS SNI header — so multiple Shoots share a single Load Balancer IP. This is configured in [`pkg/component/networking/istio/`](../../pkg/component/networking/istio/).

The `ExposureClass` API type allows different exposure strategies per Shoot (e.g., public vs. private, different ingress gateways).

### DNS

```
Shoot.spec.dns.domain: "my-cluster.example.com"
         │
         ▼
DNSRecord extension CRD (in Seed)
         │ watched by dns-provider extension
         ▼
Cloud DNS provider (Route53, Cloud DNS, etc.)
```

Two domain types:
- **External domain**: user-visible, points to the Istio IngressGateway Load Balancer IP
- **Internal domain**: `<uid>.internal.gardener.cloud`, used by worker nodes to reach the API server through the VPN tunnel

**CoreDNS** ([`pkg/component/networking/coredns/`](../../pkg/component/networking/coredns/)) runs in each Shoot cluster's `kube-system`. **NodeLocalDNS** ([`pkg/component/networking/nodelocaldns/`](../../pkg/component/networking/nodelocaldns/)) optionally runs as a DaemonSet on nodes for low-latency DNS.

### Network Policies

The gardenlet's `networkpolicy` controller ([`pkg/gardenlet/controller/networkpolicy/`](../../pkg/gardenlet/controller/networkpolicy/)) continuously reconciles `NetworkPolicy` objects in all Shoot namespaces in the Seed. This enforces:
- Control plane Pods in a Shoot namespace can only communicate with each other and the VPN
- No cross-Shoot traffic in the Seed

### CNI — Calico, Cilium, and the Network Extension Contract

Gardener has no built-in CNI implementation. Instead, it uses the `Network` extension CRD as a contract between gardenlet and an external CNI provider controller:

```
gardenlet creates:
  extensions.gardener.cloud/Network (in Seed, Shoot namespace)
    spec:
      type: "calico"            ← or "cilium", etc.
      podCIDR: "100.128.0.0/11"
      serviceCIDR: "100.96.0.0/11"
      providerConfig: <raw extension>

external provider controller (e.g. gardener-extension-networking-calico) watches:
  → deploys Calico DaemonSet + ConfigMap into the Shoot cluster
  → updates Network.status.lastOperation = Succeeded
```

The external CNI provider (e.g., `gardener-extension-networking-calico`, `gardener-extension-networking-cilium`) lives in a **separate repository** and runs as a controller in the Seed cluster. Gardenlet only writes the `Network` object; the provider does the actual CNI deployment.

The `Cluster` CRD gives the provider controller access to the full `Shoot`, `Seed`, and `CloudProfile` specs without needing Garden cluster credentials.

### NGINX Ingress

NGINX runs in **three distinct roles** across the Gardener landscape:

**1. Seed-level NGINX (deployed by gardenlet)**

The gardenlet deploys an NGINX Ingress controller into the Seed cluster's `kube-system` namespace ([`pkg/gardenlet/controller/seed/seed/components.go`](../../pkg/gardenlet/controller/seed/seed/components.go)). This serves HTTP `Ingress` resources for Seed-internal services (Plutono dashboards, Alertmanager, etc.). Its external address is used as the target for Seed-level DNS records.

It coexists with Istio in the Seed: **Istio handles TLS passthrough for Shoot API servers** (L4 SNI routing), while **NGINX handles HTTP ingresses** for observability UIs (L7 routing). They use different ports and different `IngressClass` values (`nginx` vs Istio's gateway).

Can be disabled with the `DisableNginxIngressInSeed` feature gate.

**2. Garden/runtime-cluster NGINX (deployed by operator)**

The operator deploys the same NGINX component into the runtime cluster for Garden-level services ([`pkg/operator/controller/garden/garden/components.go`](../../pkg/operator/controller/garden/garden/components.go)). Can be disabled with `DisableNginxIngressInGarden`.

**3. Shoot addon NGINX (optional, deployed into Shoot clusters)**

When `Shoot.spec.addons.nginxIngress.enabled: true`, gardenlet deploys an NGINX Ingress controller directly into the Shoot cluster's `kube-system` as a Shoot addon ([`pkg/gardenlet/operation/botanist/nginxingress.go`](../../pkg/gardenlet/operation/botanist/nginxingress.go)). This is for end-user workloads running in the Shoot — not for Gardener's own services. Note that `spec.addons` is deprecated.

---

## Communication Flows: Seed, Shoot, and Workloads

This section traces every communication path between the three tiers.

### 1. User → Shoot API Server

```
User (kubectl)
  │  HTTPS, SNI: api.<shoot-domain>
  ▼
Cloud Load Balancer (Seed)
  │  TCP passthrough (L4)
  ▼
Istio IngressGateway (Seed)
  │  SNI-based TLS passthrough → routes by server name
  ▼
kube-apiserver Pod (in Shoot namespace, Seed cluster)
  │  TLS terminates here — certificate signed by Shoot cluster CA
  ▼
etcd (in same Shoot namespace, managed by etcd-druid)
```

The SNI header on the TLS ClientHello is what Istio reads. Multiple Shoots share a single Load Balancer IP; Istio routes each connection to the correct namespace based on the full server name.

### 2. Shoot kube-apiserver → Shoot Worker Node (exec/logs/port-forward)

```
kube-apiserver Pod (Seed)
  │  HTTPS to pod/node IP — these IPs are in the Shoot's pod/node CIDR
  ▼
vpn-seed-server (same Seed namespace)
  │  tunneled through OpenVPN/WireGuard
  ▼
vpn-shoot (DaemonSet in Shoot kube-system)
  │  routes to pod/node IPs inside the Shoot network
  ▼
Worker node / Pod
```

The Shoot pod and node CIDRs are routed exclusively through the VPN. The kube-apiserver never has a direct route to the worker network.

### 3. Worker Node → Shoot API Server (kubelet, kube-proxy)

```
kubelet / kube-proxy (on worker node)
  │  HTTPS to internal DNS: api.<uid>.internal.gardener.cloud
  ▼
apiserverproxy (Envoy sidecar on each node, kube-system DaemonSet)
  │  proxies to the Seed's VPN tunnel endpoint
  ▼
vpn-shoot → vpn-seed-server (reverse direction)
  │
  ▼
kube-apiserver Pod (Seed)
```

Workers never have a direct route to the kube-apiserver Pod's IP. The `apiserverproxy` ([`pkg/component/networking/apiserverproxy/`](../../pkg/component/networking/apiserverproxy/)) intercepts all traffic to the API server's internal DNS name and proxies it through the VPN tunnel.

The internal domain (`api.<uid>.internal.gardener.cloud`) resolves to the loopback/node-local address where `apiserverproxy` listens, so the proxy is always on the data path.

### 4. Gardenlet → Virtual Garden (watches, status updates)

```
Gardenlet (running in Seed cluster)
  │  HTTPS, X.509 client cert [kubeconfig 2]
  ▼
Virtual garden kube-apiserver
  │  (exposed via Istio IngressGateway on runtime cluster)
  ▼
virtual-garden-etcd-main
```

The gardenlet watches `Shoot`, `Seed`, `BackupBucket`, etc. from the virtual garden cluster using `gardenCluster.GetCache()`. Status updates (e.g., `Shoot.status.lastOperation`) are written back via `GardenClient.Status().Update(...)`.

### 5. Gardenlet → Seed Cluster (extension CRDs, ManagedResources)

```
Gardenlet (primary manager client)
  │  in-cluster service account token (gardenlet runs inside the Seed)
  ▼
Seed cluster kube-apiserver
  │
  ▼
Extension CRDs, ManagedResource objects, Secrets
```

The gardenlet's `manager.New(seedRESTConfig, ...)` uses in-cluster config. Extension providers and gardener-resource-manager then pick up the CRDs and Secrets.

### 6. Extension Provider → Shoot Cluster (CNI, CCM, CSI)

```
Extension provider controller (e.g. provider-aws, running in Seed)
  │  reads Cluster CRD (Shoot+Seed+CloudProfile snapshot)
  │  reads Infrastructure/Worker/ControlPlane/Network CRDs
  │
  ├──► Cloud API (AWS/GCP/Azure) — creates VMs, VPCs, routes
  │
  └──► Shoot cluster kube-apiserver (via generic token kubeconfig [3] + shoot-access token [4])
         │  deploys CCM, CSI DaemonSets, CNI DaemonSets into kube-system
```

### 7. Workload (Pod in Shoot) → External / Internet

```
Pod in Shoot worker node
  │  traffic is pod CIDR (source NAT by CNI or cloud router)
  ▼
Cloud VPC routing (managed by Infrastructure extension / CCM)
  │
  ▼
Internet / Cloud services
```

This is entirely managed by the CNI and the cloud provider. The VPN tunnel is **only** for control-plane ↔ worker communication; user workload egress bypasses it entirely.

### 8. Seed Cluster Internal: Control Plane Namespace Isolation

Each Shoot gets its own namespace in the Seed (`shoot--<project>--<name>`). The gardenlet's `networkpolicy` controller enforces:

```
shoot--project--A namespace:
  kube-apiserver Pod ──[NetworkPolicy: allow]──► vpn-seed-server Pod
  kube-apiserver Pod ──[NetworkPolicy: allow]──► etcd Pod (same namespace)
  kube-apiserver Pod ──[NetworkPolicy: DENY]───► shoot--project--B namespace
```

This prevents a compromised component in one Shoot's control plane from reaching another Shoot's etcd or kube-apiserver.

---

## Supporting Infrastructure Components

### etcd-druid — etcd Lifecycle Manager

Gardenlet does **not** manage etcd Pods directly. Instead, it creates `Etcd` CRDs (from `etcd.druid.gardener.cloud/v1alpha1`) in the Seed, and **etcd-druid** reconciles them into actual etcd StatefulSets.

```
gardenlet creates:
  etcd.druid.gardener.cloud/Etcd  (e.g., "etcd-main", "etcd-events")
    spec:
      replicas: 1 (or 3 for HA)
      backup: { store: s3://... }
      tls: { ... }

etcd-druid watches Etcd objects and manages:
  → StatefulSet for etcd Pods
  → ConfigMap for etcd config
  → Services (client + peer)
  → TLS secrets
  → Backup schedule (via EtcdCopyBackupsTask)
  → Defragmentation CronJob
```

etcd-druid runs as a Deployment in the Seed cluster. It also handles etcd **backup and restore**: the `EtcdCopyBackupsTask` CRD triggers a job that copies etcd snapshots to/from the configured object-store backend (`BackupBucket`/`BackupEntry`).

Source: [`pkg/component/etcd/`](../../pkg/component/etcd/)

### machine-controller-manager (MCM) — Node Provisioning

MCM runs as a Deployment in the Shoot's control-plane namespace in the Seed. It watches `Machine`, `MachineDeployment`, and `MachineSet` objects (from `machine.sapcloud.io/v1alpha1` CRDs) and creates/deletes actual cloud VMs.

```
gardenlet creates (via Worker extension CRD):
  Worker extension CRD → external provider (e.g. provider-aws) creates:
    MachineDeployment  →  MCM creates:  MachineSet  →  Machine
                                                           │
                                              MCM calls cloud API
                                              to create/delete VMs
```

The cloud provider implementation for MCM (the part that calls AWS/GCP/Azure APIs) lives in external repos (e.g., `machine-controller-manager-provider-aws`). MCM itself is provider-agnostic.

Once a VM is created, MCM bootstraps it: the node gets an `OperatingSystemConfig` secret injected by the `gardener-node-agent`.

### gardener-node-agent — On-Node Configuration Agent

`gardener-node-agent` runs as a **systemd service** on each Shoot worker node (not as a Pod). It is the first process to run on a new node during bootstrap.

Its responsibilities:
1. Watches a `Secret` in the Seed that contains the rendered `OperatingSystemConfig` for its node
2. Applies **systemd units** (kubelet service, containerd config, etc.) from the OSC to the host
3. Configures containerd, kubelet flags, and node-specific settings
4. Manages its own **certificate rotation** (similar to gardenlet's bootstrap)
5. Monitors kubelet and containerd health via the `healthcheck` controller
6. Updates the Shoot's `Node` object with labels/annotations

```
Seed cluster:
  shoot namespace / Secret (OperatingSystemConfig rendered by gardenlet)
         │  watched by node-agent (from the node)
         ▼
  Worker node:
    /etc/systemd/system/kubelet.service
    /etc/containerd/config.toml
    /var/lib/kubelet/config/...
```

Source: [`pkg/nodeagent/controller/`](../../pkg/nodeagent/controller/)

### VPA — Vertical Pod Autoscaler

VPA is deployed by gardenlet into each Seed cluster. It right-sizes **Shoot control-plane components** running in the Seed:

- `kube-apiserver` Pod memory/CPU
- `etcd` Pod memory/CPU
- `kube-controller-manager`, `kube-scheduler`, `vpn-seed-server`

Gardenlet creates `VerticalPodAutoscaler` objects alongside each control-plane Deployment. VPA's recommender analyzes historical usage and its admission webhook mutates Pod resource requests at creation time.

VPA also runs inside each Shoot cluster for user workloads, deployed via `ManagedResource`.

Source: [`pkg/component/autoscaling/vpa/`](../../pkg/component/autoscaling/vpa/)

### Observability Stack

Each Seed gets a full observability stack, deployed by gardenlet, for monitoring Shoot control planes:

| Component | Role |
|---|---|
| **Prometheus / VictoriaMetrics** | Scrapes metrics from Shoot control-plane Pods in the Seed |
| **Alertmanager** | Routes firing alerts (per-Shoot and per-Seed alert rules) |
| **Plutono** | Gardener's Grafana fork; dashboards for Shoot and Seed health |
| **Loki** | Log aggregation for Shoot control-plane Pod logs |
| **OpenTelemetry Collector** | Traces from control-plane components |

These are deployed via `ManagedResource` objects into the Seed. Access is behind the Seed-level NGINX Ingress, protected by basic auth (via `IstioBasicAuthServer` for the Istio path).

Source: [`pkg/component/observability/`](../../pkg/component/observability/)

---

## How controller-runtime Is Used

Every binary follows this pattern (example from [`cmd/gardenlet/app/app.go`](../../cmd/gardenlet/app/app.go)):

```go
// 1. Create manager (primary cluster — the Seed)
mgr, _ := manager.New(seedRESTConfig, manager.Options{
    Scheme:           kubernetes.SeedScheme,
    LeaderElection:   true,
    LeaderElectionID: "gardenlet-leader-election",
})

// 2. Add secondary cluster (the Garden)
gardenCluster, _ := cluster.New(gardenRESTConfig, ...)
mgr.Add(gardenCluster)

// 3. Add bootstrappers (run before controllers start)
mgr.Add(&bootstrappers.GardenKubeconfig{...})

// 4. Register all controllers
gardenletcontroller.AddToManager(ctx, mgr, cfg, gardenCluster, ...)

// 5. Start
mgr.Start(ctx)
```

Each controller implements `reconcile.Reconciler`:

```go
type Reconciler struct {
    GardenClient client.Client  // from gardenCluster
    SeedClient   client.Client  // from mgr
    Config       *config.GardenletConfiguration
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
    shoot := &gardencorev1beta1.Shoot{}
    r.GardenClient.Get(ctx, req.NamespacedName, shoot)
    // ...
}
```

The **multi-cluster watch** is the key pattern — controllers watch objects in the Garden cluster's cache but reconcile using both Garden and Seed clients:

```go
ctrl.NewControllerManagedBy(mgr).
    Named("shoot").
    WithOptions(controller.Options{MaxConcurrentReconciles: cfg.ConcurrentSyncs}).
    WatchesRawSource(source.Kind(gardenCluster.GetCache(), &gardencorev1beta1.Shoot{})).
    Complete(r)
```

### Scheme Split

| Scheme | Contains |
|---|---|
| `GardenScheme` | `core.gardener.cloud` (v1+v1beta1), `seedmanagement`, `operations`, `security`, `apiregistration`, core k8s types |
| `SeedScheme` | `extensions.gardener.cloud/v1alpha1`, `resources.gardener.cloud/v1alpha1`, `operator.gardener.cloud/v1alpha1`, VPA, etcd-druid, machine-controller-manager, Istio, Fluent Bit, Prometheus Operator, OpenTelemetry, `apiextensions` |
| `ShootScheme` | Core k8s, `apiextensions`, `apiregistration`, VPA, `metrics.k8s.io`, volume-snapshots |

---

## Kubernetes API Extensibility Features

| Feature | How Gardener Uses It |
|---|---|
| **Aggregated API Server** | `gardener-apiserver` serves all Gardener-core types via `APIService` objects registered with `kube-aggregator` |
| **CRDs** | Extension framework types (`Infrastructure`, `Worker`, `Network`, etc.) in Seeds; `ManagedResource` everywhere; `Garden` CRD on runtime cluster |
| **Admission Webhooks** | `gardener-admission-controller` (Garden cluster) + `gardener-resource-manager` (every cluster) |
| **In-process Admission Plugins** | 25+ plugins in `gardener-apiserver` for business-logic validation without round-trips |
| **APIService / kube-aggregator** | Gardener API groups registered as `APIService` objects pointing to `gardener-apiserver` |
| **CertificateSigningRequest** | Gardenlet TLS bootstrap — gets signed certs from the Garden cluster CA |
| **TokenRequest / Projected Tokens** | Resource-manager webhook injects short-lived tokens; `WorkloadIdentity` OIDC flow |
| **SubjectAccessReview** | `SeedRestriction` webhook checks if a gardenlet is allowed to access specific objects |
| **LeaderElection via Leases** | Every controller binary uses `coordination.k8s.io/Lease` for HA leader election |
| **Server-Side Apply** | Resource-manager uses SSA for conflict-safe manifest application |
| **Field Owner** | Each controller uses a distinct field manager name for SSA conflict resolution |

### In-process Admission Plugins (full list)

Registered in [`plugin/pkg/plugins.go`](../../plugin/pkg/plugins.go):

`NamespaceLifecycle`, `ResourceReferenceManager`, `ExtensionValidator`, `ExtensionLabels`, `ShootTolerationRestriction`, `ShootExposureClass`, `ShootDNS`, `ShootManagedSeed`, `ShootNodeLocalDNSEnabledByDefault`, `ShootDNSRewriting`, `ShootQuotaValidator`, `ShootMutator`, `ShootValidator`, `SeedValidator`, `SeedMutator`, `ControllerRegistrationResources`, `NamespacedCloudProfileValidator`, `ProjectMutator`, `DeletionConfirmation`, `FinalizerRemoval`, `CustomVerbAuthorizer`, `ShootVPAEnabledByDefault`, `ShootResourceReservation`, `ManagedSeed`, `ManagedSeedShoot`, `Bastion`, `BackupBucketValidator`

### Admission Webhooks (`gardener-admission-controller`)

| Path | Type | Purpose |
|---|---|---|
| `/webhooks/auth/seed` | AuthZ | Authorization for Seed access |
| `/webhooks/auth/shoot` | AuthZ | Authorization for Shoot access |
| `/webhooks/admission/seedrestriction` | Validating | Gardenlets only see their own Seed's objects |
| `/webhooks/admission/shootrestriction` | Validating | Shoot access based on project membership |
| `/webhooks/audit-policies` | Validating | Audit policy ConfigMap validation |
| `/webhooks/authorization-configuration` | Validating | Authorization config validation |
| `/webhooks/authentication-configuration` | Validating | Authentication config validation |
| `/webhooks/admission/shootserviceaccounts` | Mutating | Shoot service account defaults |
| `/webhooks/validate-internal-domain` | Validating | Internal domain secret integrity |
| `/webhooks/validate-resource-size` | Validating | Rejects oversized API objects |
| `/webhooks/validate-namespace-deletion` | Validating | Namespace deletion protection |
| `/webhooks/validate-kubeconfig-secrets` | Validating | Kubeconfig secrets integrity |
| `/webhooks/sync-provider-secret-labels` | Mutating | Provider secret label sync |
| `/webhooks/update-restriction` | Validating | Update restriction enforcement |

---

## Component Interaction Summary

```
User
  │ kubectl / Gardener Dashboard
  ▼
kube-apiserver (runtime cluster)
  │  routes *.gardener.cloud groups via APIService
  ▼
gardener-apiserver
  │  stores Shoot/Seed/CloudProfile/etc. in etcd
  │  runs admission plugins in-process
  │
  ├──► gardener-admission-controller (webhooks on Garden cluster)
  ├──► gardener-controller-manager (Projects, Seeds, CloudProfiles)
  ├──► gardener-scheduler (assigns Shoots to Seeds)
  │
  └──► gardenlet (per Seed, watches Shoot/Seed/BackupBucket)
         │  Creates/updates in Seed cluster:
         ├──► Etcd CRDs → etcd-druid manages etcd StatefulSets + backup
         ├──► extension CRDs (Infrastructure, Worker, Network, ControlPlane, ...)
         │       ├── provider-aws/gcp/azure watches Infrastructure, Worker, ControlPlane
         │       ├── provider-calico/cilium watches Network → deploys CNI into Shoot
         │       └── provider-aws/etc. creates MachineDeployments → MCM creates VMs
         ├──► ManagedResources (via resource-manager)
         │       └── resource-manager applies Helm-rendered manifests
         ├──► Shoot control-plane Pods (kube-apiserver, kube-controller-manager, kube-scheduler)
         ├──► VPN components (vpn-seed-server ↔ vpn-shoot on nodes)
         ├──► Istio IngressGateway (routes external traffic to Shoot API servers via SNI)
         ├──► NGINX Ingress (HTTP routing for Seed-internal observability UIs)
         ├──► Observability (Prometheus, Alertmanager, Plutono, Loki per Seed)
         ├──► VPA (right-sizes Shoot control-plane Pods)
         ├──► NetworkPolicies (isolate Shoot namespaces in Seed)
         └──► OperatingSystemConfig secrets → gardener-node-agent on each VM
```

---

## Kubeconfigs in Gardener

Gardener uses at least eight distinct kubeconfigs, each with a different scope, issuer, consumer, and rotation strategy. Confusing them is a frequent source of debugging pain.

### Overview

```
Virtual Garden
  kube-apiserver
       │
       ├── [1] Gardenlet bootstrap kubeconfig   (short-lived, bootstrap token)
       ├── [2] Gardenlet garden kubeconfig       (X.509 cert, rotated via CSR)
       └── [7] Operator access secret kubeconfig (token, managed by resource-mgr)

Shoot cluster
  kube-apiserver
       │
       ├── [3] Generic token kubeconfig          (CA bundle + token path, per Shoot)
       ├── [4] Shoot access secrets              (ServiceAccount tokens, per component)
       ├── [5] Static user kubeconfig            (static bearer token, optional)
       └── [6] Admin kubeconfig (on-demand)      (short-lived X.509 cert)

Virtual Garden
  kube-apiserver (in-cluster)
       │
       └── [7] Virtual garden generic token kubeconfig (CA + token path)
```

---

### [1] Gardenlet Bootstrap Kubeconfig

| | |
|---|---|
| **Location** | Secret in Seed/runtime cluster (e.g., `gardenlet-kubeconfig-bootstrap` in `garden` ns) |
| **Creator** | Operator or admin during gardenlet installation; uses `ComputeGardenletKubeconfigWithBootstrapToken` / `ComputeGardenletKubeconfigWithServiceAccountToken` in [`pkg/gardenlet/bootstrap/util/util.go`](../../pkg/gardenlet/bootstrap/util/util.go) |
| **Consumer** | Gardenlet, exclusively during startup (`GardenKubeconfig.Start` in [`pkg/gardenlet/bootstrappers/garden_kubeconfig.go`](../../pkg/gardenlet/bootstrappers/garden_kubeconfig.go)) |
| **Points to** | Virtual garden kube-apiserver |
| **Credential** | Short-lived bootstrap token from `kube-system/bootstrap-token-*` in the virtual garden; grants only `gardener.cloud:system:seed-bootstrapper` ClusterRole |
| **Rotation** | One-time use; deleted by `DeleteBootstrapAuth` after CSR is approved and [2] is issued |

The gardenlet uses this kubeconfig solely to submit a `CertificateSigningRequest`. Once the CSR is approved by `gardener-controller-manager`, this secret is deleted.

---

### [2] Gardenlet Garden Kubeconfig (Post-Bootstrap)

| | |
|---|---|
| **Location** | Secret in Seed/runtime cluster (e.g., `gardenlet-kubeconfig` in `garden` ns); path configured in `GardenletConfiguration.gardenClientConnection.kubeconfigSecret` |
| **Creator** | Gardenlet bootstrap process: submits a CSR to the virtual garden, waits for approval, stores the signed cert+key via `UpdateGardenKubeconfigSecret` in [`pkg/gardenlet/bootstrap/util/util.go`](../../pkg/gardenlet/bootstrap/util/util.go) |
| **Consumer** | Gardenlet for all ongoing communication with the virtual garden cluster (watching `Shoot`, `Seed`, `BackupBucket`, etc.) |
| **Points to** | Virtual garden kube-apiserver |
| **Credential** | X.509 client certificate: `CN=system:seed:<seed-name>`, `O=system:seeds` |
| **Rotation** | Automated by `Manager.ScheduleCertificateRotation` in [`pkg/gardenlet/bootstrap/certificate/certificate_rotation.go`](../../pkg/gardenlet/bootstrap/certificate/certificate_rotation.go). Re-issues a new CSR before expiry. Can be triggered immediately by annotating the secret with `gardener.cloud/operation: renew`. CA rotation triggers re-issuance across all Seeds. |

On every startup, gardenlet also calls `UpdateGardenKubeconfigCAIfChanged`: it reads `kube-root-ca.crt` from the virtual garden and refreshes the CA bundle in the stored kubeconfig if it changed (handles silent CA rotation).

---

### [3] Generic Token Kubeconfig (per Shoot control plane)

| | |
|---|---|
| **Location** | `generic-token-kubeconfig-<hash>` Secret in the Shoot's control-plane namespace in the Seed. Name tracked via annotation `generic-token-kubeconfig.secret.gardener.cloud/name` on the `Cluster` object. |
| **Creator** | Gardenlet via `tokenrequest.GenerateGenericTokenKubeconfig` in [`pkg/utils/gardener/tokenrequest/secrets.go`](../../pkg/utils/gardener/tokenrequest/secrets.go) |
| **Consumer** | All Shoot control-plane components that talk to the Shoot cluster: kube-controller-manager, kube-scheduler, machine-controller-manager, etcd backup-restore, observability components. Injected via `gardenerutils.InjectGenericKubeconfig`. |
| **Points to** | Shoot cluster kube-apiserver (in-cluster `svc.cluster.local` address by default) |
| **Credential** | Contains only the Shoot cluster CA bundle + a **token file path** (`/var/run/secrets/gardener.cloud/shoot/generic-kubeconfig/token`). The actual token is populated by gardener-resource-manager's token-requestor controller from the corresponding `shoot-access-*` Secret ([4]). |
| **Rotation** | `KeepOld` strategy on CA rotation (old kubeconfig kept to allow smooth rollover) |

This kubeconfig is intentionally split from the token — multiple components share the same kubeconfig but each gets its own token via a paired `shoot-access-*` Secret.

---

### [4] Shoot Access Secrets (Token-Requestor Pattern)

| | |
|---|---|
| **Location** | `shoot-access-<component>` Secrets in the Shoot's control-plane namespace in the Seed. Examples: `shoot-access-kube-controller-manager`, `shoot-access-kube-scheduler`, `shoot-access-cluster-admin`, `shoot-access-gardener-node-agent-<pool>`, `shoot-access-prometheus` |
| **Creator** | `NewShootAccessSecret(...).Reconcile(ctx, c)` called per component. Gardener-resource-manager's **token-requestor controller** creates a `ServiceAccount` in the Shoot's `kube-system` namespace and requests a `TokenRequest` for it, writing the resulting token into the Secret. |
| **Consumer** | Each control-plane component mounts this Secret and reads the token at the path expected by the generic token kubeconfig [3]. |
| **Points to** | Shoot cluster kube-apiserver (via the paired generic token kubeconfig [3]) |
| **Credential** | Short-lived Kubernetes ServiceAccount token (bound to the ServiceAccount in the Shoot cluster's `kube-system`) |
| **Rotation** | Gardener-resource-manager auto-rotates before expiry. On service-account key rotation, `tokenrequest.RenewAccessSecrets` drops the `serviceaccount.resources.gardener.cloud/token-renew-timestamp` annotation to force immediate re-issuance. |

```
Seed cluster (Shoot namespace):
  Secret: shoot-access-kube-controller-manager
    annotations:
      serviceaccount.resources.gardener.cloud/name: "kube-controller-manager"
      serviceaccount.resources.gardener.cloud/namespace: "kube-system"
      serviceaccount.resources.gardener.cloud/token-expiration-duration: "12h"
    data:
      token: <short-lived token, written by resource-manager>
```

---

### [5] Static User Kubeconfig (optional, per Shoot)

| | |
|---|---|
| **Location** | `user-kubeconfig` (with hash suffix) in Shoot control-plane namespace in Seed |
| **Creator** | `reconcileSecretUserKubeconfig` in [`pkg/component/kubernetes/apiserver/secrets.go`](../../pkg/component/kubernetes/apiserver/secrets.go); only when `StaticTokenKubeconfigEnabled: true` |
| **Consumer** | Primarily internal static-pod setup on control-plane nodes. Also historically provided to users (now superseded by [6]). |
| **Points to** | Shoot kube-apiserver external hostname |
| **Credential** | Static bearer token for user `system:cluster-admin` in group `system:masters`; served to kube-apiserver via `--token-auth-file` |
| **Rotation** | `InPlace` strategy — regenerated on each CA rotation |

---

### [6] Admin Kubeconfig (on-demand, via `AdminKubeconfigRequest`)

| | |
|---|---|
| **Location** | Not persisted; returned ephemerally in `AdminKubeconfigRequest.Status.Kubeconfig` |
| **Creator** | `KubeconfigREST.Create` in [`pkg/apiserver/registry/core/shoot/storage/admin_kubeconfig.go`](../../pkg/apiserver/registry/core/shoot/storage/admin_kubeconfig.go) — handles POST to `shoots/<name>/adminkubeconfig` |
| **Consumer** | The user who issued the request |
| **Points to** | Shoot kube-apiserver; includes all advertised addresses (external, internal, wildcard-TLS-seed-bound) |
| **Credential** | Short-lived X.509 client certificate signed by the Shoot's **client CA** (`<shoot>.ca-client` InternalSecret in Garden cluster, separate from the cluster CA). Username: `gardener.cloud:admin:<user-name>`. Groups: `system:cluster-admin` (for system admins who can list Secrets globally) or `gardener.cloud:system:project-admins` (project-scoped) — determined via a `SubjectAccessReview` against the virtual garden. |
| **Rotation** | Not applicable; cert is ephemeral. Maximum validity enforced by `ShootAdminKubeconfigMaxExpiration` in `GardenerAPIServerConfig`. |

This is the **recommended way** for users to access their Shoot clusters. The use of the client CA (not the cluster CA) means these certs can be revoked independently by rotating the client CA without affecting node/component trust.

---

### [7] Virtual Garden Generic Token Kubeconfig

| | |
|---|---|
| **Location** | `generic-token-kubeconfig-<hash>` in `garden` namespace of runtime cluster; name tracked as annotation on the `Garden` object |
| **Creator** | `gardener-operator` reconciler via `tokenrequest.GenerateGenericTokenKubeconfig` (same function used for Shoots) |
| **Consumer** | Virtual garden control-plane components: virtual `kube-controller-manager`, virtual `gardener-resource-manager`, `gardener-apiserver`, etc. |
| **Points to** | `virtual-garden-kube-apiserver.<garden-ns>.svc.cluster.local` (in-cluster) or external DNS when Istio TLS termination is used |
| **Credential** | Virtual garden cluster CA bundle + token file path (token populated by virtual-garden resource-manager) |
| **Rotation** | `KeepOld` strategy on CA rotation |

---

### [8] Operator-to-Virtual-Garden Access Secret

| | |
|---|---|
| **Location** | `shoot-access-gardener-operator` (or similar) in `garden` namespace of runtime cluster |
| **Creator** | `gardeneraccess.New(...)` component deployed as part of the virtual garden bootstrap; follows the same shoot-access pattern as [4] |
| **Consumer** | `gardener-operator` itself — used to get a client (`GardenClientMap.GetClient(...)`) to the virtual garden after it is bootstrapped |
| **Points to** | Virtual garden kube-apiserver (in-cluster service address and external DNS) |
| **Credential** | Short-lived ServiceAccount token, managed by virtual-garden gardener-resource-manager |
| **Rotation** | Auto-rotated by virtual-garden gardener-resource-manager |

---

### Kubeconfig Relationship Diagram

```
Virtual Garden kube-apiserver
      ▲              ▲                ▲
      │              │                │
  [1] bootstrap  [2] garden       [7] virtual-garden
  kubeconfig     kubeconfig       generic-token-kubeconfig
  (one-time)     (gardenlet,      (virtual kube-ctrl-mgr,
                 X.509 cert)      gardener-resource-mgr)
                                  [8] operator access
                                  (gardener-operator)

Shoot kube-apiserver
      ▲              ▲                ▲
      │              │                │
  [3] generic    [4] shoot-access  [6] admin kubeconfig
  token          secrets           (on-demand, short-lived,
  kubeconfig     (SA tokens,       client CA signed)
  (CA + path)    per component)
                 [5] static user
                 kubeconfig
                 (static token,
                  optional)
```

---

## Key Design Decisions

**Aggregated API server over CRDs** for core types gives Gardener full control over validation, conversion, storage strategies, and sub-resources (like `shoots/status`, `shoots/binding`) without CRD limitations.

**The Botanist pattern** — each Shoot sub-component is independently reconcilable and testable. The flow graph in the shoot reconciler handles ordering and parallelism across components.

**Extension CRDs in the Seed** (not the Garden) — extension providers need access only to their own Seed's objects, not all Garden objects. The `Cluster` CRD provides the denormalized snapshot they need without cross-cluster credentials.

**ManagedResource + resource-manager** — decouples manifest generation (gardenlet/operator Helm rendering) from application (resource-manager). The resource-manager can run with different RBAC from the gardenlet, and health tracking is built in.

**Gardenlet as a distributed agent** — analogous to kubelet. Each Seed is autonomous; gardenlet failures affect only that Seed's Shoots. The Garden cluster is the source of truth but is not on the hot path for running workloads.

**SNI-based API server exposure** via Istio allows multiple Shoots to share a single cloud Load Balancer IP, routing by TLS server name. This significantly reduces cloud LB costs at scale.
