---
title: "Модуль sds-local-volume: быстрый старт."
linkTitle: Быстрый старт
description: "Быстрый старт модуля sds-local-volume."
weight: 2
---

Модуль поддерживает три режима работы: LVM (Thick), LVM-thin и RawFile. Каждый режим имеет свои особенности, преимущества и ограничения. См. подробнее о различиях между режимами LVM в [FAQ](./faq.html#когда-следует-использовать-lvm-а-когда-lvm-thin). Информация о RawFile-томах доступна в разделе [Использование RawFile-томов](./usage.html#использование-rawfile-томов).

## Быстрый старт

Ниже описан пример настройки модуля для создания Thick-хранилища на трёх узлах кластера: включение модулей через [ModuleConfig](/modules/deckhouse/configuration.html), создание групп томов LVM на каждом узле через [LVMVolumeGroup](/modules/sds-node-configurator/stable/cr.html#lvmvolumegroup) и создание [LocalStorageClass](./cr.html#localstorageclass) для использования при создании PVC.

### Включение модуля

Для корректной работы модуля `sds-local-volume` выполните следующие шаги:

{{< alert level="info" >}}
Все команды ниже должны быть выполнены на машине с доступом к API Kubernetes и правами администратора.
{{< /alert >}}

1. Включите модуль `sds-node-configurator`, выполнив команду ниже:

   ```shell
   d8 k apply -f - <<EOF
   apiVersion: deckhouse.io/v1alpha1
   kind: ModuleConfig
   metadata:
     name: sds-node-configurator
   spec:
     enabled: true
     version: 1
     settings:
       enableThinProvisioning: true # Если планируете использовать LVM-thin тома
   EOF
   ```

1. Дождитесь перехода модуля `sds-node-configurator` в состояние `Ready`. На этом этапе проверка подов в пространстве имён `d8-sds-node-configurator` не требуется.

   ```shell
   d8 k get modules sds-node-configurator -w
   ```

1. Ознакомьтесь с [доступными настройками](./configuration.html) модуля `sds-local-volume` перед его включением. После ознакомления включите модуль, выполнив команду ниже:

   ```shell
   d8 k apply -f - <<EOF
   apiVersion: deckhouse.io/v1alpha1
   kind: ModuleConfig
   metadata:
     name: sds-local-volume
   spec:
     enabled: true
     version: 1
     settings:
       enableThinProvisioning: true # если планируете использовать LVM-thin тома
   EOF
   ```

   В результате модуль запустится с настройками по умолчанию, что приведет к созданию служебных подов компонента `sds-local-volume` на всех узлах кластера:

1. Дождитесь перехода модуля `sds-local-volume` в состояние `Ready`.

   ```shell
   d8 k get modules sds-local-volume -w
   ```

1. Убедитесь, что в пространствах имён `d8-sds-local-volume` и `d8-sds-node-configurator` все поды находятся в статусе `Running` или `Completed`. Поды должны быть запущены на всех узлах, где планируется использовать ресурсы LVM.

   ```shell
   d8 k -n d8-sds-local-volume get pod -owide -w
   d8 k -n d8-sds-node-configurator get pod -o wide -w
   ```

### Подготовка узлов к созданию хранилищ

Запустите поды `csi-node` на выбранных узлах для корректной работы хранилищ. По умолчанию эти поды запускаются на всех узлах кластера. Проверьте их наличие командой:

```shell
d8 k -n d8-sds-local-volume get pod -owide
```

Размещение подов `csi-node` управляется специальными метками (`nodeSelector`), которые задаются в параметре [spec.settings.dataNodes.nodeSelector](configuration.html#parameters-datanodes-nodeselector) модуля. Подробнее о настройке и выборе узлов для работы модуля см. в разделе [Выбор узлов для работы модуля](./usage.html#выбор-узлов-для-работы-модуля).

### Настройка хранилища на узлах

Для настройки хранилища на узлах создайте группы томов LVM с использованием ресурсов [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup). В данном примере создается Thick-хранилище. Инструкции по созданию thin-хранилища см. в разделе [Создание thin-хранилища](./usage.html#создание-thin-хранилища).

{{< alert level="warning" >}}
Перед созданием ресурса [LVMVolumeGroup](/modules/sds-node-configurator/stable/cr.html#lvmvolumegroup) убедитесь, что на данном узле запущен под `csi-node`. Для этого выполните команду:

```shell
d8 k -n d8-sds-local-volume get pod -owide
```

{{< /alert >}}

Чтобы настроить хранилище на узлах, выполните следующие действия:

1. Получите все доступные ресурсы [BlockDevice](/modules/sds-node-configurator/stable/cr.html#blockdevice) в кластере:

   ```shell
   d8 k get bd
   ```

   Пример вывода:

   ```console
   NAME                                           NODE       CONSUMABLE   SIZE           PATH
   dev-ef4fb06b63d2c05fb6ee83008b55e486aa1161aa   worker-0   false        976762584Ki    /dev/nvme1n1
   dev-0cfc0d07f353598e329d34f3821bed992c1ffbcd   worker-0   false        894006140416   /dev/nvme0n1p6
   dev-7e4df1ddf2a1b05a79f9481cdf56d29891a9f9d0   worker-1   false        976762584Ki    /dev/nvme1n1
   dev-b103062f879a2349a9c5f054e0366594568de68d   worker-1   false        894006140416   /dev/nvme0n1p6
   dev-53d904f18b912187ac82de29af06a34d9ae23199   worker-2   false        976762584Ki    /dev/nvme1n1
   dev-6c5abbd549100834c6b1668c8f89fb97872ee2b1   worker-2   false        894006140416   /dev/nvme0n1p6
   ```

1. Создайте ресурс [LVMVolumeGroup](/modules/sds-node-configurator/stable/cr.html#lvmvolumegroup) для узла `worker-0`:

   ```shell
   d8 k apply -f - <<EOF
   apiVersion: storage.deckhouse.io/v1alpha1
   kind: LVMVolumeGroup
   metadata:
    name: "vg-1-on-worker-0" # Имя может быть любым валидным именем ресурса в Kubernetes. Это имя ресурса LVMVolumeGroup будет использовано при создании LocalStorageClass
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
             - dev-0cfc0d07f353598e329d34f3821bed992c1ffbcd
     actualVGNameOnTheNode: "vg-1" # Имя группы томов LVM, которая будет создана из указанных блочных устройств на узле
   EOF
   ```

1. Дождитесь перехода созданного ресурса [LVMVolumeGroup](/modules/sds-node-configurator/stable/cr.html#lvmvolumegroup) в состояние `Ready`:

   ```shell
   d8 k get lvg vg-1-on-worker-0 -w
   ```

   После перехода ресурса в состояние `Ready` на узле `worker-0` из блочных устройств `/dev/nvme1n1` и `/dev/nvme0n1p6` будет создана группа томов LVM с именем `vg-1`.

1. Создайте ресурс [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) для узла `worker-1`:

   ```shell
   d8 k apply -f - <<EOF
   apiVersion: storage.deckhouse.io/v1alpha1
   kind: LVMVolumeGroup
   metadata:
     name: "vg-1-on-worker-1"
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
             - dev-b103062f879a2349a9c5f054e0366594568de68d
     actualVGNameOnTheNode: "vg-1"
   EOF
   ```

1. Дождитесь перехода созданного ресурса [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) в состояние `Ready`:

   ```shell
   d8 k get lvg vg-1-on-worker-1 -w
   ```

   После перехода ресурса в состояние `Ready` на узле `worker-1` из блочных устройств `/dev/nvme1n1` и `/dev/nvme0n1p6` будет создана группа томов LVM с именем `vg-1`.

1. Создайте ресурс [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) для узла `worker-2`:

   ```shell
   d8 k apply -f - <<EOF
   apiVersion: storage.deckhouse.io/v1alpha1
   kind: LVMVolumeGroup
   metadata:
     name: "vg-1-on-worker-2"
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
             - dev-6c5abbd549100834c6b1668c8f89fb97872ee2b1
     actualVGNameOnTheNode: "vg-1"
   EOF
   ```

1. Дождитесь перехода созданного ресурса [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) в состояние `Ready`:

   ```shell
   d8 k get lvg vg-1-on-worker-2 -w
   ```

   После перехода ресурса в состояние `Ready` на узле `worker-2` из блочных устройств `/dev/nvme1n1` и `/dev/nvme0n1p6` будет создана группа томов LVM с именем `vg-1`.

1. Создайте ресурс [LocalStorageClass](./cr.html#localstorageclass):

   > **Внимание:** создание StorageClass для CSI-драйвера `local.csi.storage.deckhouse.io` пользователем запрещено.

   ```shell
   d8 k apply -f -<<EOF
   apiVersion: storage.deckhouse.io/v1alpha1
   kind: LocalStorageClass
   metadata:
     name: local-storage-class
   spec:
     lvm:
       lvmVolumeGroups:
         - name: vg-1-on-worker-0
         - name: vg-1-on-worker-1
         - name: vg-1-on-worker-2
       type: Thick
     reclaimPolicy: Delete
     volumeBindingMode: WaitForFirstConsumer
   EOF
   ```

1. Дождитесь перехода созданного ресурса [LocalStorageClass](./cr.html#localstorageclass) в состояние `Created`:

   ```shell
   d8 k get lsc local-storage-class -w
   ```

1. Проверьте создание соответствующего StorageClass:

   ```shell
   d8 k get sc local-storage-class
   ```

После появления StorageClass с именем `local-storage-class` настройка модуля `sds-local-volume` завершена. Теперь можно создавать Persistent Volume Claim (PVC), указывая StorageClass с именем `local-storage-class`.

## Быстрый старт с RawFile (без LVM)

Если вы не хотите использовать LVM или у вас нет выделенных блочных устройств, можно использовать RawFile-тома. RawFile использует обычные файлы на файловой системе узла, смонтированные как loop-устройства.

### Создание RawFile-хранилища

1. После включения модулей (как описано выше) создайте ресурс [LocalStorageClass](./cr.html#localstorageclass) с конфигурацией RawFile:

   ```shell
   d8 k apply -f -<<EOF
   apiVersion: storage.deckhouse.io/v1alpha1
   kind: LocalStorageClass
   metadata:
     name: rawfile-storage
   spec:
     rawFile:
       sparse: false
     reclaimPolicy: Delete
     volumeBindingMode: WaitForFirstConsumer
     fsType: ext4
   EOF
   ```

1. Дождитесь перехода ресурса [LocalStorageClass](./cr.html#localstorageclass) в состояние `Created`:

   ```shell
   d8 k get lsc rawfile-storage -w
   ```

1. Проверьте, что создан соответствующий StorageClass:

   ```shell
   d8 k get sc rawfile-storage
   ```

### Создание тестового PVC и Pod

1. Создайте PersistentVolumeClaim:

   ```shell
   d8 k apply -f -<<EOF
   apiVersion: v1
   kind: PersistentVolumeClaim
   metadata:
     name: test-rawfile-pvc
   spec:
     accessModes:
       - ReadWriteOnce
     storageClassName: rawfile-storage
     resources:
       requests:
         storage: 1Gi
   EOF
   ```

1. Создайте Pod, использующий PVC:

   ```shell
   d8 k apply -f -<<EOF
   apiVersion: v1
   kind: Pod
   metadata:
     name: test-rawfile-pod
   spec:
     containers:
     - name: test
       image: busybox
       command: ["sleep", "3600"]
       volumeMounts:
       - name: data
         mountPath: /data
     volumes:
     - name: data
       persistentVolumeClaim:
         claimName: test-rawfile-pvc
   EOF
   ```

1. Проверьте, что Pod запущен и том смонтирован:

   ```shell
   d8 k get pod test-rawfile-pod
   d8 k exec test-rawfile-pod -- df -h /data
   ```

Подробнее о параметрах конфигурации RawFile см. в разделе [Использование RawFile-томов](./usage.html#использование-rawfile-томов).
