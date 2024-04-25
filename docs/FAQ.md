---
title: "The sds-local-volume module: FAQ"
description: "The sds-local-volume module: FAQ"
---

## What is difference between LVM and LVMThin?

- LVM is simpler and has high performance that is similar to that of native disk drives;
- LVMThin allows overprovisioning; however, it is slower than LVM.

{{< alert level="warning" >}} 
Overprovisioning in LVMThin should be used with caution, monitoring the availability of free space in the pool (The cluster monitoring system generates separate events when the free space in the pool reaches 20%, 10%, 5%, and 1%).

In case of no free space in the pool, degradation in the module's operation as a whole will be observed, and there is a real possibility of data loss!
{{< /alert >}}

## How do I set the default StorageClass?

Set the `spec.IsDefault` field to `true` in the corresponding [LocalStorageClass](./cr.html#localstorageclass) custom resource.
