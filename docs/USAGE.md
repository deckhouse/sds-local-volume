---
title: "The sds-local-volume module: usage"
description: "The sds-local-volume module usage: volume cleanup, node configuration, data migration, and snapshots."
weight: 5
---

## Volume cleanup on deletion

{{< alert level="warning" >}}
Volume cleanup is available only in commercial editions of Deckhouse.
{{< /alert >}}

When files are deleted, the operating system does not physically delete the content but only marks the corresponding blocks as "free". If a new volume receives physical blocks previously used by another volume, data from the previous user may remain in them.

### Data leakage scenario example

1. User #1 placed files in a volume requested from StorageClass 1 on node 1 (in "Block" or "Filesystem" mode).
1. User #1 deleted the files and the volume.
1. The physical blocks that the volume occupied become "free" but are not overwritten.
1. User #2 requested a new volume from StorageClass 1 on node 1 in "Block" mode.
1. There is a risk that some or all blocks previously occupied by user #1 will be allocated to user #2 again.
1. In this case, user #2 may recover user #1's data.

### Thick volumes

To prevent data leakage through thick volumes, the `volumeCleanup` parameter is provided, which allows you to select a method for cleaning the volume before deleting the PersistentVolume (PV).

Possible values:

- Parameter not set: No additional actions are performed when deleting the volume. Data may remain accessible to the next user.

- `RandomFillSinglePass`: The volume is overwritten with random data once before deletion. **Not recommended** for solid-state drives, as it reduces the drive's lifespan.

- `RandomFillThreePass`: The volume is overwritten with random data three times before deletion. **Not recommended** for solid-state drives, as it reduces the drive's lifespan.

- `Discard`: All blocks of the volume are marked as free using the `discard` system call before deletion. Use this option only for solid-state drives.

Most modern solid-state drives guarantee that a block marked with `discard` will not return previous data when read. This makes the `Discard` option the most effective way to prevent leaks when using solid-state drives.

However, clearing a cell is a relatively long operation, so it is performed by the device in the background. In addition, many drives cannot clear individual cells, only groups — pages. Because of this, not all drives guarantee immediate unavailability of freed data. In addition, not all drives that guarantee this keep their promise.

Do not use a device if it does not guarantee Deterministic TRIM (DRAT), Deterministic Read Zero after TRIM (RZAT) and is not verified.

### Thin volumes

When a thin volume block is freed via `discard` from the guest operating system, this command is forwarded to the device. When using a hard disk or lack of `discard` support from the solid-state drive, data may remain on the thin pool until such a block is reused.

Users are provided access only to thin volumes, not to the thin pool itself. They can only get a volume from the pool. For thin volumes, the thin pool block is zeroed when reused, which prevents leaks between clients. This is guaranteed by the `thin_pool_zero=1` setting in LVM.

## Migrating data between PVCs

Use the following script to transfer data from one PVC to another:

1. Copy the script to a file `migrate.sh` on any master node.

1. Use the script with parameters: `migrate.sh NAMESPACE SOURCE_PVC_NAME DESTINATION_PVC_NAME`

```shell
#!/bin/bash

ns=$1
src=$2
dst=$3

if [[ -z $3 ]]; then
  echo "You must give as args: namespace source_pvc_name destination_pvc_name"
  exit 1
fi

echo "Creating job yaml"
cat > migrate-job.yaml << EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: migrate-pv-$src
  namespace: $ns
spec:
  template:
    spec:
      containers:
      - name: migrate
        image: debian
        command: [ "/bin/bash", "-c" ]
        args:
          -
            apt-get update && apt-get install -y rsync &&
            ls -lah /src_vol /dst_vol &&
            df -h &&
            rsync -avPS --delete /src_vol/ /dst_vol/ &&
            ls -lah /dst_vol/ &&
            du -shxc /src_vol/ /dst_vol/
        volumeMounts:
        - mountPath: /src_vol
          name: src
          readOnly: true
        - mountPath: /dst_vol
          name: dst
      restartPolicy: Never
      volumes:
      - name: src
        persistentVolumeClaim:
          claimName: $src
      - name: dst
        persistentVolumeClaim:
          claimName: $dst
  backoffLimit: 1
EOF

kubectl create -f migrate-job.yaml
kubectl -n $ns get jobs -o wide
kubectl_completed_check=0

echo "Waiting for data migration to be completed"
while [[ $kubectl_completed_check -eq 0 ]]; do
   kubectl -n $ns get pods | grep migrate-pv-$src
   sleep 5
   kubectl_completed_check=`kubectl -n $ns get pods | grep migrate-pv-$src | grep "Completed" | wc -l`
done
echo "Data migration completed"
```

