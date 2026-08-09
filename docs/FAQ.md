---
title: "The sds-local-volume module: FAQ"
description: "The sds-local-volume module FAQ"
weight: 6
---

## When to use LVM and when to use LVM-thin?

Use LVM (Thick) if you need maximum performance comparable to the drive's performance. LVM (Thick) is easier to configure.

Use LVM-thin if you need to use overprovisioning. LVM-thin performance is lower than LVM.

{{< alert level="warning" >}}
Use overprovisioning in LVM-thin with caution. Monitor the available space in the pool. The cluster monitoring system has separate events when the pool reaches 20%, 10%, 5%, and 1% free space.

If there is no free space in the pool, module degradation and data loss may occur.
{{< /alert >}}

## Why can't I create a PVC on the selected node?

Check that the `csi-node` pod is running on the selected node:

```shell
d8 k -n d8-sds-local-volume get po -owide
```

If the pod is missing, ensure that the node has all the labels specified in the `nodeSelector` field in the module settings. For more details, see [Why are the sds-local-volume component service pods not created on the desired node?](#why-are-the-sds-local-volume-component-service-pods-not-created-on-the-desired-node).



## Why did the csi-node pod remain on the node after removing labels?

The node likely has [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) resources that are used in [LocalStorageClass](cr.html#localstorageclass) resources.

Delete the dependent resources manually to avoid losing control over the created volumes. For instructions on checking dependent resources, see the [Checking dependent LVMVolumeGroup resources on a node](./usage.html#checking-dependent-lvmvolumegroup-resources-on-a-node) section.

## Why are the sds-local-volume component service pods not created on the desired node?

The issue is likely related to node labels. The module uses nodes that have the labels specified in the `nodeSelector` field in the module settings.

1. Run the command to view labels in `nodeSelector`:

   ```shell
   d8 k get mc sds-local-volume -o=jsonpath={.spec.settings.dataNodes.nodeSelector}
   ```

   Example output:

   ```console
   nodeSelector:
     my-custom-label-key: my-custom-label-value
   ```

1. Check the selectors that the module uses in the `d8-sds-local-volume-controller-config` secret:

   ```shell
   d8 k -n d8-sds-local-volume get secret d8-sds-local-volume-controller-config -o jsonpath='{.data.config}' | base64 --decode
   ```

   Example output:

   ```console
   nodeSelector:
     kubernetes.io/os: linux
     my-custom-label-key: my-custom-label-value
   ```

   The output should include all labels from the module's `data.nodeSelector` settings, as well as `kubernetes.io/os: linux`.

1. Check the labels on the node:

   ```shell
   d8 k get node <node-name> --show-labels
   ```

1. Add the missing labels to the node:

   ```shell
   d8 k label node <node-name> my-custom-label-key=my-custom-label-value
   ```

1. If the labels are present, check for the `storage.deckhouse.io/sds-local-volume-node=` label on the node. If the label is missing, check the controller status:

   ```shell
   d8 k -n d8-sds-local-volume get po -l app=sds-local-volume-controller
   d8 k -n d8-sds-local-volume logs -l app=sds-local-volume-controller
   ```

## How do I reclaim the space of a volume created with reclaimPolicy: Retain?

With `reclaimPolicy: Retain`, deleting a PersistentVolumeClaim leaves both the PersistentVolume and the data in place: reclaiming the storage is a deliberate administrative step. Deleting the PersistentVolume is not that step — it removes the Kubernetes object, while the logical volume stays allocated in the volume group and is described by an [LVMLogicalVolume](/modules/sds-node-configurator/cr.html#lvmlogicalvolume) resource with the same name as the PersistentVolume.

To free the space, delete that resource:

```shell
d8 k delete lvmlogicalvolume <persistent-volume-name>
```

The module removes its own finalizer, `sds-node-configurator` runs `lvremove`, and the space returns to the volume group. This is irreversible: the data is destroyed.

The data is erased before the logical volume is removed exactly as `LocalStorageClass.spec.lvm.volumeCleanup` said when the volume was created — the value is recorded in `spec.volumeCleanup` of the resource. A later change to the LocalStorageClass is not picked up here, because there is no PersistentVolume left to resolve the class through. Check it, and set it, before deleting:

```shell
d8 k get llv <persistent-volume-name> -o jsonpath='{.spec.volumeCleanup}'
d8 k patch llv <persistent-volume-name> --type=merge -p '{"spec":{"volumeCleanup":"RandomFillSinglePass"}}'
```

To find the volumes whose PersistentVolume is already gone, and the space they hold:

```shell
d8 k get lvmlogicalvolume -o custom-columns=NAME:.metadata.name,VG:.spec.lvmVolumeGroupName,TYPE:.spec.type,SIZE:.status.actualSize,PHASE:.status.phase
d8 k get pv
```

Read the size per the type: for a `Thick` volume it is the space held in the volume group, for a `Thin` one it is the virtual size, which says nothing about what the thin pool consumes. Deleting thin volumes to reclaim the number in that column will free less than it promises.

The same information is available in the monitoring stack: the `sds_local_volume_orphaned_lvm_logical_volume_count` and `sds_local_volume_orphaned_lvm_logical_volume_allocated_bytes` metrics, the "SDS Local Volume" Grafana dashboard, and the `D8SdsLocalVolumeOrphanedLVMLogicalVolumes` alert.

If a volume is meant to outlive its PersistentVolume, say so instead of silencing that alert:

```shell
d8 k annotate lvmlogicalvolume <name> storage.deckhouse.io/retain-acknowledged=true
```

An acknowledged volume is reported as `state="retained"`, which no alert reads, so the alert goes back to meaning "something is leaking that nobody has looked at". The annotation protects nothing: a deletion is still unblocked normally when one is asked for.

If the deletion does not complete, the module is keeping its finalizer, and it reports why per volume in the `reason` label of `sds_local_volume_orphaned_lvm_logical_volume_allocated_bytes{state="blocked"}` — the "Which volumes are leaking" table on the dashboard shows it as the "Reason" column. The same reasons are in the controller log:

```shell
d8 k -n d8-sds-local-volume logs deploy/controller -c controller | grep -E "refusing to unblock|keeping the finalizer"
```

The reasons are:

- `snapshots_present` — the logical volume still has snapshots taken from it. Delete them first, with `d8 k delete lvmlogicalvolumesnapshot <name>`: the module unblocks a snapshot deletion on the same rule it unblocks a volume one, so this does not hang;
- `agent_finalizer_absent` — no `sds-node-configurator` agent has taken ownership of the resource, so deleting it would leave the logical volume on the node with nothing pointing at it. Check the node and its [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup);
- `persistent_volume_exists` — a PersistentVolume still refers to the volume, by name or by CSI volume handle, so the volume may still be in use. The `driver` field in the log says whether it is a PersistentVolume of this module or merely one whose name collides;
- `successor_in_place` — the name is now held by a different object than the one the module looked at, because `external-provisioner` reused the volume ID on a retry. Nothing to do: the object now holding the name is considered on its own terms on the next pass, and this reason disappears with it;
- `api_error` or `removal_failed` — the module could not confirm the reclaim, or could not write the finalizer off. Unlike the reasons above these are faults in the module rather than properties of the volume, and `D8SdsLocalVolumeCSIFinalizerRemovalErrors` fires for the second one.

Snapshots are collected on the same rule and reported the same way, in `sds_local_volume_orphaned_lvm_logical_volume_snapshot_count` and `sds_local_volume_orphaned_lvm_logical_volume_snapshot_used_bytes`, with one reason of their own: `volume_snapshot_content_exists` means a VolumeSnapshotContent still refers to the snapshot, so the snapshot is live and only the resource underneath it was deleted. Delete the VolumeSnapshot instead and let the snapshot-controller take the rest down.

A deletion that carries no finalizer of this module any more, and still carries the `sds-node-configurator` one, is counted in `sds_local_volume_awaiting_agent_count`. Every deletion passes through that state, and a volume with `spec.volumeCleanup` set is written over completely before `lvremove` runs, so time in this series is normal rather than a fault: `D8SdsLocalVolumeLVMLogicalVolumeDeletionStuck` fires only after two hours of it. The series also counts resources this module never owned — the absence of its finalizer cannot tell "already unblocked" from "never ours" — and no orphan metric covers any of them, for the same reason.

The delay between asking for a volume to be deleted and the module removing its finalizer exists so that an in-flight `DeleteVolume` call gets to remove it first. It is 30 seconds by default and is set by the `llvOrphanGracePeriod` module setting.
