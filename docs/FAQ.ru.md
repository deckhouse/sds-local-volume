---
title: "Модуль sds-local-volume: FAQ"
description: "Модуль sds-local-volume: FAQ"
weight: 6
---

## Когда следует использовать LVM, а когда LVM-thin?

Используйте LVM (Thick), если нужна максимальная производительность, сравнимая с производительностью накопителя. LVM (Thick) проще в настройке.

Используйте LVM-thin, если нужно использовать overprovisioning. Производительность LVM-thin ниже, чем у LVM.

{{< alert level="warning" >}}
Используйте overprovisioning в LVM-thin с осторожностью. Контролируйте наличие свободного места в пуле. В системе мониторинга кластера есть отдельные события при достижении 20%, 10%, 5% и 1% свободного места в пуле.

При отсутствии свободного места в пуле возможна деградация работы модуля и потеря данных.
{{< /alert >}}

## Почему не удается создать PVC на выбранном узле?

Проверьте, что на выбранном узле работает под `csi-node`:

```shell
d8 k -n d8-sds-local-volume get po -owide
```

Если под отсутствует, убедитесь, что на узле присутствуют все метки, указанные в поле `nodeSelector` в настройках модуля. Подробнее см. [Почему служебные поды компонентов sds-local-volume не создаются на нужном узле](#почему-служебные-поды-компонентов-sds-local-volume-не-создаются-на-нужном-узле).



## Почему под csi-node остался на узле после снятия меток?

Вероятно, на узле есть ресурсы [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup), которые используются в ресурсах [LocalStorageClass](cr.html#localstorageclass).

Удалите зависимые ресурсы вручную, чтобы не потерять контроль над созданными томами. Инструкции по проверке зависимых ресурсов см. в разделе [Проверка зависимых ресурсов LVMVolumeGroup на узле](./usage.html#проверка-зависимых-ресурсов-lvmvolumegroup-на-узле).

## Почему служебные поды компонентов sds-local-volume не создаются на нужном узле?

Вероятно, проблема связана с метками на узле. Модуль использует узлы, которые имеют метки, указанные в поле `nodeSelector` в настройках модуля.

1. Выполните команду для просмотра меток в `nodeSelector`:

   ```shell
   d8 k get mc sds-local-volume -o=jsonpath={.spec.settings.dataNodes.nodeSelector}
   ```

   Пример вывода:

   ```console
   nodeSelector:
     my-custom-label-key: my-custom-label-value
   ```

1. Проверьте селекторы, которые использует модуль в секрете `d8-sds-local-volume-controller-config`:

   ```shell
   d8 k -n d8-sds-local-volume get secret d8-sds-local-volume-controller-config -o jsonpath='{.data.config}' | base64 --decode
   ```

   Пример вывода:

   ```console
   nodeSelector:
     kubernetes.io/os: linux
     my-custom-label-key: my-custom-label-value
   ```

   В выводе должны быть указаны все метки из настроек модуля `data.nodeSelector`, а также `kubernetes.io/os: linux`.

1. Проверьте метки на узле:

   ```shell
   d8 k get node <node-name> --show-labels
   ```

1. Добавьте недостающие метки на узел:

   ```shell
   d8 k label node <node-name> my-custom-label-key=my-custom-label-value
   ```

1. Если метки присутствуют, проверьте наличие метки `storage.deckhouse.io/sds-local-volume-node=` на узле. Если метка отсутствует, проверьте состояние контроллера:

   ```shell
   d8 k -n d8-sds-local-volume get po -l app=sds-local-volume-controller
   d8 k -n d8-sds-local-volume logs -l app=sds-local-volume-controller
   ```

## Как освободить место тома, созданного с reclaimPolicy: Retain?

При `reclaimPolicy: Retain` удаление PersistentVolumeClaim оставляет и PersistentVolume, и данные: освобождение хранилища — отдельное осознанное действие администратора. Удаление PersistentVolume таким действием не является: оно удаляет только объект Kubernetes, а логический том остаётся размещённым в группе томов и описывается ресурсом [LVMLogicalVolume](/modules/sds-node-configurator/cr.html#lvmlogicalvolume) с тем же именем, что у PersistentVolume.

Чтобы освободить место, удалите этот ресурс:

```shell
d8 k delete lvmlogicalvolume <имя-persistent-volume>
```

Модуль снимет свой finalizer, `sds-node-configurator` выполнит `lvremove`, и место вернётся в группу томов. Действие необратимо: данные будут уничтожены.

Данные затираются перед удалением логического тома ровно так, как было указано в `LocalStorageClass.spec.lvm.volumeCleanup` на момент создания тома: значение зафиксировано в поле `spec.volumeCleanup` самого ресурса. Более позднее изменение LocalStorageClass здесь не подхватывается — PersistentVolume уже удалён, и разрешить через него класс хранения не по чему. Проверьте и при необходимости задайте значение перед удалением:

```shell
d8 k get llv <имя-persistent-volume> -o jsonpath='{.spec.volumeCleanup}'
d8 k patch llv <имя-persistent-volume> --type=merge -p '{"spec":{"volumeCleanup":"RandomFillSinglePass"}}'
```

Чтобы найти тома, у которых PersistentVolume уже удалён, и занимаемое ими место:

```shell
d8 k get lvmlogicalvolume -o custom-columns=NAME:.metadata.name,VG:.spec.lvmVolumeGroupName,TYPE:.spec.type,SIZE:.status.actualSize,PHASE:.status.phase
d8 k get pv
```

Размер читается по-разному в зависимости от типа: для `Thick` это место, занятое в группе томов, а для `Thin` — виртуальный размер, который ничего не говорит о фактическом потреблении thin-пула. Удаление thin-томов ради числа из этой колонки освободит заметно меньше, чем обещает.

Та же информация доступна в системе мониторинга: метрики `sds_local_volume_orphaned_lvm_logical_volume_count` и `sds_local_volume_orphaned_lvm_logical_volume_allocated_bytes`, дашборд Grafana «SDS Local Volume» и алерт `D8SdsLocalVolumeOrphanedLVMLogicalVolumes`.

Если том должен пережить свой PersistentVolume намеренно, отметьте это вместо того, чтобы глушить алерт через silence (заглушение уведомлений):

```shell
d8 k annotate lvmlogicalvolume <имя> storage.deckhouse.io/retain-acknowledged=true
```

Отмеченный том попадает в состояние `state="retained"`, которое не читает ни один алерт, так что алерт снова начинает означать «что-то утекает и на это никто не посмотрел». Аннотация ничего не защищает: запрошенное удаление тома по-прежнему разблокируется штатно.

Если удаление не завершается, значит модуль удерживает свой finalizer, и причину он публикует по каждому тому в лейбле `reason` метрики `sds_local_volume_orphaned_lvm_logical_volume_allocated_bytes{state="blocked"}` — на дашборде это колонка «Reason» в таблице «Which volumes are leaking». Те же причины есть в логе контроллера:

```shell
d8 k -n d8-sds-local-volume logs deploy/controller -c controller | grep -E "refusing to unblock|keeping the finalizer"
```

Причины:

- `snapshots_present` — с логического тома сняты снапшоты (snapshot — снимок состояния тома), сначала удалите их командой `d8 k delete lvmlogicalvolumesnapshot <имя>`: удаление снапшота модуль разблокирует по тому же правилу, что и удаление тома, поэтому команда не зависает;
- `agent_finalizer_absent` — ни один агент `sds-node-configurator` не взял ресурс под управление, поэтому его удаление оставило бы логический том на узле без ссылающегося на него ресурса. Проверьте узел и его [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup);
- `persistent_volume_exists` — на том всё ещё ссылается PersistentVolume — по имени или по CSI volume handle (идентификатор тома, по которому драйвер его находит), — то есть том может быть занят. Поле `driver` в логе показывает, принадлежит ли этот PersistentVolume данному модулю или лишь совпадает по имени;
- `successor_in_place` — имя теперь занято другим объектом, не тем, который рассматривал модуль: `external-provisioner` при повторной попытке создания переиспользует volume ID (идентификатор тома). Делать ничего не нужно — объект, который сейчас занимает имя, будет рассмотрен на следующем проходе сам по себе, и вместе с ним исчезнет эта причина;
- `api_error` или `removal_failed` — модуль не смог подтвердить возможность освобождения либо не смог записать снятие finalizer. В отличие от причин выше это неисправность самого модуля, а не свойство тома; для второй причины срабатывает алерт `D8SdsLocalVolumeCSIFinalizerRemovalErrors`.

Снапшоты собираются по тому же правилу и публикуются так же — в метриках `sds_local_volume_orphaned_lvm_logical_volume_snapshot_count` и `sds_local_volume_orphaned_lvm_logical_volume_snapshot_used_bytes`, — с одной собственной причиной: `volume_snapshot_content_exists` означает, что на снапшот всё ещё ссылается VolumeSnapshotContent, то есть снапшот жив и вручную удалён только лежащий под ним ресурс. В этом случае удаляйте VolumeSnapshot, а остальное снимет snapshot-controller.

Удаляемый ресурс, на котором finalizer'а этого модуля уже нет, а finalizer `sds-node-configurator` ещё стоит, учитывается в метрике `sds_local_volume_awaiting_agent_count`. Через это состояние проходит каждое удаление, а том с заданным `spec.volumeCleanup` перед `lvremove` полностью перезаписывается, поэтому время в этой метрике — норма, а не неисправность: алерт `D8SdsLocalVolumeLVMLogicalVolumeDeletionStuck` срабатывает только после двух часов ожидания. В эту же метрику попадают ресурсы, которые модулю никогда не принадлежали: по отсутствию finalizer'а нельзя отличить «модуль уже разблокировал удаление» от «ресурс никогда не был нашим». Ни одна orphan-метрика (метрика по «осиротевшим» ресурсам) их не покрывает — по той же причине.

Задержка между запросом на удаление тома и снятием finalizer нужна, чтобы уже выполняющийся вызов `DeleteVolume` успел снять finalizer сам. По умолчанию она составляет 30 секунд и задаётся параметром модуля `llvOrphanGracePeriod`.
