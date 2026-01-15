---
title: "Модуль sds-local-volume: использование"
description: "Использование модуля sds-local-volume: очистка томов, настройка узлов, миграция данных и создание снимков."
weight: 5
---

## Очистка томов при удалении

{{< alert level="warning" >}}
Возможность очистки томов доступна только в коммерческих редакциях Deckhouse.
{{< /alert >}}

При удалении файлов операционная система не удаляет содержимое физически, а лишь помечает соответствующие блоки как «свободные». Если новый том получает физические блоки, ранее использовавшиеся другим томом, в них могут остаться данные предыдущего пользователя.

### Пример сценария утечки данных

1. Пользователь №1 разместил файлы в томе, запрошенном из StorageClass 1 на узле 1 (в режиме «Block» или «Filesystem»).
1. Пользователь №1 удалил файлы и том.
1. Физические блоки, которые занимал том, становятся «свободными», но не затертыми.
1. Пользователь №2 запросил новый том из StorageClass 1 на узле 1 в режиме «Block».
1. Существует риск, что часть или все блоки, ранее занимаемые пользователем №1, будут снова выделены пользователю №2.
1. В этом случае пользователь №2 может восстановить данные пользователя №1.

### Thick-тома

Для предотвращения утечек данных через thick-тома предусмотрен параметр `volumeCleanup`, который позволяет выбрать метод очистки тома перед удалением PersistentVolume (PV).

Возможные значения:

- параметр не задан — дополнительные действия при удалении тома не выполняются. Данные могут остаться доступными следующему пользователю;

- `RandomFillSinglePass` — том перезаписывается случайными данными один раз перед удалением. **Не рекомендуется** для твердотельных накопителей, так как уменьшает ресурс накопителя;

- `RandomFillThreePass` — том перезаписывается случайными данными три раза перед удалением. **Не рекомендуется** для твердотельных накопителей, так как уменьшает ресурс накопителя;

- `Discard` — все блоки тома отмечаются как свободные с использованием системного вызова `discard` перед удалением. Используйте эту опцию только для твердотельных накопителей.

Большинство современных твердотельных накопителей гарантирует, что помеченный `discard` блок при чтении не вернет предыдущие данные. Это делает опцию `Discard` самым эффективным способом предотвращения утечек при использовании твердотельных накопителей.

Однако очистка ячейки — относительно долгая операция, поэтому выполняется устройством в фоне. К тому же многие диски не могут очищать индивидуальные ячейки, а только группы — страницы. Из-за этого не все накопители гарантируют немедленную недоступность освобожденных данных. К тому же не все накопители, гарантирующие это, держат обещание.

Не используйте устройство, если оно не гарантирует Deterministic TRIM (DRAT), Deterministic Read Zero after TRIM (RZAT) и не является проверенным.

### Thin-тома

При освобождении блока thin-тома через `discard` гостевой операционной системы эта команда пересылается на устройство. В случае использования жесткого диска или отсутствия поддержки `discard` со стороны твердотельного накопителя данные могут остаться на thin pool до нового использования такого блока.

Пользователям предоставляется доступ только к thin-томам, а не к самому thin pool. Они могут получить только том из пула. Для thin-томов производится зануление блока thin pool при новом использовании, что предотвращает утечки между клиентами. Это гарантируется настройкой `thin_pool_zero=1` в LVM.

## Миграция данных между PVC

Используйте следующий скрипт для переноса данных из одного PVC в другой:

1. Скопируйте скрипт в файл `migrate.sh` на любом master-узле.

1. Используйте скрипт с параметрами: `migrate.sh NAMESPACE SOURCE_PVC_NAME DESTINATION_PVC_NAME`

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

## Создание снимков томов

{{< alert level="warning" >}}
Возможность работы со снимками томов доступна только в коммерческих редакциях Deckhouse Kubernetes Platform и только при использовании LVM-thin томов.

Для работы со снимками томов требуется подключенный модуль [snapshot-controller](/modules/snapshot-controller/).
{{< /alert >}}

