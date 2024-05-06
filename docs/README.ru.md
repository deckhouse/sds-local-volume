---
title: "Модуль sds-local-volume"
description: "Модуль sds-local-volume: общие концепции и положения."
moduleStatus: experimental
---

Модуль управляет локальным блочным хранилищем на базе `LVM`. Модуль позволяет создавать `StorageClass` в `Kubernetes` через создание [пользовательских ресурсов Kubernetes](./cr.html) `LocalStorageClass` (пример создания ниже).
Для создания `Storage Class` потребуются настроенные на узлах кластера `LVMVolumeGroup`. Настройка `LVM` осуществляется модулем [sds-node-configurator](../../sds-node-configurator/).
> **Внимание!** Перед включением модуля `sds-local-volume` необходимо включить модуль `sds-node-configurator`.
>
После включения модуля `sds-local-volume` необходимо создать StorageClass'ы.

> **Внимание!** Создание `StorageClass` для CSI-драйвера local.csi.storage.deckhouse.io пользователем запрещено.

Поддерживаются два режима — LVM и LVMThin.
Каждый из них имеет свои достоинства и недостатки, подробнее о различиях читайте в [FAQ](./faq.html#когда-следует-использовать-lvm-а-когда-lvmthin).

## Быстрый старт

Все команды следует выполнять на машине, имеющей доступ к API Kubernetes с правами администратора.

### Включение модулей

- Включить модуль sds-node-configurator

```yaml
kubectl apply -f - <<EOF
apiVersion: deckhouse.io/v1alpha1
kind: ModuleConfig
metadata:
  name: sds-node-configurator
spec:
  enabled: true
  version: 1
EOF
```

- Дождаться, когда модуль перейдет в состояние `Ready`. На этом этапе НЕ нужно проверять поды в namespace `d8-sds-node-configurator`.

```shell
kubectl get mc sds-node-configurator -w
 ```

- Включить модуль `sds-local-volume`. Возможные настройки модуля рекомендуем посмотреть в [конфигурации](./configuration.html). В примере ниже модуль запускается с настройками по умолчанию. Это приведет к тому, что на всех узлах кластера будут запущены служебные поды компонентов `sds-local-volume`.

```yaml
kubectl apply -f - <<EOF
apiVersion: deckhouse.io/v1alpha1
kind: ModuleConfig
metadata:
  name: sds-local-volume
spec:
  enabled: true
  version: 1
EOF
```

- Дождаться, когда модуль перейдет в состояние `Ready`.

  ```shell
  kubectl get mc sds-local-volume -w
  ```

- Проверить, что в namespace `d8-sds-local-volume` и `d8-sds-node-configurator` все поды в состоянии `Running` или `Completed` и запущены на всех узлах, где планируется использовать ресурсы `LVM`.

```shell
kubectl -n d8-sds-local-volume get pod -owide -w
kubectl -n d8-sds-node-configurator get pod -o wide -w
```

### Подготовка узлов к созданию хранилищ на них
Для создания хранилищ на узлах необходимо, чтобы на выбранных узлах были запущены pod-ы `sds-local-volume-csi-node`. 

По умолчанию pod-ы выедут на всех узлах кластера, проверить их наличие можно командой:

```shell
 kubectl -n d8-sds-local-volume get pod -owide
 ```

> Расположение данных pod-ов определяется специальными метками (nodeSelector), которые указываются в поле `spec.settings.dataNodes.nodeSelector` в настройках модуля. Для получения более подробной информации о настройке, пожалуйста, перейдите по [ссылке](FAQ.ru.md#я-не-хочу-чтобы-модуль-использовался-на-всех-узлах-кластера-как-мне-выбрать-желаемые-узлы-)

### Настройка хранилища на узлах
Необходимо на этих узлах создать группы томов `LVM` с помощью пользовательских ресурсов `LVMVolumeGroup`. В быстром старте будем создавать обычное `Thick` хранилище.

> Пожалуйста, перед созданием `LVMVolumeGroup` убедитесь, что на данном узле запущен pod `sds-local-volume-csi-node`. Это можно сделать командой:
> 
> ```shell
> kubectl -n d8-sds-local-volume get pod -owide
> ```

Приступим к настройке хранилища:

- Получить все ресурсы [BlockDevice](../../sds-node-configurator/stable/cr.html#blockdevice), которые доступны в вашем кластере:

  ```shell
  kubectl get bd
  
  NAME                                           NODE       CONSUMABLE   SIZE           PATH
  dev-ef4fb06b63d2c05fb6ee83008b55e486aa1161aa   worker-0   false        976762584Ki    /dev/nvme1n1
  dev-0cfc0d07f353598e329d34f3821bed992c1ffbcd   worker-0   false        894006140416   /dev/nvme0n1p6
  dev-7e4df1ddf2a1b05a79f9481cdf56d29891a9f9d0   worker-1   false        976762584Ki    /dev/nvme1n1
  dev-b103062f879a2349a9c5f054e0366594568de68d   worker-1   false        894006140416   /dev/nvme0n1p6
  dev-53d904f18b912187ac82de29af06a34d9ae23199   worker-2   false        976762584Ki    /dev/nvme1n1
  dev-6c5abbd549100834c6b1668c8f89fb97872ee2b1   worker-2   false        894006140416   /dev/nvme0n1p6
  ```

- Создать ресурс [LVMVolumeGroup](../../sds-node-configurator/stable/cr.html#lvmvolumegroup) для узла `worker-0`:

```yaml
kubectl apply -f - <<EOF
apiVersion: storage.deckhouse.io/v1alpha1
kind: LvmVolumeGroup
metadata:
  name: "vg-1-on-worker-0" # The name can be any fully qualified resource name in Kubernetes. This LvmVolumeGroup resource name will be used to create LocalStorageClass in the future
spec:
  type: Local
  blockDeviceNames:  # specify the names of the BlockDevice resources that are located on the target node and whose CONSUMABLE is set to true. Note that the node name is not specified anywhere since it is derived from BlockDevice resources.
    - dev-ef4fb06b63d2c05fb6ee83008b55e486aa1161aa
    - dev-0cfc0d07f353598e329d34f3821bed992c1ffbcd
  actualVGNameOnTheNode: "vg-1" # the name of the LVM VG to be created from the above block devices on the node 
  EOF
```

- Дождаться, когда созданный ресурс `LVMVolumeGroup` перейдет в состояние `Operational`:

```shell
kubectl get lvg vg-1-on-worker-0 -w
```

- Если ресурс перешел в состояние `Operational`, то это значит, что на узле `worker-0` из блочных устройств `/dev/nvme1n1` и `/dev/nvme0n1p6` была создана LVM VG с именем `vg-1`.

- Далее создать ресурс [LVMVolumeGroup](../../sds-node-configurator/stable/cr.html#lvmvolumegroup) для узла `worker-1`:

  ```yaml
  kubectl apply -f - <<EOF
  apiVersion: storage.deckhouse.io/v1alpha1
  kind: LvmVolumeGroup
  metadata:
    name: "vg-1-on-worker-1"
  spec:
    type: Local
    blockDeviceNames:
    - dev-7e4df1ddf2a1b05a79f9481cdf56d29891a9f9d0
    - dev-b103062f879a2349a9c5f054e0366594568de68d
    actualVGNameOnTheNode: "vg-1"
  EOF
  ```

- Дождаться, когда созданный ресурс `LVMVolumeGroup` перейдет в состояние `Operational`:

```shell
kubectl get lvg vg-1-on-worker-1 -w
```

- Если ресурс перешел в состояние `Operational`, то это значит, что на узле `worker-1` из блочного устройства `/dev/nvme1n1` и `/dev/nvme0n1p6` была создана LVM VG с именем `vg-1`.

- Далее создать ресурс [LVMVolumeGroup](../../sds-node-configurator/stable/cr.html#lvmvolumegroup) для узла `worker-2`:

```yaml
kubectl apply -f - <<EOF
apiVersion: storage.deckhouse.io/v1alpha1
kind: LvmVolumeGroup
metadata:
  name: "vg-1-on-worker-2"
spec:
  type: Local
  blockDeviceNames:
  - dev-53d904f18b912187ac82de29af06a34d9ae23199
  - dev-6c5abbd549100834c6b1668c8f89fb97872ee2b1
  actualVGNameOnTheNode: "vg-1"
EOF
```

- Дождаться, когда созданный ресурс `LVMVolumeGroup` перейдет в состояние `Operational`:

```shell
kubectl get lvg vg-1-on-worker-2 -w
```

- Если ресурс перешел в состояние `Operational`, то это значит, что на узле `worker-2` из блочного устройства `/dev/nvme1n1` и `/dev/nvme0n1p6` была создана LVM VG с именем `vg-1`.

- Создать ресурс [LocalStorageClass](./cr.html#localstorageclass):

```yaml
kubectl apply -f -<<EOF
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

- Дождаться, когда созданный ресурс `LocalStorageClass` перейдет в состояние `Created`:

```shell
kubectl get lsc local-storage-class -w
```

- Проверить, что соответствующий `StorageClass` создался:

```shell
kubectl get sc local-storage-class
```

- Если `StorageClass` с именем `local-storage-class` появился, значит настройка модуля `sds-local-volume` завершена. Теперь пользователи могут создавать PV, указывая `StorageClass` с именем `local-storage-class`. При указанных выше настройках будет создаваться том с 3мя репликами на разных узлах.

## Системные требования и рекомендации

### Требования

- Использование стоковых ядер, поставляемых вместе с [поддерживаемыми дистрибутивами](https://deckhouse.ru/documentation/v1/supported_versions.html#linux);
- Не использовать другой SDS (Software defined storage) для предоставления дисков нашему SDS
