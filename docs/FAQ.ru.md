---
title: "Модуль sds-local-volume: FAQ"
description: "Модуль sds-local-volume: FAQ"
---

## Когда следует использовать LVM, а когда LVMThin?

- LVM проще и обладает высокой производительностью, сравнимой с производительностью накопителя, но не позволяет использовать snapshot'ы;
- LVMThin позволяет использовать snapshot'ы и overprovisioning, но производительность ниже, чем у LVM.

## Как назначить StorageClass по умолчанию?

В соответствующем пользовательском ресурсе [LocalStorageClass](./cr.html#localstorageclass) в поле `spec.isDefault` указать `true`.  
