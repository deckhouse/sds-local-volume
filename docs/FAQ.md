---
title: "The sds-local-volume module: FAQ"
description: "The sds-local-volume module FAQ"
weight: 6
---

## When to use LVM and when to use LVM-thin?

Use LVM (Thick) if you need maximum performance comparable to the drive's performance. LVM (Thick) is easier to configure.

Use LVM-thin if you need to use overprovisioning. LVM-thin performance is lower than LVM.

{{< alert level="warning" >}}
How far a thin pool can be overprovisioned is set by the `allocationLimit` field of the thin pool in the [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) resource: the total size of the volumes and snapshots in the pool may not exceed the pool size multiplied by this value. The default is `150%`, which allows overprovisioning. Set `100%` to turn it off: a volume, an expansion or a snapshot that does not fit is then refused instead of being created. For details, see [How does allocationLimit of a thin pool work, and can I change it?](/modules/sds-node-configurator/faq.html#how-does-allocationlimit-of-a-thin-pool-work-and-can-i-change-it).

With a limit above `100%`, monitor the free space in the pool, for example with the `sds_node_configurator_lvm_thin_pool_used_percent` and `sds_node_configurator_lvm_thin_pool_metadata_used_percent` metrics. If there is no free space in the pool, module degradation and data loss may occur.
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

## How much of a volume is reserved for the superuser?

Nothing, by default: the module formats ext4 with `-m 0`, so the whole volume is available to the workload.

Where the classic 5% ext4 reserve is wanted — for example to keep a filesystem writable for a privileged process after a workload has filled it — annotate the `LocalStorageClass` with the percentage to reserve:

```shell
d8 k annotate localstorageclass <lsc-name> storage.deckhouse.io/ext4-reserved-percent=5
```

The value is a whole number of percent between 0 and 50; an invalid one is rejected by the validating webhook.

The reserve applies only to volumes created after the annotation was set — filesystems that already exist keep the reserve they were created with. Only the ext filesystems keep blocks for the superuser: with `fsType: xfs` the annotation is ignored, and the webhook returns a warning saying so.

Changing the annotation makes the controller recreate the StorageClass, because `parameters` of an existing StorageClass are immutable in Kubernetes. Existing volumes and PVCs are not affected.

## Why does the `D8SdsLocalVolumeLocalStorageClassNotReady` alert fire?

The `Ready` condition of a LocalStorageClass has not been `True` for 15 minutes. Read the condition to see which of the two causes applies:

```shell
d8 k get lsc <lsc-name> -o jsonpath='{.status.conditions[?(@.type=="Ready")]}'
```

- `reason: ReconcileFailed` — the controller could not bring the class to its spec, and the message says why. Fix the spec or the LVMVolumeGroups it refers to; the controller retries on its own.
- `reason: Deleting` — the class was asked to be deleted, and a finalizer of another controller still holds it. The module has already deleted its StorageClass. Look for the cause on the side of the controller that owns the finalizer rather than removing the finalizer by hand.

If the command prints nothing, the controller has not been able to write the status of the class. The controller log says why:

```shell
d8 k -n d8-sds-local-volume logs deploy/controller -c controller --tail=200 | grep <lsc-name>
```

The Events of the class tell when the condition changed and what it said before: the controller records a `Warning` Event when `Ready` turns `False` and a `Normal` one when it turns `True` again, with the reason of the condition. The class is cluster-scoped, so its Events are in the `default` namespace, and the API server keeps them for an hour by default:

```shell
d8 k describe lsc <lsc-name>
```

A class whose LVMVolumeGroups are partly on nodes without the module's CSI node stays `Ready` with `reason: LVMVolumeGroupsOutsideCSINodes` and does not trigger the alert.

## How do I see how much space a LocalStorageClass has left?

Open the "SDS Local Volume" Grafana dashboard, pick the class in the "LocalStorageClass" variable at the top, and read the "Capacity by LocalStorageClass" row:

- For a `Thick` class: the size of its volume groups, their free space, and the share used. The share counts everything in the volume groups, thin pools and volumes of other classes on the same LVMVolumeGroups included.
- For a `Thin` class: the size of its thin pools, the size allocated to volumes, the allocation limit, the space free by `allocationLimit`, and the share of the pools filled with data. The space free by `allocationLimit` is the pool size multiplied by `allocationLimit` of the thin pool in the LVMVolumeGroup (150% by default), less the requested sizes of the volumes already in the pool, counted per thin pool. It is what new volumes of the class can still be given — the same figure the module checks when it creates a volume — so overprovisioning is taken into account. It says nothing about the data the pool can still hold: watch the share filled with data, because a thin pool that fills up stops every volume in it.
- The table at the bottom of the row shows the same figures per node and LVMVolumeGroup.
- "LVMVolumeGroups without capacity data" counts the LVMVolumeGroups of the class (thin pools for a `Thin` class) that sds-node-configurator reports no capacity for: the LVMVolumeGroup does not exist, its volume group or thin pool has not been created yet, or the agent on its node is not running. The totals leave them out.

The capacity comes from the sds-node-configurator metrics `sds_node_configurator_lvg_vg_size_bytes`, `sds_node_configurator_lvg_vg_free_bytes` and `sds_node_configurator_lvg_thin_pool_*_bytes`, which are labeled by LVMVolumeGroup. The module ties them to classes with the `sds_local_volume_local_storage_class_lvg_info` metric: `1` for every LVMVolumeGroup a class provisions on, with the labels `storage_class`, `lvg_name` and `thin_pool` (empty for a `Thick` class). `labelSelector` entries are resolved the same way as for the StorageClass, and the series follow changes to the class and to the labels of the LVMVolumeGroups; a deleted class stops being exported.

To use the metric in your own queries, pick one class and join on `lvg_name`, adding `thin_pool` for a `Thin` class. For example, the free space of a `Thick` class:

```text
sum(
  sds_node_configurator_lvg_vg_free_bytes
  * on (lvg_name) group_left (storage_class)
  max by (storage_class, lvg_name) (sds_local_volume_local_storage_class_lvg_info{storage_class="<lsc-name>", thin_pool=""})
)
```

Pick the class first: one LVMVolumeGroup can be used by several classes, and the join fails when the right-hand side has more than one series per LVMVolumeGroup. `max by` keeps the join working while the controller changes its leader.
