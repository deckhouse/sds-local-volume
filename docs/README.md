---
title: "The sds-local-volume module"
description: "The sds-local-volume module: general concepts and principles."
weight: 1
---

The module is designed to manage local block storage based on LVM. It enables creating StorageClasses in Kubernetes using the [LocalStorageClass](cr.html#localstorageclass) resource.

{{< alert level="info" >}}
Creating StorageClass for the `local.csi.storage.deckhouse.io` CSI driver by users is prohibited.

Supported access mode: ReadWriteOnce (RWO). See [Access modes and volume modes](#access-modes-and-volume-modes).
{{< /alert >}}

{{< alert level="warning" >}}
Volume snapshots require a module that provides the `snapshot.storage.k8s.io` CRDs, for example [snapshot-controller](/modules/snapshot-controller/). The ability to work with volume snapshots is available only in commercial editions of Deckhouse Kubernetes Platform and only when using LVM-thin volumes.
{{< /alert >}}

## How the module works

The `sds-local-volume` module uses local disks of cluster nodes to create block storage based on LVM.

The module supports two operation modes: **LVM (Thick)** and **LVM-thin**. For more details on the differences between modes, see the [FAQ](./faq.html#when-to-use-lvm-and-when-to-use-lvm-thin).

## Access modes and volume modes

Volumes of the module are logical volumes on the disks of a single node, so they can be used from one node only.

- The `ReadWriteOnce` access mode is supported. A volume with this mode can also be mounted read-only on that node.
- The `ReadWriteOncePod`, `ReadWriteMany` and `ReadOnlyMany` access modes are not supported.
- Both `volumeMode: Filesystem` and `volumeMode: Block` are supported.

If a PersistentVolumeClaim requests an unsupported access mode, the driver refuses to create the volume. The PersistentVolumeClaim stays in the `Pending` state, and a `ProvisioningFailed` event with the reason appears in its events. The check applies only to the creation of new volumes; volumes that already exist are not changed.

## When to use the module

The `sds-local-volume` module is suitable for the following scenarios:

- Maximum storage performance comparable to local disk performance is required (LVM (Thick) mode).
- Applications need fast access to data on local node disks without network latency.
- Disk space needs to be used efficiently through on-demand volume allocation (LVM-thin mode).

The module is not suitable for scenarios that require:

- Network storage with the ability to migrate volumes between nodes.
- Data replication between nodes.

## System requirements and recommendations

The module has the following system requirements and recommendations:

- It is recommended to use standard Linux kernels included in [supported distributions](/products/kubernetes-platform/documentation/v1/reference/supported_versions.html#linux).
- It is not recommended to use other SDS (Software Defined Storage) solutions to provide SDS disks for Deckhouse Kubernetes Platform.

## Additional materials

- **[Quick start](./quick_start.html)** — example of module setup for creating Thick storage on three cluster nodes.
- **[Configuration](./configuration.html)** — description of module configuration parameters.
- **[Custom Resources](./cr.html)** — description of custom resources used by the module.
- **[Usage](./usage.html)** — volume cleanup, data migration between PVCs, and volume snapshots.
- **[FAQ](./faq.html)** — answers to frequently asked questions about the module.
