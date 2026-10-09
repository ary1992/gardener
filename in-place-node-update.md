# In-Place Node Update in Gardener — Architecture & Deep Dive

A complete reference for how Gardener performs **in-place updates** of worker nodes — updating
the operating system, kubelet, and credentials on an existing node **without replacing the VM** —
spanning Gardener core, the Machine Controller Manager, the OS extension, the node agent, and the
GardenLinux OS itself.

---

## Table of Contents

1. [Problem & Motivation](#1-problem--motivation)
2. [Foundational Concepts (UEFI, ESP, UKI, EROFS)](#2-foundational-concepts)
   - [OperatingSystemConfig (OSC)](#operatingsystemconfig-osc--the-node-configuration-contract)
   - [Gardener Node Agent (GNA)](#gardener-node-agent-gna--the-on-node-reconciler)
3. [Update Strategies](#3-update-strategies)
4. [Components & Responsibilities](#4-components--responsibilities)
5. [Key API Resources & Types](#5-key-api-resources--types)
6. [High-Level Architecture](#6-high-level-architecture)
7. [End-to-End Flow](#7-end-to-end-flow)
8. [The Hash Mechanism — How Change Is Detected](#8-the-hash-mechanism)
9. [GardenLinux OS Update Mechanism](#9-gardenlinux-os-update-mechanism)
10. [Boot Counting & Automatic Rollback](#10-boot-counting--automatic-rollback)
11. [Failure Handling (Multi-Layer)](#11-failure-handling-multi-layer)
12. [Credential Rotation Interplay](#12-credential-rotation-interplay)
13. [Validation & Constraints](#13-validation--constraints)
14. [Summary](#14-summary)

---

## 1. Problem & Motivation

The default way to update a worker node's OS image or Kubernetes version in Gardener is a
**rolling update**: provision a brand-new VM with the new image, drain the old node, then delete it.

That model is clean but has real costs:

- **Capacity overhead** — you need spare quota/capacity to spin up replacement VMs.
- **Slow** — provisioning, bootstrapping, and joining a fresh node takes minutes per node.
- **Not always possible** — bare-metal nodes, nodes with special/local hardware, or
  environments without elastic VM provisioning cannot simply "create a new machine."
- **State loss** — local data on the node is lost unless carefully migrated.

**In-place update** solves this by mutating the *existing* node: it updates the OS and kubelet on
the running machine and reboots it, keeping the same VM identity.

The trade-off is **complexity and risk** — you are mutating a live machine, so the design must
provide draining, health checks, rollback detection, and automatic recovery. The feature is
currently **Alpha**, gated behind the `InPlaceNodeUpdates` feature gate (default `false`).

---

## 2. Foundational Concepts

In-place update on GardenLinux is built directly on modern UEFI boot primitives rather than on
package management. Four terms underpin everything.

### UEFI / EFI — the firmware

**UEFI** (Unified Extensible Firmware Interface) is the firmware that runs at power-on, replacing
legacy BIOS. Unlike BIOS, it can read a real filesystem (FAT32) and execute `.efi` binaries, and it
stores boot configuration in NVRAM as **EFI variables**. GardenLinux's updater reads the
`LoaderEntrySelected` EFI variable to confirm which entry the machine actually booted from.

### ESP — the EFI System Partition

A small **FAT32 partition** (conventionally mounted at `/efi`) that the firmware can read. It holds
all bootable artifacts. On GardenLinux, in-place "installation" of a new OS is literally **copying a
new `.efi` file into the ESP** at `/efi/EFI/Linux/`. Because the ESP is small, old versions must be
garbage-collected to make room.

### UKI — the Unified Kernel Image

A **UKI** bundles the kernel, initrd, and kernel command line into **one signed `.efi` executable**
the firmware can boot directly — no separate bootloader config required.

### EROFS — the embedded root filesystem

GardenLinux's `_usi` feature goes further: it embeds the **entire root filesystem** as a
compressed, **read-only EROFS image inside the UKI**. So a single `.efi` file *is* the complete,
immutable operating system:

```
 gardenlinux-1443.0.efi   (one UKI = one OS version)
 ┌────────────────────────────────────────┐
 │ kernel (vmlinuz)                         │
 │ initrd                                   │
 │ kernel command line                      │
 │ EROFS root filesystem (read-only, whole  │
 │   OS userland)                           │
 │ ──────────────────────────────────────  │
 │ signature (covers the entire image)      │
 └────────────────────────────────────────┘
```

**Why this matters:** updates are *atomic* (you either have the full new `.efi` or you don't),
the root cannot drift (read-only, immutable), rollback is trivial (boot the old `.efi` still on the
ESP), and one signature verifies kernel + initrd + userland together.

```
 UEFI firmware ──reads──▶ ESP (FAT32 @ /efi) ──contains──▶ UKI .efi files
   "how to boot"            "where" (only place              "what" — each file is a
                             firmware can read)                complete, signed, immutable OS
```

The terms above are OS/boot primitives. The next two concepts are **Gardener control-plane
building blocks** — they appear throughout this document and are central to how an update is
delivered to and executed on a node.

### OperatingSystemConfig (OSC) — the node-configuration contract

The **`OperatingSystemConfig`** (`extensions.gardener.cloud/v1alpha1.OperatingSystemConfig`, often
abbreviated **OSC**) is the declarative description of **how a worker node's operating system should
be configured**. It is the single source of truth for everything that lives on a node below the
Kubernetes layer:

- **`spec.units`** — systemd units to install/enable (e.g. `kubelet.service`, `containerd.service`).
- **`spec.files`** — files to write to the node's filesystem (configs, certificates, scripts),
  inlined, referenced from a Secret, or extracted from a container image.
- **`spec.criConfig`** — container runtime (containerd) configuration, registries, cgroup driver.
- **`spec.inPlaceUpdates`** — the desired OS/kubelet versions and credential-rotation state
  (the in-place "intent").
- **`status`** — enriched by the OS extension: `extensionUnits`, `extensionFiles`, and
  `inPlaceUpdates.osUpdate` (the command/args that actually perform the OS update).

An OSC has one of two **purposes**:

| Purpose | Meaning | Consumed by |
|---|---|---|
| `provision` | Bootstraps a brand-new VM; rendered into the cloud-init / user-data that first boots the machine | Cloud provider / VM boot |
| `reconcile` | Applied continuously on an already-running VM to keep it in the desired state | Gardener Node Agent |

**Lifecycle / ownership:**

```
 Gardenlet ──generates──▶ OSC (per worker pool, from Shoot spec)
                              │
 OS extension ──reconciles──▶ enriches status (OS-specific units/files, osUpdate command)
                              │
 Gardener ──renders reconcile OSC──▶ serialized into a Secret, synced into the shoot cluster
                              │
 Gardener Node Agent ──watches the Secret──▶ applies it to the node
```

Think of the OSC as **"desired state of the node's OS-level configuration."** In the in-place flow
it is the contract that decouples *intent* (Gardener writes `spec.inPlaceUpdates`) from *mechanism*
(the OS extension writes `status.inPlaceUpdates.osUpdate`) from *execution* (the node agent reads
both and acts).

### Gardener Node Agent (GNA) — the on-node reconciler

The **Gardener Node Agent** (**GNA**) is a small agent that runs as a systemd unit
(`gardener-node-agent`) on **every worker node**. It is the on-node counterpart to the OSC: its job
is to **continuously reconcile the node to match the `reconcile` OSC**. (It replaced the older
`cloud-config-downloader` script-based approach.)

**What it does:**

- **Watches its OSC Secret** in the shoot cluster. RBAC is deliberately tight — via the
  *node-agent-authorizer*, each agent may watch only *its own* node's Secret, nothing else. The node
  never talks to the Gardener/seed API directly.
- **Computes a minimal diff** between the last-applied OSC (persisted on disk as
  `last-applied-osc.yaml`) and the incoming one, so it changes only what actually changed —
  tracked in `last-computed-osc-changes.yaml`.
- **Applies the desired state**: writes/updates files, installs and enables/disables systemd units,
  configures containerd and registries, reloads the systemd daemon, and restarts affected units.
- **Executes in-place updates**: performs credential rotation, kubelet updates (with health checks),
  and runs the OS update command from `osc.status.inPlaceUpdates.osUpdate` — see the main flow.
- **Self-manages**: if its own unit (`gardener-node-agent`) changes, it restarts itself gracefully
  (cancels its context) and resumes from persisted state on the next start — important across the
  reboot that an OS update triggers.
- **Reports status by patching the Node object**: e.g. the applied-OSC checksum annotation
  (`node-agent.gardener.cloud/checksum-applied-operating-system-config`), the worker Kubernetes
  version label, and the in-place `update-result` label/annotation.

**Why it exists:** it provides a secure, pull-based, continuously-reconciling mechanism to manage a
node's low-level configuration from a single declarative document — and in the in-place update flow
it is the only component that actually *mutates the running machine*.

```
          Shoot cluster                           Worker node
 ┌───────────────────────────┐        ┌───────────────────────────────┐
 │  OSC Secret (reconcile)    │◀───────│ Gardener Node Agent            │
 │  (synced from the seed)    │  watch │  - diff vs. last-applied        │
 └───────────────────────────┘        │  - write files / units          │
          ▲                            │  - configure containerd         │
          │ patch Node                 │  - run in-place update          │
          │ (checksum, labels)         │  - restart self if needed       │
 ┌───────────────────────────┐        └───────────────┬───────────────┘
 │  Node object               │◀───────────────────────┘ reconcile to desired state
 └───────────────────────────┘
```

---

## 3. Update Strategies

Set per worker pool via `Shoot.spec.provider.workers[].updateStrategy`:

| Strategy | VM replaced? | Node selection | Use case |
|---|---|---|---|
| `AutoRollingUpdate` (default) | **Yes** | N/A — new VMs created | Standard elastic cloud workers |
| `AutoInPlaceUpdate` | No | MCM auto-selects up to `maxUnavailable` | In-place, fully automated |
| `ManualInPlaceUpdate` | No | Operator labels each node when ready | In-place, human-gated rollout |

- `AutoInPlaceUpdate` defaults: `maxSurge = 0`, `maxUnavailable = 1`.
- `ManualInPlaceUpdate`: MCM drains and prepares a node but then **waits** for an operator to label
  it `node.machine.sapcloud.io/selected-for-update=true` before proceeding.

---

## 4. Components & Responsibilities

| Component | Runs in | Responsibility |
|---|---|---|
| **Gardenlet** | Seed | Orchestrates the shoot reconcile flow; writes desired versions into the `OperatingSystemConfig`; tracks pending worker updates in Shoot status |
| **Worker extension controller** | Seed | Translates `updateStrategy` into an MCM `MachineDeployment` with `InPlaceUpdate` strategy; computes per-pool hashes |
| **OS extension** (`gardener-extension-os-gardenlinux`) | Seed | Processes the OSC; declares *how* to update by setting `status.inPlaceUpdates.osUpdate.command` and injecting the update script + post-boot hook |
| **Machine Controller Manager (MCM)** | Seed | Cordons & drains the node; signals readiness via a Node condition; migrates the Machine between MachineSets; detects success/failure |
| **Gardener Node Agent (GNA)** | On each node | Watches the OSC secret; computes what changed; waits for MCM's readiness gate; executes credential rotation, kubelet update, and the OS update; reports result back as node labels |
| **`gardenlinux-update`** | On each node | Pulls, verifies, and installs the new OS image (UKI) into the ESP |
| **GardenLinux OS / systemd-boot** | On each node | Boots the new UKI with boot counting; auto-rolls-back if it fails to boot |

**Seed/node boundary:** the first four run centrally in the seed; the last three run on the worker
node itself. The node cannot talk to the Gardener API directly — the OSC is synced to the node as a
Secret, and GNA has permission to watch only that one Secret.

---

## 5. Key API Resources & Types

### Feature gate
`pkg/features/features.go` — `InPlaceNodeUpdates` (Alpha, default `false`).

### Shoot (`core.gardener.cloud/v1beta1`)
- `Worker.UpdateStrategy *MachineUpdateStrategy` — per-pool strategy.
- `MachineControllerManagerSettings.MachineInPlaceUpdateTimeout` — timeout after which an in-place
  update is declared failed.
- `MachineControllerManagerSettings.DisableHealthTimeout` — prevents MCM replacing the node mid-update.
- `Shoot.Status.InPlaceUpdates.PendingWorkerUpdates.{AutoInPlaceUpdate, ManualInPlaceUpdate} []string`
  — pools awaiting update.
- Condition `ManualInPlaceWorkersUpdated`.

### CloudProfile (`core.gardener.cloud/v1beta1`)
- `MachineImageVersion.InPlaceUpdates.Supported bool` — does this OS image version support in-place?
- `MachineImageVersion.InPlaceUpdates.MinVersionForUpdate *string` — minimum *source* version you
  can in-place-update *from*.

### OperatingSystemConfig (`extensions.gardener.cloud/v1alpha1`)
- `Spec.InPlaceUpdates` — the **desired** state written by Gardener:
  - `OperatingSystemVersion string`
  - `KubeletVersion string`
  - `CredentialsRotation *CredentialsRotation` (CA + service-account-key rotation timestamps)
- `Status.InPlaceUpdates.OSUpdate` — **how to update**, written by the OS extension:
  - `Command string` (path to the update script on the node)
  - `Args []string`

### Worker (`extensions.gardener.cloud/v1alpha1`)
- `WorkerPool.UpdateStrategy` — per-pool strategy.
- `WorkerPool.NodeAgentSecretName` — identifies the node-agent secret (deliberately excluded from
  the in-place hash so a machine-class change alone does not trigger an in-place update).
- `Status.InPlaceUpdates.WorkerPoolToHashMap map[string]string` — last-applied hash per pool.

### MachineDeployment (`machine.sapcloud.io/v1alpha1`, MCM)
- `Spec.Strategy.Type = InPlaceUpdate`
- `Spec.Strategy.InPlaceUpdate.OrchestrationType = Auto | Manual`
- Machine phases: `InPlaceUpdating`, `InPlaceUpdateSuccessful`, `InPlaceUpdateFailed`.
- Node condition `InPlaceUpdate` with reasons:
  `CandidateForUpdate → SelectedForUpdate → DrainSuccessful → ReadyForUpdate`.
- Node labels/annotations:
  - `node.machine.sapcloud.io/selected-for-update` (operator sets, Manual mode)
  - `node.machine.sapcloud.io/update-result = successful | failed`
  - `node.machine.sapcloud.io/update-failed-reason`

---

## 6. High-Level Architecture

### 6.1 Component map (seed vs. node)

```
                              SEED CLUSTER
 ┌───────────────────────────────────────────────────────────────────────┐
 │                                                                         │
 │   ┌───────────┐   writes spec     ┌──────────────────────────────┐     │
 │   │ Gardenlet │ ────────────────▶ │ OperatingSystemConfig (OSC)  │     │
 │   │           │                   │  spec.inPlaceUpdates          │     │
 │   │ (botanist │ ◀──reads status── │  status.inPlaceUpdates.osUpdate│    │
 │   │  flow)    │                   └──────────────────────────────┘     │
 │   └─────┬─────┘                          ▲         │                    │
 │         │                        writes  │         │ reconciles         │
 │         │ deploys                 status  │         ▼                    │
 │         ▼                                ┌──────────────────────────┐   │
 │   ┌──────────────────┐                   │ OS extension             │   │
 │   │ Worker extension │                   │ (os-gardenlinux)         │   │
 │   │ controller       │                   │  injects update script + │   │
 │   │                  │                   │  post-boot hook; sets     │   │
 │   │  creates MD with │                   │  osUpdate.command/args    │   │
 │   │  InPlaceUpdate   │                   └──────────────────────────┘   │
 │   │  strategy        │                                                  │
 │   └────────┬─────────┘                                                  │
 │            │ creates/updates                                            │
 │            ▼                                                            │
 │   ┌──────────────────┐       ┌──────────────────────────────┐          │
 │   │ MachineDeployment│──────▶│ Machine Controller Manager   │          │
 │   │ (machine.sapcloud│       │ drains node, sets Node        │          │
 │   │  .io)            │       │ condition ReadyForUpdate      │          │
 │   └──────────────────┘       └──────────────┬───────────────┘          │
 │                                              │ acts on Node             │
 └──────────────────────────────────────────────┼──────────────────────────┘
                        OSC synced as Secret     │  (shared Node object in
                        into shoot cluster        │   the shoot cluster)
                                 │                │
 ════════════════════════════════╪════════════════╪══════════════════════════
                                 ▼                ▼          WORKER NODE
 ┌───────────────────────────────────────────────────────────────────────┐
 │   ┌──────────────────────────┐   executes   ┌───────────────────────┐  │
 │   │ Gardener Node Agent (GNA)│ ───────────▶ │ gardenlinux-update    │  │
 │   │  watches OSC secret       │              │  pull+verify+install  │  │
 │   │  computes changes         │              │  UKI into ESP         │  │
 │   │  waits ReadyForUpdate     │ ◀──exit code─└───────────────────────┘  │
 │   │  patches Node result      │                        │                │
 │   └──────────────────────────┘                        ▼                │
 │                                              ┌───────────────────────┐  │
 │                                              │ systemd-boot          │  │
 │                                              │  boot counting +       │  │
 │                                              │  automatic rollback    │  │
 │                                              └───────────────────────┘  │
 └───────────────────────────────────────────────────────────────────────┘
```

### 6.2 The OSC as the contract

The `OperatingSystemConfig` is the single source of truth that decouples **intent** from
**execution**:

```
 Gardener  ──writes──▶  OSC.spec.inPlaceUpdates          (WHAT: desired OS/kubelet versions)
 OS ext.   ──writes──▶  OSC.status.inPlaceUpdates.osUpdate (HOW: command + args to run)
 GNA       ──reads───▶  both, then executes on the node    (DO IT)
```

---

## 7. End-to-End Flow

### Phase 1 — Declare intent (Gardenlet)
An operator bumps the machine image or Kubernetes version on an in-place worker pool. Gardenlet's
shoot reconcile flow:
- Writes the desired state into the OSC: `spec.inPlaceUpdates.operatingSystemVersion`,
  `kubelet`, and credential-rotation timestamps.
- Records the pool as pending in `Shoot.Status.InPlaceUpdates.PendingWorkerUpdates`.

### Phase 2 — Declare *how* (OS extension)
`gardener-extension-os-gardenlinux` reconciles the OSC (`purpose: reconcile`). When
`spec.inPlaceUpdates != nil`, it:
- Injects `inplace-update.sh` as a node file.
- Injects an `etc-setup-hook.sh` into the node's update-hook directory (re-applies the `/etc`
  overlay and the GNA unit so the node agent survives the reboot into a fresh root).
- Sets `status.inPlaceUpdates.osUpdate`:
  ```yaml
  osUpdate:
    command: /opt/gardenlinux/inplace-update.sh
    args: ["1443.0"]
  ```

### Phase 3 — Create MachineDeployment (Worker extension)
The Worker extension controller creates/updates an MCM `MachineDeployment` with:
```yaml
spec:
  strategy:
    type: InPlaceUpdate
    inPlaceUpdate:
      orchestrationType: Auto      # or Manual
      maxUnavailable: 1
      maxSurge: 0
```
No new VMs are created — a new `MachineSet` is created and existing `Machine` objects migrate to it
in place. The controller also computes a per-pool hash and stores it in
`Worker.Status.InPlaceUpdates.WorkerPoolToHashMap`.

### Phase 4 — Drain & gate (MCM)
MCM advances the Node condition state machine:
```
CandidateForUpdate → SelectedForUpdate → DrainSuccessful → ReadyForUpdate
```
- Cordons the node (`unschedulable = true`) and drains pods, respecting PodDisruptionBudgets.
- Sets the `InPlaceUpdate` Node condition reason to `ReadyForUpdate` — the handshake signal.
- For **Manual** mode, waits for the operator's `selected-for-update` label before draining.
- Transitions the Machine phase to `InPlaceUpdating`.

### Phase 5 — Execute on the node (GNA)
The node agent reconciles when the OSC secret checksum changes:
1. Compares `node.annotations[checksum-applied-osc]` to the new OSC checksum; if equal, no-op.
2. Computes a structured diff (`operatingSystemConfigChanges.InPlaceUpdates`): OS version,
   kubelet minor version, kubelet config, CPU-manager policy, CA rotation, SA-key rotation.
3. **Gate:** if an in-place update is required but the Node does **not** yet have the
   `InPlaceUpdate = ReadyForUpdate` condition, GNA returns early and waits for MCM.
4. Applies changed files and units to disk.
5. Performs **credential rotation first** (see §12) — before the OS update.
6. If kubelet changed: writes new unit/config, restarts kubelet, polls `/healthz` until healthy.
7. Runs the OS update: annotates the Node with the version being attempted, then executes
   `osUpdate.command` with retry, classifying failures by the script's exit code.

### Phase 6 — OS update + reboot (gardenlinux-update + systemd-boot)
`inplace-update.sh` calls `gardenlinux-update <version>`; on exit 0 it calls `reboot`.
systemd-boot runs the new UKI under boot counting (see §10).

### Phase 7 — Post-reboot verification (GNA)
After reboot GNA restarts, reloads its persisted change-state, and reads `/etc/os-release`:
- **Version matches desired** → delete leftover pods (DaemonSets/local-storage pods that survived
  drain) and label the Node `update-result=successful`.
- **Version still old** (silent rollback) → detected via the still-present attempt annotation →
  label the Node `update-result=failed` with a reason; stop retrying until a new OSC arrives.
- Patch the Node with the new OSC checksum and Kubernetes version label.

### Phase 8 — Completion (MCM → Gardenlet)
- MCM observes `update-result=successful`, migrates the Machine to the new MachineSet
  (phase `InPlaceUpdateSuccessful`), removes the condition, and uncordons the node.
- The Shoot status controller sees the pool's hash now matches the desired hash and clears it from
  `PendingWorkerUpdates`. When all pools are cleared, `Shoot.Status.InPlaceUpdates` is removed and
  the `ManualInPlaceWorkersUpdated` condition returns to `True`.

### 7.1 Sequence diagram

```
Operator   Gardenlet   OS-ext   Worker-ext   MCM        GNA (node)   gardenlinux-update   systemd-boot
   │           │          │          │         │            │               │                 │
   │ bump ver  │          │          │         │            │               │                 │
   ├──────────▶│          │          │         │            │               │                 │
   │           │ write OSC.spec       │         │            │               │                 │
   │           ├─────────▶│          │         │            │               │                 │
   │           │          │ set OSC.status.osUpdate          │               │                 │
   │           │◀─────────┤          │         │            │               │                 │
   │           │ deploy Worker        │         │            │               │                 │
   │           ├────────────────────▶│ create MD (InPlace)   │               │                 │
   │           │          │          ├────────▶│            │               │                 │
   │           │          │          │         │ cordon+drain│               │                 │
   │           │          │          │         │ set ReadyForUpdate          │                 │
   │           │          │          │         ├───────────▶│               │                 │
   │           │          │          │         │            │ apply files    │                 │
   │           │          │          │         │            │ rotate creds   │                 │
   │           │          │          │         │            │ update kubelet │                 │
   │           │          │          │         │            │ run OS update  │                 │
   │           │          │          │         │            ├──────────────▶│ pull+verify+    │
   │           │          │          │         │            │               │ install UKI(+3) │
   │           │          │          │         │            │◀──exit 0──────┤                 │
   │           │          │          │         │            │ reboot ───────────────────────▶ │
   │           │          │          │         │            │               │   boot count    │
   │           │          │          │         │            │◀────── bless OR rollback ─────── │
   │           │          │          │         │            │ read /etc/os-release            │
   │           │          │          │         │            │ label update-result=ok/fail     │
   │           │          │          │         │◀───────────┤               │                 │
   │           │          │          │         │ uncordon    │               │                 │
   │           │◀─ hash matches, clear pending ─┤            │               │                 │
```

---

## 8. The Hash Mechanism

Gardener decides *whether* a pool needs an in-place update by comparing a **desired hash** against
the **last-applied hash** stored in `Worker.Status.InPlaceUpdates.WorkerPoolToHashMap`.

`CalculateWorkerPoolHashForInPlaceUpdate(poolName, kubernetesVersion, kubeletConfig,
machineImageVersion, credentials)` hashes exactly the inputs that an in-place update can change:

- Kubernetes/kubelet version
- Kubelet configuration
- Machine image (OS) version
- Credential-rotation state (CA + service-account key)

Deliberately **excluded**: machine type, image name, CRI, volume — because those cannot be changed
in place (they require VM replacement and are blocked by validation). The node-agent secret name is
also excluded, so a machine-class-only change does not trigger an unnecessary in-place update.

> Any change to this hash function triggers an in-place update of **every** existing node — the
> source code carries an explicit warning about this.

Flow:
```
desired hash  ≠  stored hash   →  pool is pending  →  OSC.spec.inPlaceUpdates updated
                                                       → nodes updated → Worker status hash updated
desired hash  =  stored hash   →  pool is up to date → cleared from PendingWorkerUpdates
```

---

## 9. GardenLinux OS Update Mechanism

The on-node tool `gardenlinux-update <version>` (Go binary, package
`gardenlinux/package-gardenlinux-update`) performs the OS swap. Its steps:

1. **Identify the running system** — parse `/etc/os-release` for `GARDENLINUX_CNAME`
   (flavor+arch) and current version. The `<version>` argument is used as an **OCI tag**.
2. **EFI safety check** — read the `LoaderEntrySelected` EFI variable; confirm the machine booted
   via systemd-boot and the booted entry matches `cname-currentVersion`. Otherwise abort
   (system failure). Can be skipped with `--skip-efi-check` (used in local/testing).
3. **Pull from an OCI registry** — default `ghcr.io/gardenlinux/gardenlinux`, overridable via
   `--repo` (Gardener sets this from `/etc/gardenlinux/usirepo.conf`). Resolve the image index,
   find the manifest whose `cname` annotation matches this node, and locate the UKI layer
   (media type `application/io.gardenlinux.uki`).
4. **Verify the signature** — fetch the `.sig` manifest, extract an RSA signature, fetch the signed
   message, confirm it references the correct manifest digest, **re-hash the message locally**
   (never trust the server-provided hash), and verify RSA-PKCS1v15 against the local public key
   `/etc/gardenlinux/oci_signing_key.pem`. Any failure aborts the update.
5. **Garbage-collect the ESP** — if free space is insufficient, delete old UKI `.efi` files
   (sorted so blessed/oldest go first), never removing the currently-running version. If it still
   can't free enough, abort (system failure).
6. **Install the new UKI** — download the layer and write it to the ESP as
   `/efi/EFI/Linux/<cname>-<version>+3.efi`. The `+3` suffix arms systemd-boot's boot counter with
   3 tries.
7. **Run post-update hooks** — execute everything in `/etc/gardenlinux/update-hooks` (this is where
   Gardener's `etc-setup-hook.sh` runs to preserve `/etc` and the GNA unit across the reboot).
8. **Exit** — the tool does *not* reboot; the Gardener wrapper reboots on exit 0.

```
gardenlinux-update 1443.0
   │
   ├─ parse /etc/os-release (cname, current version)
   ├─ EFI safety check (LoaderEntrySelected matches running system?)
   ├─ OCI: resolve index → manifest for this cname → UKI layer
   ├─ VERIFY SIGNATURE (local re-hash + RSA against local pubkey)   ◀── tamper/unsigned → ABORT
   ├─ ensure ESP free space (GC old UKIs, keep current)
   ├─ download UKI → /efi/EFI/Linux/<cname>-1443.0+3.efi            ◀── atomic file drop
   ├─ run /etc/gardenlinux/update-hooks/*  (Gardener etc-setup-hook)
   └─ exit 0   →   wrapper calls `reboot`
```

---

## 10. Boot Counting & Automatic Rollback

GardenLinux relies on **systemd-boot's Automatic Boot Assessment** instead of custom rollback code.
The `+N` suffix on the UKI filename is a try counter.

### State before reboot
```
/efi/EFI/Linux/
  ├── gardenlinux-1312.0.efi      ← current, BLESSED (no +N = permanent)
  └── gardenlinux-1443.0+3.efi    ← new, UNBLESSED, 3 tries remaining
```

### Happy path — new image boots
```
 +3.efi ──boot(--counter)──▶ +2.efi ──system healthy──▶ gardenlinux-1443.0.efi
 (unblessed)                 (booting)   systemd-bless-   (BLESSED, permanent default)
                                         boot strips +N
```

### Rollback path — new image fails to boot
```
 +3 ──fail──▶ +2 ──fail──▶ +1 ──fail──▶ +0 ──exhausted──▶ systemd-boot skips +0,
                                                           boots next BLESSED entry:
                                                           gardenlinux-1312.0.efi
                                                           (node self-heals, no human)
```

### Combined decision
```
             boot attempt starts (counter --)
                       │
             reached "boot-complete"? ──YES──▶ BLESS entry (strip +N) → update committed
                       │NO
             tries remaining > 0 ? ──YES──▶ reboot & retry (same image)
                       │NO
             mark entry BAD → fall back to old BLESSED entry (automatic rollback)
```

This is only safe because the OS is an immutable single-file image: the old version is a complete,
bootable artifact that was never mutated.

---

## 11. Failure Handling (Multi-Layer)

Failures are caught at three independent layers.

### Layer 1 — `gardenlinux-update` exit codes (retry contract)

| Code | Meaning | Retry? | Examples |
|---|---|---|---|
| `0` | success | — | — |
| `1` | invalid arguments | **No** (permanent) | missing version, cname parse error |
| `2` | system failure | **No** (permanent) | EFI check failed, bad signature, disk full, write error |
| `3` | network problems | **Yes** | registry unreachable, fetch failed |

GNA's reconciler classifies the script output with regexes:
`"network problems"` → retriable (retry ~every 30s up to 5 min, then requeue ~10 min later);
`"invalid arguments" | "system failure"` → non-retriable (terminal error).

### Layer 2 — systemd-boot boot counting

If the tool succeeds but the new image won't boot, boot counting (§10) auto-reverts to the previous
version after 3 failed attempts — no orchestration involved.

### Layer 3 — GNA post-reboot verification

A rollback is invisible to the cluster, so GNA closes the loop: it annotates the Node with the
*attempted* version before rebooting, and after reboot checks `/etc/os-release`. If the running
version is still the old one despite the attempt annotation, it concludes the OS rolled back and
labels the Node `update-result=failed`, surfacing the failure to MCM and the Shoot status.

Additional guards:
- **Kubelet health**: after a kubelet update, GNA polls `/healthz` for up to 5 minutes; failure
  marks the node update-failed.
- **MCM timeout**: `MachineInPlaceUpdateTimeout` bounds how long an update may take before MCM
  marks the Machine `InPlaceUpdateFailed`.

---

## 12. Credential Rotation Interplay

In-place update also carries CA and service-account-key rotation. GNA performs credential rotation
**before** the OS update, never after. Reason:

> An in-place OS update wipes the node's `/etc` overlay on reboot, taking the agent's persisted
> state files (`last-applied-osc.yaml`, `last-computed-osc-changes.yaml`) with it — this is
> intentional, forcing a clean re-apply onto the fresh root. If a pending rotation were recorded in
> that state, it would be lost across the reboot. Rotating first means the rotation completes and
> its result is captured when the post-reboot reconcile writes the new state.

Rotation steps on the node:
- **Service-account key**: trigger the token-sync controller to refresh projected tokens.
- **CA (node-agent)**: request a new kubeconfig for GNA, then restart GNA.
- **CA (kubelet)**: rebootstrap the kubelet with a new bootstrap kubeconfig, restart kubelet,
  health-check it.

Gardener also **blocks** starting a CA/SA rotation while manual in-place workers are still pending,
to avoid a rotation-mid-update race.

---

## 13. Validation & Constraints

Enforced by the API server admission plugins and validation (`plugin/pkg/shoot/validator`,
`pkg/api/core/validation/shoot.go`):

- **Feature gate**: in-place strategies are rejected unless `InPlaceNodeUpdates` is enabled.
- **CloudProfile support**: the target image version must have `inPlaceUpdates.supported = true` and
  satisfy `minVersionForUpdate` (the running version must be high enough to update from).
- **Immutable strategy**: you cannot switch a pool between rolling and in-place after creation.
- **Immutable VM-identity fields**: machine type, image name, CRI, and volume cannot change for an
  in-place pool (those require VM replacement).
- **No downgrades**: the machine image version cannot be downgraded for an in-place pool.
- **MCM settings scoping**: `inPlaceUpdateTimeout` and `disableHealthTimeout` are only valid with an
  in-place strategy.
- **Rotation guard**: CA/SA rotation cannot start while in-place workers are pending.
- **node-local-DNS restriction** for in-place pools (Kubernetes < 1.34 or IPVS kube-proxy mode).
- `force-in-place-update` is a registered `gardener.cloud/operation` annotation value that forces a
  re-run mid-flight.
- The **maintenance controller** only proposes image versions during auto-maintenance that satisfy
  the in-place support/min-version constraints.

Defaults (`pkg/apis/core/v1beta1/defaults_shoot.go`):
- Self-hosted shoots (unmanaged infrastructure) default to `AutoInPlaceUpdate`.
- `AutoInPlaceUpdate`: `maxSurge = 0`, `maxUnavailable = 1`.
- In-place pools default `disableHealthTimeout = true`.

---

## 14. Summary

In-place node update avoids VM replacement by treating the operating system as an **immutable,
signed, single-file image** (a UKI with an embedded read-only EROFS root) and updating it through
UEFI boot primitives rather than package management.

The design cleanly **decouples intent from execution**, coordinated through the
`OperatingSystemConfig`:

- **Gardener** declares the desired OS/kubelet versions (`spec.inPlaceUpdates`).
- **The OS extension** declares how to apply them (`status.inPlaceUpdates.osUpdate`).
- **MCM** guarantees the node is safe to mutate — it drains and gates via a Node condition.
- **The node agent** performs the surgery (credentials → kubelet → OS update) and reports back via
  Node labels.
- **GardenLinux + systemd-boot** make the OS swap atomic and provide **free automatic rollback**
  through boot counting.

Failures are contained at three independent layers — the updater's retry-classifying exit codes,
systemd-boot's automatic rollback, and the node agent's post-reboot version check — so a bad update
either retries, self-heals, or is cleanly surfaced to the cluster, all without ever replacing the VM.
```

key primitive: the `+N` boot-count suffix on the UKI filename is what turns "copy a file into the
ESP" into a safe, self-rolling-back OS update.