## Creating volume snapshots

{{< alert level="warning" >}}
The ability to work with volume snapshots is available only in commercial editions of Deckhouse Kubernetes Platform and only when using LVM-thin volumes.

To work with volume snapshots, the [snapshot-controller](/modules/snapshot-controller/) module must be connected.
{{< /alert >}}

For detailed information about snapshots, see the [Kubernetes documentation](https://kubernetes.io/docs/concepts/storage/volume-snapshots/).

To create a volume snapshot, follow these steps:

1. Enable the `snapshot-controller` module:

   ```shell
   d8 s module enable snapshot-controller
   ```

1. To create a volume snapshot, run the following command with the necessary parameters:

   ```shell
   d8 k apply -f -<<EOF
   apiVersion: snapshot.storage.k8s.io/v1
   kind: VolumeSnapshot
   metadata:
     name: my-snapshot
     namespace: <namespace-name> # Namespace name where the PVC is located
   spec:
     volumeSnapshotClassName: sds-local-volume-snapshot-class
     source:
       persistentVolumeClaimName: <pvc-name> # Name of the PVC for which the snapshot is created
   EOF
   ```

   > **Warning:** the `sds-local-volume-snapshot-class` class is created automatically. The `deletionPolicy` parameter is set to `Delete`, so `VolumeSnapshotContent` is deleted when the associated `VolumeSnapshot` is deleted.

1. Check the snapshot status:

   ```shell
   d8 k get volumesnapshot
   ```

   The command outputs a list of all snapshots and their current status.

## Setting StorageClass as default

Add the `storageclass.kubernetes.io/is-default-class: "true"` annotation to the corresponding StorageClass resource:

```shell
d8 k annotate storageclasses.storage.k8s.io <storageClassName> storageclass.kubernetes.io/is-default-class=true
```

## Selecting nodes for module operation

The module uses nodes that have the labels specified in the `nodeSelector` field in the module settings. To do this:

1. To display the module settings, run the command:

   ```shell
   d8 k edit mc sds-local-volume
   ```

   Example output:

   ```console
   apiVersion: deckhouse.io/v1alpha1
   kind: ModuleConfig
   metadata:
     name: sds-local-volume
   spec:
     enabled: true
     settings:
       dataNodes:
         nodeSelector:
           my-custom-label-key: my-custom-label-value
   status:
     message: ""
     version: "1"
   ```

1. Run the command to view labels in the `nodeSelector` field:

   ```shell
   d8 k get mc sds-local-volume -o=jsonpath={.spec.settings.dataNodes.nodeSelector}
   ```

   Example output:

   ```console
   nodeSelector:
     my-custom-label-key: my-custom-label-value
   ```

1. The module selects nodes that have all the specified labels. Modify the `nodeSelector` field to change the list of nodes.

   > **Warning:** you can specify any number of labels in the `nodeSelector` field. All specified labels must be present on the node. The module starts the `csi-node` pod only on nodes that have all the specified labels.

1. After adding labels, verify that the `csi-node` pods are running on the nodes:

   ```shell
   d8 k -n d8-sds-local-volume get pod -owide
   ```

## Removing a node from module management

To remove a node from module management, remove the labels specified in the `nodeSelector` field in the module settings. To do this:

1. Run the command to view labels in `nodeSelector`:

   ```shell
   d8 k get mc sds-local-volume -o=jsonpath={.spec.settings.dataNodes.nodeSelector}
   ```

   Example output:

   ```console
   nodeSelector:
     my-custom-label-key: my-custom-label-value
   ```

1. Remove the specified labels from the node:

   ```shell
   d8 k label node %node-name% %label-from-selector%-
   ```

   > **Warning:** to remove a label, add a minus sign after the label key instead of the value.

1. Verify that the `csi-node` pod is removed from the node:

   ```shell
   d8 k -n d8-sds-local-volume get po -owide
   ```

If the `csi-node` pod remains on the node after removing labels:

1. Verify that the labels are actually removed from the node:

   ```shell
   d8 k get node <node-name> --show-labels
   ```

1. Ensure that the node has no [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) resources that are used in [LocalStorageClass](cr.html#localstorageclass) resources. For more details, see [Checking dependent LVMVolumeGroup resources on a node](#checking-dependent-lvmvolumegroup-resources-on-a-node).

{{< alert level="warning" >}}
Note that [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) and [LocalStorageClass](cr.html#localstorageclass) resources that prevent the node from being removed from module management will display the `storage.deckhouse.io/sds-local-volume-candidate-for-eviction` label.

The node itself will have the `storage.deckhouse.io/sds-local-volume-need-manual-eviction` label.
{{< /alert >}}

## Creating thin storage

1. Get a list of available [BlockDevice](/modules/sds-node-configurator/cr.html#blockdevice) resources in the cluster:

   ```shell
   d8 k get bd
   ```

   Example output:

   ```console
   NAME                                           NODE       CONSUMABLE   SIZE           PATH
   dev-ef4fb06b63d2c05fb6ee83008b55e486aa1161aa   worker-0   false        100Gi          /dev/nvme1n1
   dev-7e4df1ddf2a1b05a79f9481cdf56d29891a9f9d0   worker-1   false        100Gi          /dev/nvme1n1
   dev-53d904f18b912187ac82de29af06a34d9ae23199   worker-2   false        100Gi          /dev/nvme1n1
   ```

1. Create an [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) resource for the `worker-0` node:

   ```shell
   d8 k apply -f - <<EOF
   apiVersion: storage.deckhouse.io/v1alpha1
   kind: LVMVolumeGroup
   metadata:
    name: "vg-2-on-worker-0"
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
     actualVGNameOnTheNode: "vg-2"
     thinPools:
     - name: thindata
       size: 100Gi
   EOF
   ```

1. Wait for the [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) resource to transition to the `Ready` state:

   ```shell
   d8 k get lvg vg-2-on-worker-0 -w
   ```

   After transitioning to the `Ready` state, an LVM volume group named `vg-2` and a thin pool named `thindata` will be created on the `worker-0` node from the block device `/dev/nvme1n1`.

1. Create an [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) resource for the `worker-1` node:

   ```shell
   d8 k apply -f - <<EOF
   apiVersion: storage.deckhouse.io/v1alpha1
   kind: LVMVolumeGroup
   metadata:
     name: "vg-2-on-worker-1"
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
     actualVGNameOnTheNode: "vg-2"
     thinPools:
     - name: thindata
       size: 100Gi
   EOF
   ```

1. Wait for the [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) resource to transition to the `Ready` state:

   ```shell
   d8 k get lvg vg-2-on-worker-1 -w
   ```

   After transitioning to the `Ready` state, an LVM volume group named `vg-2` and a thin pool named `thindata` will be created on the `worker-1` node from the block device `/dev/nvme1n1`.

1. Create an [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) resource for the `worker-2` node:

   ```shell
   d8 k apply -f - <<EOF
   apiVersion: storage.deckhouse.io/v1alpha1
   kind: LVMVolumeGroup
   metadata:
     name: "vg-2-on-worker-2"
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
     actualVGNameOnTheNode: "vg-2"
     thinPools:
     - name: thindata
       size: 100Gi
   EOF
   ```

1. Wait for the [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) resource to transition to the `Ready` state:

   ```shell
   d8 k get lvg vg-2-on-worker-2 -w
   ```

   After transitioning to the `Ready` state, an LVM volume group named `vg-2` and a thin pool named `thindata` will be created on the `worker-2` node from the block device `/dev/nvme1n1`.

1. Create a [LocalStorageClass](./cr.html#localstorageclass) resource:

   ```shell
   d8 k apply -f -<<EOF
   apiVersion: storage.deckhouse.io/v1alpha1
   kind: LocalStorageClass
   metadata:
     name: local-storage-class
   spec:
     lvm:
       lvmVolumeGroups:
        - name: vg-2-on-worker-0
          thin:
            poolName: thindata
        - name: vg-2-on-worker-1
          thin:
            poolName: thindata
        - name: vg-2-on-worker-2
          thin:
            poolName: thindata
       type: Thin
     reclaimPolicy: Delete
     volumeBindingMode: WaitForFirstConsumer
   EOF
   ```

1. Wait for the [LocalStorageClass](cr.html#localstorageclass) resource to transition to the `Created` state:

   ```shell
   d8 k get lsc local-storage-class -w
   ```

1. Verify that the corresponding StorageClass is created:

   ```shell
   d8 k get sc local-storage-class
   ```

You can now create PVCs, specifying the StorageClass named `local-storage-class`.

## Checking dependent LVMVolumeGroup resources on a node

Follow these steps:

1. Display [LocalStorageClass](cr.html#localstorageclass) resources:

   ```shell
   d8 k get lsc
   ```

1. Check the list of used [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) resources in each [LocalStorageClass](cr.html#localstorageclass).

   Display the contents of all [LocalStorageClass](cr.html#localstorageclass) resources:

   ```shell
   d8 k get lsc -oyaml
   ```

   Or display the contents of a specific resource:

   ```shell
   d8 k get lsc <lsc-name> -oyaml
   ```

   Example of a [LocalStorageClass](cr.html#localstorageclass) resource:

   ```yaml
   apiVersion: v1
   items:
   - apiVersion: storage.deckhouse.io/v1alpha1
     kind: LocalStorageClass
     metadata:
       finalizers:
       - storage.deckhouse.io/local-storage-class-controller
       name: test-sc
     spec:
       fsType: ext4
       lvm:
         lvmVolumeGroups:
         - name: test-vg
         type: Thick
       reclaimPolicy: Delete
       volumeBindingMode: WaitForFirstConsumer
     status:
       phase: Created
   kind: List
   ```

   The `spec.lvm.lvmVolumeGroups` field lists the used [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) resources.

1. Display the list of [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) resources:

   ```shell
   d8 k get lvg
   ```

   Example output:

   ```console
   NAME                 THINPOOLS   CONFIGURATION APPLIED   PHASE   NODE                   SIZE     ALLOCATED SIZE   VG    AGE
   vg0-on-astra-1-8     0/0         True                    Ready   astra-1-8              5116Mi   0                vg0   180d
   vg0-on-master-0      0/0         True                    Ready   p-master-0             5116Mi   0                vg0   182d
   vg0-on-redos-murom   0/0         True                    Ready   redos-murom            5116Mi   0                vg0   32d
   vg0-on-worker-1      0/0         True                    Ready   p-worker-1             5116Mi   0                vg0   225d
   vg0-on-worker-2      0/0         True                    Ready   p-worker-2             5116Mi   0                vg0   225d
   vg1-on-redos-murom   1/1         True                    Ready   redos-murom            3068Mi   3008Mi           vg1   32d
   vg1-on-worker-1      1/1         True                    Ready   p-worker-1             3068Mi   3068Mi           vg1   190d
   vg1-on-worker-2      1/1         True                    Ready   p-worker-2             3068Mi   3068Mi           vg1   190d
   ```

1. Verify that the node has no [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) resources that are used in [LocalStorageClass](cr.html#localstorageclass) resources.

   Before removing a node from module management, delete dependent resources manually to avoid losing control over created volumes.

