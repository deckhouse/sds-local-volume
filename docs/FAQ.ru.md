---
title: "Модуль sds-local-volume: FAQ"
description: "Модуль sds-local-volume: FAQ"
---

## Когда следует использовать LVM, а когда LVMThin?

- LVM проще и обладает высокой производительностью, сравнимой с производительностью накопителя;
- LVMThin позволяет использовать overprovisioning, но производительность ниже, чем у LVM.

## Как назначить StorageClass по умолчанию?

В соответствующем пользовательском ресурсе [LocalStorageClass](./cr.html#localstorageclass) в поле `spec.isDefault` указать `true`.  
