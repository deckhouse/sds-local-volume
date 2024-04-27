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

Расположение данных pod-ов определяется специальными лейблами (nodeSelector), которые указываются в поле `spec.settings.dataNodes.nodeSelector` в настройках модуля.

```yaml
apiVersion: deckhouse.io/v1alpha1
kind: ModuleConfig
metadata:
  name: sds-replicated-volume
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

> Обратите внимание, что в поле `nodeSelector` может быть указано любое количество лейблов, но важно, что каждый из указанных лейблов присутствовал на узле, который Вы собираетесь использовать для работы с модулем. Именно при наличии всех указанных лейблов на выбранном узле, произойдет запуск pod-а `sds-local-volume-csi-node`.

Проверить наличие существующих лейблов можно командой: 
```shell
kubectl get mc sds-local-volume -o=jsonpath={.spec.settings.dataNodes.nodeSelector}
```

Вы можете дополнительно проверить селекторы, которые используются модулем в конфиге секрета `d8-sds-local-volume-controller-config` в пространстве имен `d8-sds-local-volume`. 

```shell
kubectl -n d8-sds-local-volume get secret d8-sds-local-volume-controller-config  -o jsonpath='{.data.config}' | base64 --decode
```

Примерный вывод команды:
```yaml
nodeSelector:
  kubernetes.io/os: linux
  my-custom-label-key: my-custom-label-value
```

> В выводе данной команды должны быть указаны все лейблы из настроек модуля `data.nodeSelector`, а также `kubernetes.io/os: linux`.

### Добавление узла в `nodeSelector` модуля.
- Добавьте желаемые лейблы (nodeSelector) в поле `spec.settings.dataNodes.nodeSelector` в настройках модуля.

```yaml
apiVersion: deckhouse.io/v1alpha1
kind: ModuleConfig
metadata:
  annotations:
  name: sds-replicated-volume
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
- Сохраните изменения
- Добавьте лейблы, указанные в `nodeSelector`, на узлы, которые вы желаете отдать под управление модулю, используя шаблоны в `NodeGroup`. 

После добавление лейблов на узлах должны быть запущены pod-ы `sds-local-volume-csi-node`. Проверить их наличие можно командой:
```shell
 kubectl -n d8-sds-local-volume get pod -owide
 ```

### Настройка хранилища на узлах

Необходимо на этих узлах создать группы томов `LVM` с помощью пользовательских ресурсов `LVMVolumeGroup`. В быстром старте будем создавать обычное `Thin` хранилище.

> Пожалуйста, перед созданием `LVMVolumeGroup` убедитесь, что на данном узле запущен pod `sds-local-volume-csi-node`. Это можно сделать командой:
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
    name: "vg-1-on-worker-0" # The name can be any fully qualified resource name in Kubernetes. This LvmVolumeGroup resource name will be used to create ReplicatedStoragePool in the future
  spec:
    type: Local
    blockDeviceNames:  # specify the names of the BlockDevice resources that are located on the target node and whose CONSUMABLE is set to true. Note that the node name is not specified anywhere since it is derived from BlockDevice resources.
      - dev-ef4fb06b63d2c05fb6ee83008b55e486aa1161aa
      - dev-0cfc0d07f353598e329d34f3821bed992c1ffbcd
    thinPools:
      - name: ssd-thin
        size: 50Gi
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
    thinPools:
    - name: ssd-thin
      size: 50Gi
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
    thinPools:
    - name: ssd-thin
      size: 50Gi
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
    isDefault: false
    lvm:
      lvmVolumeGroups:
        - name: vg-1-on-worker-0
          thin:
            poolName: ssd-thin
        - name: vg-1-on-worker-1
          thin:
            poolName: ssd-thin
        - name: vg-1-on-worker-2
          thin:
            poolName: ssd-thin
      type: Thin
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

## Вывод узла из-под управления модуля

Если в процессе работы появилась необходимость вывести узел из-под управления модуля, необходимо убрать лейблы, указанные в `nodeSelector` в настройках `ModuleConfig` `sds-local-volume`. 

> Обратите внимание, что для успешного вывода узла из-под управления модуля, необходимо, чтобы на узле не было создано ресурсов `LVMVolumeGroup`, которые использовались бы в ресурсах `LocalStorageClass`, то есть, которые использовались бы `Storage Class` с `provisioner` `local.csi.storage.deckhouse.io`. 

### Проверка используемых `LVMVolumeGroup` в `LocalStorageClass`
Для проверки таковых ресурсов необходимо выполнить следующие шаги:
 1. Отобразить имеющиеся `LocalStorageClass` ресурсы
```shell
kubectl get lsc
```
2. Проверить у каждого из них список используемых `LVMVolumeGroup` ресурсов
```shell
kubectl get lsc %lsc-name% -oyaml
```

