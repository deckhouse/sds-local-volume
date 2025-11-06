---
title: "The sds-local-volume module: quick start"
description: "The sds-local-volume quick start."
weight: 2
---

{{< alert level="warning" >}}
To work with volume snapshots, the [snapshot-controller](/modules/snapshot-controller/) module must be connected. The ability to work with volume snapshots is available only in commercial editions of Deckhouse and only when using LVM Thin volumes.
{{< /alert >}}

The module supports two operation modes: LVM (Thick) and LVM Thin. Each mode has its own features, advantages, and limitations. For more details on the differences between modes, see the [FAQ](./faq.html#when-to-use-lvm-and-when-to-use-lvm-thin).

## Quick start

Below is an example of module setup for creating Thick storage on three cluster nodes: enabling modules via [ModuleConfig](/modules/deckhouse/configuration.html), creating LVM volume groups on each node via [LVMVolumeGroup](/modules/sds-node-configurator/stable/cr.html#lvmvolumegroup), and creating [LocalStorageClass](./cr.html#localstorageclass) for use when creating PVCs.

### Enabling the module

To ensure the correct operation of the `sds-local-volume` module, follow these steps:

{{< alert level="info" >}}
All commands below must be run on a machine with access to the Kubernetes API and administrator privileges.
{{< /alert >}}

1. Enable the `sds-node-configurator` module by running the command below:

   ```shell
   d8 k apply -f - <<EOF
   apiVersion: deckhouse.io/v1alpha1
   kind: ModuleConfig
   metadata:
     name: sds-node-configurator
   spec:
     enabled: true
     version: 1
     settings:
       enableThinProvisioning: true # If you plan to use LVM Thin volumes
   EOF
   ```

1. Wait for the `sds-node-configurator` module to transition to the `Ready` state. At this stage, checking pods in the `d8-sds-node-configurator` namespace is not required.

   ```shell
   d8 k get modules sds-node-configurator -w
   ```

1. Review the [available settings](./configuration.html) of the `sds-local-volume` module before enabling it. After reviewing, enable the module by running the command below:

   ```shell
   d8 k apply -f - <<EOF
   apiVersion: deckhouse.io/v1alpha1
   kind: ModuleConfig
   metadata:
     name: sds-local-volume
   spec:
     enabled: true
     version: 1
     settings:
       enableThinProvisioning: true # if you plan to use LVM Thin volumes
   EOF
   ```

   As a result, the module will start with default settings, which will create service pods of the `sds-local-volume` component on all cluster nodes:

1. Wait for the `sds-local-volume` module to transition to the `Ready` state.

   ```shell
   d8 k get modules sds-local-volume -w
   ```

1. Ensure that all pods in the `d8-sds-local-volume` and `d8-sds-node-configurator` namespaces are in the `Running` or `Completed` status. Pods must be running on all nodes where LVM resources are planned to be used.

   ```shell
   d8 k -n d8-sds-local-volume get pod -owide -w
   d8 k -n d8-sds-node-configurator get pod -o wide -w
   ```

### Preparing nodes for storage creation

Start `csi-node` pods on selected nodes for correct storage operation. By default, these pods start on all cluster nodes. Check their presence with the command:

```shell
d8 k -n d8-sds-local-volume get pod -owide
```

Scheduling of `csi-node` pods is controlled by special labels (`nodeSelector`) that are set in the [spec.settings.dataNodes.nodeSelector](configuration.html#parameters-datanodes-nodeselector) parameter of the module. For more details on configuring and selecting nodes for module operation, see the [Selecting nodes for module operation](./usage.html#selecting-nodes-for-module-operation) section.

### Configuring storage on nodes

To configure storage on nodes, create LVM volume groups using [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) resources. This example creates Thick storage. For instructions on creating Thin storage, see the [Creating thin storage](./usage.html#creating-thin-storage) section.

{{< alert level="warning" >}}
Before creating an [LVMVolumeGroup](/modules/sds-node-configurator/stable/cr.html#lvmvolumegroup) resource, ensure that the `csi-node` pod is running on the node. To do this, run the command:

```shell
d8 k -n d8-sds-local-volume get pod -owide
```

{{< /alert >}}

To configure storage on nodes, follow these steps:

1. Get all available [BlockDevice](/modules/sds-node-configurator/stable/cr.html#blockdevice) resources in the cluster:

   ```shell
   d8 k get bd
   ```

   Example output:

   ```console
   NAME                                           NODE       CONSUMABLE   SIZE           PATH
   dev-ef4fb06b63d2c05fb6ee83008b55e486aa1161aa   worker-0   false        976762584Ki    /dev/nvme1n1
   dev-0cfc0d07f353598e329d34f3821bed992c1ffbcd   worker-0   false        894006140416   /dev/nvme0n1p6
   dev-7e4df1ddf2a1b05a79f9481cdf56d29891a9f9d0   worker-1   false        976762584Ki    /dev/nvme1n1
   dev-b103062f879a2349a9c5f054e0366594568de68d   worker-1   false        894006140416   /dev/nvme0n1p6
   dev-53d904f18b912187ac82de29af06a34d9ae23199   worker-2   false        976762584Ki    /dev/nvme1n1
   dev-6c5abbd549100834c6b1668c8f89fb97872ee2b1   worker-2   false        894006140416   /dev/nvme0n1p6
   ```

1. Create an [LVMVolumeGroup](/modules/sds-node-configurator/stable/cr.html#lvmvolumegroup) resource for the `worker-0` node:

   ```shell
   d8 k apply -f - <<EOF
   apiVersion: storage.deckhouse.io/v1alpha1
   kind: LVMVolumeGroup
   metadata:
    name: "vg-1-on-worker-0" # The name can be any valid Kubernetes resource name. This LVMVolumeGroup resource name will be used when creating LocalStorageClass
   spec:
     type: Local
     local:
       nodeName: "worker-0"
     blockDeviceSelector:
       matchExpressions:
         - key: kubernetes.io/metadata.name
           operator: In
           values:
             - dev-ef4fb06b63d2c05fb6ee83008b55e486aa1161aa
             - dev-0cfc0d07f353598e329d34f3821bed992c1ffbcd
     actualVGNameOnTheNode: "vg-1" # The name of the LVM volume group that will be created from the specified block devices on the node
   EOF
   ```

1. Wait for the created [LVMVolumeGroup](/modules/sds-node-configurator/stable/cr.html#lvmvolumegroup) resource to transition to the `Ready` state:

   ```shell
   d8 k get lvg vg-1-on-worker-0 -w
   ```

   After the resource transitions to the `Ready` state, an LVM volume group named `vg-1` will be created on the `worker-0` node from the block devices `/dev/nvme1n1` and `/dev/nvme0n1p6`.

1. Create an [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) resource for the `worker-1` node:

   ```shell
   d8 k apply -f - <<EOF
   apiVersion: storage.deckhouse.io/v1alpha1
   kind: LVMVolumeGroup
   metadata:
     name: "vg-1-on-worker-1"
   spec:
     type: Local
     local:
       nodeName: "worker-1"
     blockDeviceSelector:
       matchExpressions:
         - key: kubernetes.io/metadata.name
           operator: In
           values:
             - dev-7e4df1ddf2a1b05a79f9481cdf56d29891a9f9d0
             - dev-b103062f879a2349a9c5f054e0366594568de68d
     actualVGNameOnTheNode: "vg-1"
   EOF
   ```

1. Wait for the created [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) resource to transition to the `Ready` state:

   ```shell
   d8 k get lvg vg-1-on-worker-1 -w
   ```

   After the resource transitions to the `Ready` state, an LVM volume group named `vg-1` will be created on the `worker-1` node from the block devices `/dev/nvme1n1` and `/dev/nvme0n1p6`.

1. Create an [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) resource for the `worker-2` node:

   ```shell
   d8 k apply -f - <<EOF
   apiVersion: storage.deckhouse.io/v1alpha1
   kind: LVMVolumeGroup
   metadata:
     name: "vg-1-on-worker-2"
   spec:
     type: Local
     local:
       nodeName: "worker-2"
     blockDeviceSelector:
       matchExpressions:
         - key: kubernetes.io/metadata.name
           operator: In
           values:
             - dev-53d904f18b912187ac82de29af06a34d9ae23199
             - dev-6c5abbd549100834c6b1668c8f89fb97872ee2b1
     actualVGNameOnTheNode: "vg-1"
   EOF
   ```

1. Wait for the created [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) resource to transition to the `Ready` state:

   ```shell
   d8 k get lvg vg-1-on-worker-2 -w
   ```

   After the resource transitions to the `Ready` state, an LVM volume group named `vg-1` will be created on the `worker-2` node from the block devices `/dev/nvme1n1` and `/dev/nvme0n1p6`.

1. Create a [LocalStorageClass](./cr.html#localstorageclass) resource:

   > **Warning:** Creating a StorageClass for the `local.csi.storage.deckhouse.io` CSI driver by users is prohibited.

   ```shell
   d8 k apply -f -<<EOF
   apiVersion: storage.deckhouse.io/v1alpha1
   kind: LocalStorageClass
   metadata:
     name: local-storage-class
   spec:
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

> **Warning.** Do not use [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) resources that contain at least one thin pool in a [LocalStorageClass](./cr.html#localstorageclass) with `type: Thick`.

1. Wait for the created [LocalStorageClass](./cr.html#localstorageclass) resource to transition to the `Created` state:

   ```shell
   d8 k get lsc local-storage-class -w
   ```

1. Verify the creation of the corresponding StorageClass:

   ```shell
   d8 k get sc local-storage-class
   ```

After the StorageClass named `local-storage-class` appears, the `sds-local-volume` module setup is complete. You can now create PersistentVolumeClaim (PVC) resources, specifying the StorageClass named `local-storage-class`.

