---
title: "The sds-local-volume module: configuration"
force_searchable: true
description: The sds-local-volume module's configuration.
weight: 3
---

## Enabling the module

To enable the `sds-local-volume` module, follow these steps:

{{< alert level="info" >}}
All commands must be run on a machine with access to the Kubernetes API and administrator privileges.
{{< /alert >}}

1. Enable the `sds-local-volume` module:

   ```shell
   d8 k apply -f - <<EOF
   apiVersion: deckhouse.io/v1alpha1
   kind: ModuleConfig
   metadata:
     name: sds-local-volume
   spec:
     enabled: true
     version: 2
   EOF
   ```

1. Wait for the `sds-local-volume` module to transition to the `Ready` state:

   ```shell
   d8 k get modules sds-local-volume -w
   ```

1. Verify that the module pods are running:

   ```shell
   d8 k -n d8-sds-local-volume get pod -owide
   d8 k -n d8-sds-node-configurator get pod -o wide
   ```

For detailed module setup instructions, see the [Quick start](./quick_start.html) section.

## Disabling the module

Disabling the module removes its controller and CSI driver, and nothing else. Logical volumes stay on the nodes with the data on them, and PersistentVolumes, [LVMLogicalVolume](/modules/sds-node-configurator/cr.html#lvmlogicalvolume) and [LocalStorageClass](./cr.html#localstorageclass) resources stay in the cluster. The module removes the finalizers of the LocalStorageClass resources and of their StorageClasses only.

Remove the volumes and the classes while the module is still running:

1. Find the volumes of the module and delete their PersistentVolumeClaims, including those of Deckhouse modules (for example, monitoring) that use the module's StorageClasses:

   ```shell
   d8 k get pv -o jsonpath='{range .items[?(@.spec.csi.driver=="local.csi.storage.deckhouse.io")]}{.metadata.name}{"\t"}{.spec.claimRef.namespace}/{.spec.claimRef.name}{"\t"}{.spec.persistentVolumeReclaimPolicy}{"\n"}{end}'
   ```

   Wait until the command returns nothing. A volume with `reclaimPolicy: Retain` stays after its PersistentVolumeClaim is deleted: free its space as described in [How do I reclaim the space of a volume created with reclaimPolicy: Retain?](./faq.html#how-do-i-reclaim-the-space-of-a-volume-created-with-reclaimpolicy-retain).

   Delete the PersistentVolumeClaims before the LocalStorageClass. The driver takes `spec.lvm.volumeCleanup` from the LocalStorageClass when it deletes a volume; once the class is gone, the volume is removed without erasing its data.

1. If one of the module's StorageClasses is the default one for the cluster (the `global.defaultClusterStorageClass` setting or the `storageclass.kubernetes.io/is-default-class` annotation), switch the default to another class.

1. Delete the LocalStorageClass resources. The controller deletes their StorageClasses; deleting such a StorageClass directly is rejected.

   ```shell
   d8 k get localstorageclasses.storage.deckhouse.io
   d8 k delete localstorageclasses.storage.deckhouse.io <name>
   ```

1. Disable the module:

   ```shell
   d8 k patch moduleconfig sds-local-volume --type=merge -p '{"spec":{"enabled":false}}'
   ```

1. If the LVM Volume Groups are no longer needed, delete them and disable the `sds-node-configurator` module as described in [its documentation](/modules/sds-node-configurator/faq.html#how-do-i-disable-the-module), unless other modules (`sds-replicated-volume`, `sds-elastic`) still use it.

What happens to the volumes left in the cluster after the module is disabled:

- Pods that already run keep their volumes; a Pod that starts or moves to another node cannot mount a volume of the module.
- A PersistentVolumeClaim deleted after disabling leaves its PersistentVolume, its LVMLogicalVolume and the logical volume on the node behind.
- An LVMLogicalVolume of the module keeps the `storage.deckhouse.io/sds-local-volume-csi` finalizer and is not deleted until the finalizer is removed by hand.
