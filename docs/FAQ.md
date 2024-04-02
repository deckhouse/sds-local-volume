---
title: "The sds-local-volume module: FAQ"
description: "The sds-local-volume module: FAQ"
---

## What is difference between LVM and LVMThin?

- LVM is simpler and has high performance that is similar to that of native disk drives, but it does not support snapshots;
- LVMThin allows for snapshots and overprovisioning; however, it is slower than LVM.

## How do I set the default StorageClass?

Set the `spec.IsDefault` field to `true` in the corresponding [LocalStorageClass](./cr.html#localstorageclass) custom resource.