Примерный вид `LocalStorageClass`
```yaml
apiVersion: v1
items:
- apiVersion: storage.deckhouse.io/v1alpha1
  kind: LocalStorageClass
  metadata:
    annotations:
      kubectl.kubernetes.io/last-applied-configuration: |
        {"apiVersion":"storage.deckhouse.io/v1alpha1","kind":"LocalStorageClass","metadata":{"annotations":{},"name":"test-sc"},"spec":{"isDefault":false,"lvm":{"lvmVolumeGroups":[{"name":"test-vg"}],"type":"Thick"},"reclaimPolicy":"Delete","volumeBindingMode":"WaitForFirstConsumer"}}
    creationTimestamp: "2024-04-26T07:40:36Z"
    finalizers:
    - localstorageclass.storage.deckhouse.io
    generation: 2
    name: test-sc
    resourceVersion: "26243988"
    uid: 05e32b0c-0bb1-4754-a305-7646d483175e
  spec:
    isDefault: false
    lvm:
      lvmVolumeGroups:
      - name: test-vg
      type: Thick
    reclaimPolicy: Delete
    volumeBindingMode: WaitForFirstConsumer
  status:
    phase: Created
kind: List
metadata:
  resourceVersion: ""
```
> Обратите внимание на поле spec.lvm.lvmVolumeGroups - именно в нем указаны используемые `LVMVolumeGroup` ресурсы.

3. Отобразите список существующих `LVMVolumeGroup` ресурсов
```shell
kubectl get lvg
```
Примерный вывод `LVMVolumeGroup` ресурсов 
```text
NAME              HEALTH        NODE                         SIZE       ALLOCATED SIZE   VG        AGE
lvg-on-worker-0   Operational   node-worker-0   40956Mi    0                test-vg   15d
lvg-on-worker-1   Operational   node-worker-1   61436Mi    0                test-vg   15d
lvg-on-worker-2   Operational   node-worker-2   122876Mi   0                test-vg   15d
lvg-on-worker-3   Operational   node-worker-3   307196Mi   0                test-vg   15d
lvg-on-worker-4   Operational   node-worker-4   307196Mi   0                test-vg   15d
lvg-on-worker-5   Operational   node-worker-5   204796Mi   0                test-vg   15d
```

4. Проверьте, что на узле, который вы собираетесь вывести из-под управления модуля, не присутствует какой-либо `LVMVolumeGroup` ресурс, используемый в `LocalStorageClass` ресурсах.

> Во избежание непредвиденной потери контроля за уже созданными с помощью модуля томами пользователю необходимо вручную удалить зависимые ресурсы, совершив необходимые операции над томом.

### Вывод узла из-под управления модуля 

Проверить наличие существующих лейблов в `nodeSelector` можно командой:
```shell
kubectl get mc sds-local-volume -o=jsonpath={.spec.settings.dataNodes.nodeSelector}
```

Примерный вывод команды:
```yaml
nodeSelector:
  my-custom-label-key: my-custom-label-value
```

- Снимите указанные в `nodeSelector` лейблы с желаемых узлов
```shell
kubectl label node %node-name% %label-from-selector%-
```
> Обратите внимание, что для снятия лейбла необходимо после его ключа вместо значения сразу же поставить знак минуса.

В результате pod `sds-local-volume-csi-node` должен быть удален с желаемого узла. Для проверки состояния можно выполнить команду
```shell
kubectl -n d8-sds-local-volume get po -owide
```

> Если pod `sds-local-volume-csi-node` после удаления лейблов `nodeSelector` все же остался на узле, пожалуйста, убедитесь, указанные в конфиге `d8-sds-local-volume-controller-config` в `nodeSelector` лейблы действительно успешно снялись с выбранного узла. 
> Проверить это можно командой: 
> ```shell
> kubectl get node %node-name% --show-labels=true
> ```
> Если лейблы из `nodeSelector` не присутствуют на узле, то убедитесь, что данному узлу не принадлежат `LVMVolumeGroup` ресурсы, использующиеся `LocalStorageClass` ресурсами. Для этого необходимо выполнить следующую [проверку](#проверка-используемых-lvmvolumegroup-в-localstorageclass).

> Обратите внимание, что на ресурсах `LVMVolumeGroup` и `LocalStorageClass`, из-за которых не удается вывести узел из-под управления модуля будут отображен лейбл `storage.deckhouse.io/sds-local-volume-candidate-for-eviction`.
> 
> На самом узле будет присутствовать лейбл `storage.deckhouse.io/sds-local-volume-need-manual-eviction`.



## Системные требования и рекомендации

### Требования

- Использование стоковых ядер, поставляемых вместе с [поддерживаемыми дистрибутивами](https://deckhouse.ru/documentation/v1/supported_versions.html#linux);
- Не использовать другой SDS (Software defined storage) для предоставления дисков нашему SDS