Подробную информацию о снимках см. в [документации Kubernetes](https://kubernetes.io/docs/concepts/storage/volume-snapshots/).

Для того чтобы создать снимок тома, выполните следующие действия:

1. Включите модуль `snapshot-controller`:

   ```shell
   d8 s module enable snapshot-controller
   ```

1. Для того чтобы создать снимок тома, выполните следующую команду с необходимыми параметрами:

   ```shell
   d8 k apply -f -<<EOF
   apiVersion: snapshot.storage.k8s.io/v1
   kind: VolumeSnapshot
   metadata:
     name: my-snapshot
     namespace: <namespace-name> # Имя пространства имен, где находится PVC
   spec:
     volumeSnapshotClassName: sds-local-volume-snapshot-class
     source:
       persistentVolumeClaimName: <pvc-name> # Имя PVC, для которого создается снимок
   EOF
   ```

   > **Внимание:** класс `sds-local-volume-snapshot-class` создается автоматически. Параметр `deletionPolicy` установлен в `Delete`, поэтому `VolumeSnapshotContent` удаляется при удалении связанного `VolumeSnapshot`.

1. Проверьте статус снимка:

   ```shell
   d8 k get volumesnapshot
   ```

   Команда выводит список всех снимков и их текущий статус.

## Назначение StorageClass по умолчанию

Добавьте аннотацию `storageclass.kubernetes.io/is-default-class: "true"` в соответствующий ресурс StorageClass:

```shell
d8 k annotate storageclasses.storage.k8s.io <storageClassName> storageclass.kubernetes.io/is-default-class=true
```

## Выбор узлов для работы модуля

Модуль использует узлы, которые имеют метки, указанные в поле `nodeSelector` в настройках модуля. Сделать это можно следующим образом:

1. Для отображения настроек модуля выполните команду:

   ```shell
   d8 k edit mc sds-local-volume
   ```

   Пример вывода:

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

1. Выполните команду для просмотра меток в поле `nodeSelector`:

   ```shell
   d8 k get mc sds-local-volume -o=jsonpath={.spec.settings.dataNodes.nodeSelector}
   ```

   Пример вывода:

   ```console
   nodeSelector:
     my-custom-label-key: my-custom-label-value
   ```

1. Модуль выбирает узлы, которые имеют все указанные метки. Измените поле `nodeSelector`, чтобы изменить список узлов.

   > **Внимание:** в поле `nodeSelector` можно указать любое количество меток. Все указанные метки должны присутствовать на узле. Модуль запускает под `csi-node` только на узлах, которые имеют все указанные метки.

1. После добавления меток проверьте, что поды `csi-node` запущены на узлах:

   ```shell
   d8 k -n d8-sds-local-volume get pod -owide
   ```

## Вывод узла из-под управления модуля

Чтобы вывести узел из-под управления модуля, снимите метки, указанные в поле `nodeSelector` в настройках модуля. Для этого:

1. Выполните команду для просмотра меток в `nodeSelector`:

   ```shell
   d8 k get mc sds-local-volume -o=jsonpath={.spec.settings.dataNodes.nodeSelector}
   ```

   Пример вывода:

   ```console
   nodeSelector:
     my-custom-label-key: my-custom-label-value
   ```

1. Снимите указанные метки с узла:

   ```shell
   d8 k label node %node-name% %label-from-selector%-
   ```

   > **Внимание:** для снятия метки добавьте знак минуса после ключа метки вместо значения.

1. Проверьте, что pod `csi-node` удален с узла:

   ```shell
   d8 k -n d8-sds-local-volume get po -owide
   ```

Если под `csi-node` остался на узле после снятия меток:

1. Проверьте, что метки действительно сняты с узла:

   ```shell
   d8 k get node <node-name> --show-labels
   ```

1. Убедитесь, что на узле нет ресурсов [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup), которые используются в ресурсах [LocalStorageClass](cr.html#localstorageclass). Подробнее см. [Проверка зависимых ресурсов LVMVolumeGroup на узле](#проверка-зависимых-ресурсов-lvmvolumegroup-на-узле).

{{< alert level="warning" >}}
Обратите внимание, что на ресурсах [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) и [LocalStorageClass](cr.html#localstorageclass), из-за которых не удается вывести узел из-под управления модуля, будет отображена метка `storage.deckhouse.io/sds-local-volume-candidate-for-eviction`.

На самом узле будет присутствовать метка `storage.deckhouse.io/sds-local-volume-need-manual-eviction`.
{{< /alert >}}

## Создание thin-хранилища

1. Получите список доступных ресурсов [BlockDevice](/modules/sds-node-configurator/cr.html#blockdevice) в кластере:

   ```shell
   d8 k get bd
   ```

   Пример вывода:

   ```console
   NAME                                           NODE       CONSUMABLE   SIZE           PATH
   dev-ef4fb06b63d2c05fb6ee83008b55e486aa1161aa   worker-0   false        100Gi          /dev/nvme1n1
   dev-7e4df1ddf2a1b05a79f9481cdf56d29891a9f9d0   worker-1   false        100Gi          /dev/nvme1n1
   dev-53d904f18b912187ac82de29af06a34d9ae23199   worker-2   false        100Gi          /dev/nvme1n1
   ```

1. Создайте ресурс [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) для узла `worker-0`:

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

1. Дождитесь перехода ресурса [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) в состояние `Ready`:

   ```shell
   d8 k get lvg vg-2-on-worker-0 -w
   ```

   После перехода в состояние `Ready` на узле `worker-0` из блочного устройства `/dev/nvme1n1` создана группа томов LVM с именем `vg-2` и thin pool с именем `thindata`.

1. Создайте ресурс [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) для узла `worker-1`:

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

1. Дождитесь перехода ресурса [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) в состояние `Ready`:

   ```shell
   d8 k get lvg vg-2-on-worker-1 -w
   ```

   После перехода в состояние `Ready` на узле `worker-1` из блочного устройства `/dev/nvme1n1` создана группа томов LVM с именем `vg-2` и thin pool с именем `thindata`.

1. Создайте ресурс [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) для узла `worker-2`:

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

1. Дождитесь перехода ресурса [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) в состояние `Ready`:

   ```shell
   d8 k get lvg vg-2-on-worker-2 -w
   ```

   После перехода в состояние `Ready` на узле `worker-2` из блочного устройства `/dev/nvme1n1` создана группа томов LVM с именем `vg-2` и thin pool с именем `thindata`.

1. Создайте ресурс [LocalStorageClass](./cr.html#localstorageclass):

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

1. Дождитесь перехода ресурса [LocalStorageClass](cr.html#localstorageclass) в состояние `Created`:

   ```shell
   d8 k get lsc local-storage-class -w
   ```

1. Проверьте, что создан соответствующий StorageClass:

   ```shell
   d8 k get sc local-storage-class
   ```

Теперь можно создавать PVC, указывая StorageClass с именем `local-storage-class`.

## Создание хранилища с LVMVolumeGroupSet и селектором

При управлении большим количеством узлов можно использовать [LVMVolumeGroupSet](/modules/sds-node-configurator/cr.html#lvmvolumegroupset) для автоматического создания ресурсов [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) на узлах, соответствующих селектору. Затем используйте `lvmVolumeGroupSelector` в [LocalStorageClass](cr.html#localstorageclass) для автоматического выбора всех LVG, созданных набором.

Этот подход полезен, когда:
- У вас много узлов с похожей конфигурацией хранилища
- Узлы добавляются/удаляются динамически
- Вы хотите избежать ручного указания имени каждого LVMVolumeGroup

### Пример: создание Thick-хранилища с LVMVolumeGroupSet

1. Создайте [LVMVolumeGroupSet](/modules/sds-node-configurator/cr.html#lvmvolumegroupset), который создаёт LVMVolumeGroup на всех рабочих узлах:

   ```shell
   d8 k apply -f - <<EOF
   apiVersion: storage.deckhouse.io/v1alpha1
   kind: LVMVolumeGroupSet
   metadata:
     name: my-vg-set
   spec:
     nodeSelector:
       matchLabels:
         node-role.kubernetes.io/worker: ""
     strategy: PerNode
     lvmVolumeGroupTemplate:
       metadata:
         labels:
           storage.deckhouse.io/lvg-group: my-storage
       type: Local
       actualVGNameOnTheNode: data-vg
       blockDeviceSelector:
         matchLabels:
           storage.deckhouse.io/enabled: "true"
   EOF
   ```

1. Дождитесь создания ресурсов [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup):

   ```shell
   d8 k get lvg -l storage.deckhouse.io/lvg-group=my-storage
   ```

1. Создайте [LocalStorageClass](cr.html#localstorageclass), используя `lvmVolumeGroupSelector`:

   ```shell
   d8 k apply -f - <<EOF
   apiVersion: storage.deckhouse.io/v1alpha1
   kind: LocalStorageClass
   metadata:
     name: my-thick-storage
   spec:
     reclaimPolicy: Delete
     volumeBindingMode: WaitForFirstConsumer
     lvm:
       type: Thick
       lvmVolumeGroupSelector:
         matchLabels:
           storage.deckhouse.io/lvg-group: my-storage
   EOF
   ```

   LocalStorageClass автоматически включит все LVMVolumeGroup с меткой `storage.deckhouse.io/lvg-group: my-storage`. При добавлении новых узлов и создании LVMVolumeGroupSet новых LVG с этой меткой StorageClass будет автоматически обновлён для их включения.

### Пример: создание Thin-хранилища с LVMVolumeGroupSet

Для хранилища типа Thin с селектором необходимо указать `thinPoolName` — имя thin pool, который должен быть у всех выбранных LVMVolumeGroup:

1. Создайте [LVMVolumeGroupSet](/modules/sds-node-configurator/cr.html#lvmvolumegroupset) с thin pool:

   ```shell
   d8 k apply -f - <<EOF
   apiVersion: storage.deckhouse.io/v1alpha1
   kind: LVMVolumeGroupSet
   metadata:
     name: my-thin-vg-set
   spec:
     nodeSelector:
       matchLabels:
         node-role.kubernetes.io/worker: ""
     strategy: PerNode
     lvmVolumeGroupTemplate:
       metadata:
         labels:
           storage.deckhouse.io/lvg-group: my-thin-storage
       type: Local
       actualVGNameOnTheNode: thin-vg
       blockDeviceSelector:
         matchLabels:
           storage.deckhouse.io/enabled: "true"
       thinPools:
         - name: thin-pool
           size: 90%
   EOF
   ```

1. Создайте [LocalStorageClass](cr.html#localstorageclass) для Thin-хранилища:

   ```shell
   d8 k apply -f - <<EOF
   apiVersion: storage.deckhouse.io/v1alpha1
   kind: LocalStorageClass
   metadata:
     name: my-thin-storage
   spec:
     reclaimPolicy: Delete
     volumeBindingMode: WaitForFirstConsumer
     lvm:
       type: Thin
       thinPoolName: thin-pool
       lvmVolumeGroupSelector:
         matchLabels:
           storage.deckhouse.io/lvg-group: my-thin-storage
   EOF
   ```

### Комбинирование селектора с явным списком LVMVolumeGroups

Можно использовать `lvmVolumeGroupSelector` и `lvmVolumeGroups` вместе. Результирующий набор LVMVolumeGroup будет объединением обоих:

```yaml
apiVersion: storage.deckhouse.io/v1alpha1
kind: LocalStorageClass
metadata:
  name: combined-storage
spec:
  reclaimPolicy: Delete
  volumeBindingMode: WaitForFirstConsumer
  lvm:
    type: Thick
    lvmVolumeGroups:
      - name: special-vg-on-node-1
    lvmVolumeGroupSelector:
      matchLabels:
        storage.deckhouse.io/lvg-group: standard-storage
```

## Проверка зависимых ресурсов LVMVolumeGroup на узле

Выполните следующие шаги:

1. Отобразите ресурсы [LocalStorageClass](cr.html#localstorageclass):

   ```shell
   d8 k get lsc
   ```

1. Проверьте список используемых ресурсов [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup) в каждом [LocalStorageClass](cr.html#localstorageclass).

   Отобразите содержимое всех ресурсов [LocalStorageClass](cr.html#localstorageclass):

   ```shell
   d8 k get lsc -oyaml
   ```

   Или отобразите содержимое конкретного ресурса:

   ```shell
   d8 k get lsc <lsc-name> -oyaml
   ```

   Пример ресурса [LocalStorageClass](cr.html#localstorageclass):

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

   В поле `spec.lvm.lvmVolumeGroups` указаны используемые ресурсы [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup).

1. Отобразите список ресурсов [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup):

   ```shell
   d8 k get lvg
   ```

   Пример вывода:

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

1. Проверьте, что на узле нет ресурсов [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup), которые используются в ресурсах [LocalStorageClass](cr.html#localstorageclass).

   Перед выводом узла из-под управления модуля удалите зависимые ресурсы вручную, чтобы не потерять контроль над созданными томами.
