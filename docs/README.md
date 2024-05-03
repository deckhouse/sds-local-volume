---
title: "The sds-local-volume module"
description: "The sds-local-volume module: General Concepts and Principles."
moduleStatus: experimental
---

This module manages local block storage based on `LVM`. The module allows you to create a `StorageClass` in `Kubernetes` by creating [Kubernetes custom resources](./cr.html) `LocalStorageClass` (see example below). 
To create a `Storage Class`, you will need the `LVMVolumeGroup` configured on the cluster nodes. The `LVM` configuration is done by the [sds-node-configurator](../../sds-node-configurator/) module.
> **Caution!** Before enabling the `sds-local-volume` module, you must enable the `sds-node-configurator` module.
>
After you enable the `sds-local-volume` module in the Deckhouse Kubernetes Platform configuration, you have to create StorageClasses.

> **Caution!** The user is not allowed to create a `StorageClass` for the local.csi.storage.deckhouse.io CSI driver.

Two modes are supported: LVM and LVMThin.
Each mode has its advantages and disadvantages. Read [FAQ](./faq.html#what-is-difference-between-lvm-and-lvmthin) to learn more and compare them.

## Quickstart guide

Note that all commands must be run on a machine that has administrator access to the Kubernetes API.

### Enabling modules

- Enable the sds-node-configurator module

  ```yaml
  kubectl apply -f - <<EOF
  apiVersion: deckhouse.io/v1alpha1
  kind: ModuleConfig
  metadata:
    name: sds-node-configurator
  spec:
    enabled: true
    version: 1
  EOF
  ```

- Wait for it to become `Ready`. At this stage, you do NOT need to check the pods in the `d8-sds-node-configurator` namespace.

  ```shell
  kubectl get mc sds-node-configurator -w
  ```

- Enable the `sds-local-volume` module. Refer to the [configuration](./configuration.html) to learn more about module settings. In the example below, the module is launched with the default settings. This will result This will cause the service pads of the `sds-local-volume` components to be launched on all nodes of the cluster.
- This will cause the service pods of the `sds-local-volume` components to be launched on all nodes of the cluster.

  ```yaml
  kubectl apply -f - <<EOF
  apiVersion: deckhouse.io/v1alpha1
  kind: ModuleConfig
  metadata:
    name: sds-local-volume
  spec:
    enabled: true
    version: 1
  EOF
  ```

- Wait for the module to become `Ready`.

  ```shell
  kubectl get mc sds-local-volume -w
  ```

- Make sure that all pods in `d8-sds-local-volume` and `d8-sds-node-configurator` namespaces are `Running` or `Completed` and are running on all nodes where `LVM` resources are intended to be used.
  
  ```shell
  kubectl -n d8-sds-local-volume get pod -owide -w
  kubectl -n d8-sds-node-configurator get pod -o wide -w
  ```

### Configuring storage on nodes

You need to create `LVM` volume groups on the nodes using `LVMVolumeGroup` custom resources. As part of this quickstart guide, we will create a regular `Thick` storage.

To configure the storage:

- List all the [BlockDevice](../../sds-node-configurator/stable/cr.html#blockdevice) resources available in your cluster:

  ```shell
  kubectl get bd
  
  NAME                                           NODE       CONSUMABLE   SIZE           PATH
  dev-ef4fb06b63d2c05fb6ee83008b55e486aa1161aa   worker-0   false        976762584Ki    /dev/nvme1n1
  dev-0cfc0d07f353598e329d34f3821bed992c1ffbcd   worker-0   false        894006140416   /dev/nvme0n1p6
  dev-7e4df1ddf2a1b05a79f9481cdf56d29891a9f9d0   worker-1   false        976762584Ki    /dev/nvme1n1
  dev-b103062f879a2349a9c5f054e0366594568de68d   worker-1   false        894006140416   /dev/nvme0n1p6
  dev-53d904f18b912187ac82de29af06a34d9ae23199   worker-2   false        976762584Ki    /dev/nvme1n1
  dev-6c5abbd549100834c6b1668c8f89fb97872ee2b1   worker-2   false        894006140416   /dev/nvme0n1p6
  ```

- Create an [LVMVolumeGroup](../../sds-node-configurator/stable/cr.html#lvmvolumegroup) resource for `worker-0`:

  ```yaml
  kubectl apply -f - <<EOF
  apiVersion: storage.deckhouse.io/v1alpha1
  kind: LvmVolumeGroup
  metadata:
    name: "vg-1-on-worker-0" # The name can be any fully qualified resource name in Kubernetes. This LvmVolumeGroup resource name will be used to create LocalStorageClass in the future
  spec:
    type: Local
    blockDeviceNames:  # specify the names of the BlockDevice resources that are located on the target node and whose CONSUMABLE is set to true. Note that the node name is not specified anywhere since it is derived from BlockDevice resources.
      - dev-ef4fb06b63d2c05fb6ee83008b55e486aa1161aa
      - dev-0cfc0d07f353598e329d34f3821bed992c1ffbcd
    actualVGNameOnTheNode: "vg-1" # the name of the LVM VG to be created from the above block devices on the node 
  EOF
  ```

- Wait for the created `LVMVolumeGroup` resource to become `Operational`:

  ```shell
  kubectl get lvg vg-1-on-worker-0 -w
  ```

- The resource becoming `Operational` means that an LVM VG named `vg-1` made up of the `/dev/nvme1n1` and `/dev/nvme0n1p6` block devices has been created on the `worker-0` node.

- Next, create an [LVMVolumeGroup](../../sds-node-configurator/stable/cr.html#lvmvolumegroup) resource for `worker-1`:

  ```yaml
  kubectl apply -f - <<EOF
  apiVersion: storage.deckhouse.io/v1alpha1
  kind: LvmVolumeGroup
  metadata:
    name: "vg-1-on-worker-1"
  spec:
    type: Local
    blockDeviceNames:
    - dev-7e4df1ddf2a1b05a79f9481cdf56d29891a9f9d0
    - dev-b103062f879a2349a9c5f054e0366594568de68d
    actualVGNameOnTheNode: "vg-1"
  EOF
  ```

- Wait for the created `LVMVolumeGroup` resource to become `Operational`:

  ```shell
  kubectl get lvg vg-1-on-worker-1 -w
  ```

- The resource becoming `Operational` means that an LVM VG named `vg-1` made up of the `/dev/nvme1n1` and `/dev/nvme0n1p6` block device has been created on the `worker-1` node.

- Create an [LVMVolumeGroup](../../sds-node-configurator/stable/cr.html#lvmvolumegroup) resource for `worker-2`:

  ```yaml
  kubectl apply -f - <<EOF
  apiVersion: storage.deckhouse.io/v1alpha1
  kind: LvmVolumeGroup
  metadata:
    name: "vg-1-on-worker-2"
  spec:
    type: Local
    blockDeviceNames:
    - dev-53d904f18b912187ac82de29af06a34d9ae23199
    - dev-6c5abbd549100834c6b1668c8f89fb97872ee2b1
    actualVGNameOnTheNode: "vg-1"
  EOF
  ```

- Wait for the created `LVMVolumeGroup` resource to become `Operational`:

  ```shell
  kubectl get lvg vg-1-on-worker-2 -w
  ```

- The resource becoming `Operational` means that an LVM VG named `vg-1` made up of the `/dev/nvme1n1` and `/dev/nvme0n1p6` block device has been created on the `worker-2` node.

- Create a [LocalStorageClass](./cr.html#localstorageclass) resource:

  ```yaml
  kubectl apply -f -<<EOF
  apiVersion: storage.deckhouse.io/v1alpha1
  kind: LocalStorageClass
  metadata:
    name: local-storage-class
  spec:
    isDefault: false
    lvm:
      lvmVolumeGroups:
        - name: vg-1-on-worker-0
        - name: vg-1-on-worker-1
        - name: vg-1-on-worker-2
      type: Thick
    reclaimPolicy: Delete
    volumeBindingMode: WaitForFirstConsumer
  EOF
  ```

- Wait for the created `LocalStorageClass` resource to become `Created`:

  ```shell
  kubectl get lsc local-storage-class -w
  ```

- Confirm that the corresponding `StorageClass` has been created:

  ```shell
  kubectl get sc local-storage-class
  ```

- If `StorageClass` with the name `local-storage-class` is shown, then the configuration of the `sds-local-volume` module is complete. Now users can create PVs by specifying `StorageClass` with the name `local-storage-class`.

## System requirements and recommendations

### Requirements
- Stock kernels shipped with the [supported distributions](https://deckhouse.io/documentation/v1/supported_versions.html#linux).
- Do not use another SDS (Software defined storage) to provide disks to our SDS.
