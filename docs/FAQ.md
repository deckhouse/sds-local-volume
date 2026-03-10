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

## When to use RawFile?

Use RawFile when:

- **No dedicated block devices** — you want to use available filesystem space instead of managing LVM
- **Quick setup** — you need storage without configuring LVMVolumeGroup resources
- **Development/testing** — for non-production environments where simplicity is more important than performance
- **LVM is not available** — when the system doesn't have LVM configured or available

**Avoid RawFile when:**

- **Maximum performance is required** — loop device overhead reduces performance compared to LVM or direct block access
- **Snapshots are needed** — RawFile doesn't support volume snapshots
- **Data security is critical** — `volumeCleanup` is not supported for RawFile volumes

For more details, see [Using RawFile volumes](./usage.html#using-rawfile-volumes).

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
