---
title: "Модуль sds-local-volume: настройки"
force_searchable: true
description: Параметры настройки модуля sds-local-volume.
weight: 3
---

## Включение модуля

Для включения модуля `sds-local-volume` выполните следующие шаги:

{{< alert level="info" >}}
Все команды должны быть выполнены на машине с доступом к API Kubernetes и правами администратора.
{{< /alert >}}

1. Включите модуль `sds-local-volume`:

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

1. Дождитесь перехода модуля `sds-local-volume` в состояние `Ready`:

   ```shell
   d8 k get modules sds-local-volume -w
   ```

1. Проверьте, что поды модуля запущены:

   ```shell
   d8 k -n d8-sds-local-volume get pod -owide
   d8 k -n d8-sds-node-configurator get pod -o wide
   ```

Подробные инструкции по настройке модуля см. в разделе [Быстрый старт](./quick_start.html).

## Отключение модуля

Отключение модуля удаляет его контроллер и CSI-драйвер — и ничего больше. Логические тома остаются на узлах вместе с данными, а ресурсы PersistentVolume, [LVMLogicalVolume](/modules/sds-node-configurator/cr.html#lvmlogicalvolume) и [LocalStorageClass](./cr.html#localstorageclass) остаются в кластере. Модуль снимает финализаторы только с ресурсов LocalStorageClass и их StorageClass.

Удалите тома и классы, пока модуль ещё работает:

1. Найдите тома модуля и удалите их PersistentVolumeClaim, включая PVC модулей Deckhouse (например, мониторинга), которые используют StorageClass модуля:

   ```shell
   d8 k get pv -o jsonpath='{range .items[?(@.spec.csi.driver=="local.csi.storage.deckhouse.io")]}{.metadata.name}{"\t"}{.spec.claimRef.namespace}/{.spec.claimRef.name}{"\t"}{.spec.persistentVolumeReclaimPolicy}{"\n"}{end}'
   ```

   Дождитесь, пока команда перестанет что-либо выводить. Том с `reclaimPolicy: Retain` остаётся после удаления своего PersistentVolumeClaim: освободите его место, как описано в разделе [Как освободить место тома, созданного с reclaimPolicy: Retain?](./faq.html#как-освободить-место-тома-созданного-с-reclaimpolicy-retain).

   Удаляйте PersistentVolumeClaim раньше LocalStorageClass. При удалении тома драйвер берёт `spec.lvm.volumeCleanup` из LocalStorageClass; если класса уже нет, том удаляется без затирания данных.

1. Если один из StorageClass модуля используется в кластере по умолчанию (параметр `global.defaultClusterStorageClass` или аннотация `storageclass.kubernetes.io/is-default-class`), назначьте классом по умолчанию другой.

1. Удалите ресурсы LocalStorageClass. Их StorageClass контроллер удалит сам; удалить такой StorageClass напрямую не получится — запрос будет отклонён.

   ```shell
   d8 k get localstorageclasses.storage.deckhouse.io
   d8 k delete localstorageclasses.storage.deckhouse.io <name>
   ```

1. Отключите модуль:

   ```shell
   d8 k patch moduleconfig sds-local-volume --type=merge -p '{"spec":{"enabled":false}}'
   ```

1. Если группы томов LVM больше не нужны, удалите их и отключите модуль `sds-node-configurator`, как описано в [его документации](/modules/sds-node-configurator/faq.html#как-отключить-модуль), если его не используют другие модули (`sds-replicated-volume`, `sds-elastic`).

Что происходит с томами, оставшимися в кластере после отключения модуля:

- Уже запущенные поды продолжают работать со своими томами; под, который запускается заново или переезжает на другой узел, не сможет смонтировать том модуля.
- PersistentVolumeClaim, удалённый после отключения, оставляет после себя PersistentVolume, LVMLogicalVolume и логический том на узле.
- LVMLogicalVolume модуля сохраняет финализатор `storage.deckhouse.io/sds-local-volume-csi` и не удаляется, пока финализатор не снимут вручную.
