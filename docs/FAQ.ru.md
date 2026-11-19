---
title: "Модуль sds-local-volume: FAQ"
description: "Модуль sds-local-volume: FAQ"
weight: 6
---

## Когда следует использовать LVM, а когда LVM-thin?

Используйте LVM (Thick), если нужна максимальная производительность, сравнимая с производительностью накопителя. LVM (Thick) проще в настройке.

Используйте LVM-thin, если нужно использовать overprovisioning. Производительность LVM-thin ниже, чем у LVM.

{{< alert level="warning" >}}
Допустимый overprovisioning для thin pool задаётся полем `allocationLimit` у thin pool в ресурсе [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup): суммарный размер томов и снапшотов в пуле не может превышать размер пула, умноженный на это значение. Значение по умолчанию — `150%`, оно допускает overprovisioning. Чтобы отключить его, укажите `100%`: тогда том, расширение или снапшот, которые не помещаются в пул, не создаются, а отклоняются. Подробнее см. [Как работает allocationLimit у thin pool и можно ли его изменить?](/modules/sds-node-configurator/faq.html#как-работает-allocationlimit-у-thin-pool-и-можно-ли-его-изменить).

Если предел выше `100%`, контролируйте наличие свободного места в пуле, например с помощью метрик `sds_node_configurator_lvm_thin_pool_used_percent` и `sds_node_configurator_lvm_thin_pool_metadata_used_percent`. При отсутствии свободного места в пуле возможна деградация работы модуля и потеря данных.
{{< /alert >}}

## Почему не удается создать PVC на выбранном узле?

Проверьте, что на выбранном узле работает под `csi-node`:

```shell
d8 k -n d8-sds-local-volume get po -owide
```

Если под отсутствует, убедитесь, что на узле присутствуют все метки, указанные в поле `nodeSelector` в настройках модуля. Подробнее см. [Почему служебные поды компонентов sds-local-volume не создаются на нужном узле](#почему-служебные-поды-компонентов-sds-local-volume-не-создаются-на-нужном-узле).

## Почему PVC с режимом доступа ReadWriteMany остаётся в состоянии Pending?

Модуль поддерживает только режим доступа `ReadWriteOnce`, так как том — это логический том на дисках одного узла. Режимы `ReadWriteOncePod`, `ReadWriteMany` и `ReadOnlyMany` отклоняются при создании тома. PVC остаётся в состоянии `Pending`, а в его событиях есть `ProvisioningFailed` с названием отклонённого режима доступа:

```shell
d8 k -n <namespace> describe pvc <pvc-name>
```

Укажите `ReadWriteOnce` в `spec.accessModes` PVC и создайте его заново. Режим доступа существующего PVC изменить нельзя. Тома, созданные ранее с другим режимом доступа, проверка не затрагивает.

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

Снапшоты собираются по тому же правилу и публикуются так же — в метриках `sds_local_volume_orphaned_lvm_logical_volume_snapshot_count` и `sds_local_volume_orphaned_lvm_logical_volume_snapshot_used_bytes`, — с одной собственной причиной: `volume_snapshot_content_exists` означает, что на снапшот всё ещё ссылается VolumeSnapshotContent, то есть снапшот жив и вручную удалён только лежащий под ним ресурс. В этом случае удаляйте VolumeSnapshot, а остальное снимет storage-foundation.

Удаляемый ресурс, на котором finalizer'а этого модуля уже нет, а finalizer `sds-node-configurator` ещё стоит, учитывается в метрике `sds_local_volume_awaiting_agent_count`. Через это состояние проходит каждое удаление, а том с заданным `spec.volumeCleanup` перед `lvremove` полностью перезаписывается, поэтому время в этой метрике — норма, а не неисправность: алерт `D8SdsLocalVolumeLVMLogicalVolumeDeletionStuck` срабатывает только после двух часов ожидания. В эту же метрику попадают ресурсы, которые модулю никогда не принадлежали: по отсутствию finalizer'а нельзя отличить «модуль уже разблокировал удаление» от «ресурс никогда не был нашим». Ни одна orphan-метрика (метрика по «осиротевшим» ресурсам) их не покрывает — по той же причине.

Задержка между запросом на удаление тома и снятием finalizer нужна, чтобы уже выполняющийся вызов `DeleteVolume` успел снять finalizer сам. По умолчанию она составляет 30 секунд и задаётся параметром модуля `llvOrphanGracePeriod`.

## Сколько места тома резервируется под суперпользователя?

По умолчанию — нисколько: модуль форматирует ext4 с `-m 0`, весь объём тома доступен нагрузке.

Если классический 5-процентный резерв ext4 всё же нужен — например, чтобы привилегированный процесс мог писать в файловую систему после того, как нагрузка заняла её целиком, — задайте процент аннотацией на ресурсе `LocalStorageClass`:

```shell
d8 k annotate localstorageclass <lsc-name> storage.deckhouse.io/ext4-reserved-percent=5
```

Значение — целое число процентов от 0 до 50; некорректное значение отклоняется валидирующим вебхуком.

Резерв применяется только к томам, созданным после установки аннотации: уже существующие файловые системы сохраняют тот резерв, с которым были созданы. Блоки для суперпользователя резервируют только файловые системы семейства ext: при `fsType: xfs` аннотация игнорируется, а вебхук возвращает предупреждение об этом.

Изменение аннотации приводит к пересозданию StorageClass контроллером, поскольку поле `parameters` у существующего StorageClass в Kubernetes иммутабельно. На уже созданные тома и PVC это не влияет.

## Почему срабатывает алерт `D8SdsLocalVolumeLocalStorageClassNotReady`?

Условие `Ready` у LocalStorageClass уже 15 минут не в статусе `True`. Посмотрите условие, чтобы понять, какая из двух причин:

```shell
d8 k get lsc <lsc-name> -o jsonpath='{.status.conditions[?(@.type=="Ready")]}'
```

- `reason: ReconcileFailed` — контроллер не смог привести класс к его спецификации, причина указана в сообщении. Исправьте спецификацию или LVMVolumeGroup, на которые она ссылается; контроллер повторит попытку сам.
- `reason: Deleting` — класс удаляется, но его удерживает финализатор другого контроллера. StorageClass модуля к этому моменту уже удалён. Ищите причину на стороне контроллера, которому принадлежит финализатор, а не снимайте финализатор вручную.

Если команда ничего не вывела, контроллер не смог записать статус класса. Причина — в логе контроллера:

```shell
d8 k -n d8-sds-local-volume logs deploy/controller -c controller --tail=200 | grep <lsc-name>
```

Когда условие изменилось и что оно сообщало раньше, видно в событиях (Events) класса: контроллер создаёт событие `Warning`, когда `Ready` становится `False`, и `Normal`, когда оно снова становится `True`, с причиной условия. Класс — кластерный ресурс, поэтому его события лежат в пространстве имён `default`, и по умолчанию API-сервер хранит их час:

```shell
d8 k describe lsc <lsc-name>
```

Класс, часть LVMVolumeGroup которого находится на узлах без CSI-узла модуля, остаётся `Ready` с причиной `LVMVolumeGroupsOutsideCSINodes` и алерт не вызывает.

## Как посмотреть, сколько места осталось у LocalStorageClass?

Откройте дашборд Grafana «SDS Local Volume», выберите класс в переменной «LocalStorageClass» вверху и смотрите строку «Capacity by LocalStorageClass»:

- Для класса `Thick` — размер его групп томов, свободное место в них и занятую долю. Доля учитывает всё, что лежит в группах томов, в том числе thin-пулы и тома других классов на тех же LVMVolumeGroup.
- Для класса `Thin` — размер его thin-пулов, объём, выделенный томам, предел выделения, место, свободное с учётом `allocationLimit`, физически свободное место в пулах и долю пулов, заполненную данными. Место, свободное с учётом `allocationLimit`, — это размер пула, умноженный на `allocationLimit` thin-пула в LVMVolumeGroup (по умолчанию 150%), за вычетом запрошенных размеров уже созданных в пуле томов; считается по каждому thin-пулу. Столько ещё можно выдать новым томам класса — это та же величина, которую модуль проверяет при создании тома, поэтому overprovisioning (выделение томам больше места, чем есть в пуле) учтён. О том, сколько данных пул ещё вместит, эта величина не говорит: следите за долей, заполненной данными, потому что переполнение thin-пула останавливает все тома в нём.
- Таблица внизу строки показывает те же величины по каждому узлу и LVMVolumeGroup.
- «LVMVolumeGroups without capacity data» — число LVMVolumeGroup класса (для класса `Thin` — thin-пулов), по которым sds-node-configurator не сообщает ёмкость: LVMVolumeGroup не существует, её группа томов или thin-пул ещё не созданы либо агент на её узле не работает. В итоговые суммы они не входят.

Ёмкость берётся из метрик sds-node-configurator `sds_node_configurator_lvg_vg_size_bytes`, `sds_node_configurator_lvg_vg_free_bytes` и `sds_node_configurator_lvg_thin_pool_*_bytes` с лейблами LVMVolumeGroup. С классами их связывает метрика модуля `sds_local_volume_local_storage_class_lvg_info`: значение `1` для каждой LVMVolumeGroup, на которой класс создаёт тома, с лейблами `storage_class`, `lvg_name` и `thin_pool` (пустой для класса `Thick`). Записи с `labelSelector` разворачиваются так же, как для StorageClass, а серии следуют за изменениями класса и меток LVMVolumeGroup; удалённый класс перестаёт публиковаться.

Чтобы использовать метрику в своих запросах, выберите один класс и соединяйте ряды по `lvg_name`, а для класса `Thin` — ещё и по `thin_pool`. Например, свободное место класса `Thick`:

```text
sum(
  sds_node_configurator_lvg_vg_free_bytes
  * on (lvg_name) group_left (storage_class)
  max by (storage_class, lvg_name) (sds_local_volume_local_storage_class_lvg_info{storage_class="<lsc-name>", thin_pool=""})
)
```

Класс нужно выбрать заранее: одна LVMVolumeGroup может использоваться несколькими классами, а соединение падает, если справа на одну LVMVolumeGroup приходится больше одной серии. `max by` сохраняет работоспособность запроса, пока у контроллера меняется лидер.

## Почему thin-пул заканчивается, когда места, свободного с учётом allocationLimit, ещё много, и сколько места свободно физически?

У класса `Thin` две разные величины свободного места, и снимки влияют на них по-разному.

- Место, свободное с учётом `allocationLimit`, — это размер пула, умноженный на `allocationLimit`, за вычетом `allocatedSize`: суммы размеров всех thin-томов (thin logical volume) пула. Снимок и том, восстановленный из снимка, — тоже thin-тома, и каждый учитывается в полном размере источника, хотя делит с ним неизменённые блоки. Эту величину проверяют компоненты модуля, когда выдают место: CSI-контроллер для тома с `Immediate`, scheduler extender при `WaitForFirstConsumer` и агент при создании снимка — снимок отклоняется, если размер его источника больше места, свободного с учётом `allocationLimit`. Это бюджет для новых томов, а не показатель диска.
- Физически свободное место — это `actualSize` за вычетом `usedSize` thin-пула в `status.thinPools` ресурса [LVMVolumeGroup](/modules/sds-node-configurator/cr.html#lvmvolumegroup). `usedSize` — часть пула, заполненная данными, в том виде, в каком пул сообщает её LVM: блок считается один раз, сколько бы томов, снимков и восстановленных томов его ни делили. Учитывается каждый блок, который снимок сохраняет после того, как источник перезаписан или удалён, а блок, записанный в восстановленный том, занимает новое место. Это место, которое пул ещё может вместить; когда оно кончается, останавливаются все тома пула.

Поэтому у пула с большим числом снимков место, свободное с учётом `allocationLimit`, может быть малым при большом физически свободном месте, и наоборот: пул с большим `allocationLimit` может быть физически заполнен, пока предел показывает свободное место.

Обе величины класса видны в строке «Capacity by LocalStorageClass» дашборда «SDS Local Volume»: «Thin: free by allocationLimit» и «Thin: free on disk» стоят рядом, а в таблице есть колонка «Free on disk» по каждому thin-пулу. Для собственного запроса соедините ряды класса так, как описано выше:

Те же величины есть в `status.capacity` ресурса LocalStorageClass: суммой по thin-пулам класса и по каждому пулу в `status.capacity.pools`; `freeOnDisk` и `freeByAllocationLimit` выводит и `d8 k get localstorageclass -o wide`. Статус пишет контроллер модуля, и только для класса `Thin`. Он обновляется, когда величина меняется больше чем на 1% размера пула, когда меняются пулы класса, либо раз в 5 минут; `status.capacity.updatedAt` показывает, когда это было в последний раз, поэтому статус может отставать от LVMVolumeGroup на эту величину. Сами величины LVMVolumeGroup приходят от агента sds-node-configurator: он читает LVM не реже раза в 30 секунд и публикует `usedSize` thin-пула, когда тот меняется на 1% пула, поэтому `status.capacity.used` может отставать от данных на диске примерно на минуту и до 2% пула. Более старые релизы sds-node-configurator обновляют `usedSize` только после события блочного устройства на узле — создания или удаления тома или снимка, перезапуска агента. `poolsWithoutData` — число thin-пулов класса, по которым статус LVMVolumeGroup не содержит данных; в итоги они не входят.

```text
sum(
  clamp_min(sds_node_configurator_lvg_thin_pool_actual_size_bytes - sds_node_configurator_lvg_thin_pool_used_size_bytes, 0)
  * on (lvg_name, thin_pool) group_left (storage_class)
  max by (storage_class, lvg_name, thin_pool) (sds_local_volume_local_storage_class_lvg_info{storage_class="<lsc-name>", thin_pool!=""})
)
```

Что нужно учитывать:

- `usedSize` вычисляется из процента заполнения данными, который сообщает LVM для пула, поэтому он приблизителен. Метаданные пула он не включает: пул с заполненными метаданными тоже останавливается, поэтому, если данных далеко до заполнения, а тома всё равно отказывают, проверьте `metadata_percent` пула командой `lvs` на узле.
- Удаление PVC, VolumeSnapshot или восстановленного тома возвращает место не сразу: блок освобождается, когда его не использует ни один логический том, а величины меняются со следующим обновлением статуса LVMVolumeGroup. См. [Что происходит со снимками и восстановленными томами, когда один из них удаляют?](#что-происходит-со-снимками-и-восстановленными-томами-когда-один-из-них-удаляют).
- Удаление файлов внутри тома возвращает их блоки в пул, только если файловая система передаёт discard вниз: выполните `fstrim` на смонтированном томе или смонтируйте его с опцией `discard`. Иначе пул по-прежнему считает блоки записанными, и `usedSize` остаётся высоким.
- Удаление снимка освобождает только блоки, которые больше не использует ни другой снимок, ни восстановленный том, ни исходный том.
- Величины считаются по каждому thin-пулу и узлу. Физически свободное место класса — сумма по его пулам, но том создаётся в одном пуле и не может использовать место другого узла.

## Почему снимок тома (VolumeSnapshot) не становится готовым?

Снимки томов доступны только в коммерческих редакциях Deckhouse Kubernetes Platform и только для томов LVM-thin. Снимок, который невозможно создать, не ждёт без статуса: причина указана в поле `status.error` ресурса VolumeSnapshot и в его событиях типа `Warning`.

```shell
d8 k -n <namespace> get volumesnapshot <name> -o jsonpath='{.status.error.message}'
```

- В Community Edition модуль не разворачивает ни свой VolumeSnapshotClass `sds-local-volume-snapshot-class`, ни компонент создания снимков (snapshotter), а его StorageClass не указывают класс снимков в аннотации `storage.deckhouse.io/volumesnapshotclass`. VolumeSnapshot без `volumeSnapshotClassName` получает ошибку `Failed to set default snapshot class with error cannot find default snapshot class` (причина события `SetDefaultSnapshotClassFailed`), а VolumeSnapshot, в котором класс указан явно, — `Failed to get snapshot class with error volumesnapshotclass.snapshot.storage.k8s.io "sds-local-volume-snapshot-class" not found` (причина события `GetSnapshotClassFailed`). Не создавайте VolumeSnapshotClass для драйвера `local.csi.storage.deckhouse.io` самостоятельно: в Community Edition снимки по нему никто не создаёт, и они снова будут ждать без статуса.
- В коммерческой редакции снимок тома `Thick` получает ошибку, которая заканчивается на `Source LVMLogicalVolume '<name>' is not of 'Thin' type`.

## Как удалить VolumeSnapshot, зависшие в Community Edition?

Пока модуль разворачивал свой VolumeSnapshotClass и в Community Edition, VolumeSnapshot его тома получал VolumeSnapshotContent, который никто не обрабатывал, и оба оставались без статуса. После обновления такой VolumeSnapshot сообщает `Failed to get snapshot class` и никогда не будет создан. Удалите его:

```shell
d8 k -n <namespace> delete volumesnapshot <name> --wait=false
```

storage-foundation удаляет его VolumeSnapshotContent, но финализатор `snapshot.storage.kubernetes.io/volumesnapshotcontent-bound-protection` с VolumeSnapshotContent снимает только snapshotter, а в Community Edition его нет. VolumeSnapshotContent остаётся в состоянии `Terminating`, и VolumeSnapshot вместе с ним. Выведите такие VolumeSnapshotContent модуля:

```shell
d8 k get volumesnapshotcontent -o jsonpath='{range .items[?(@.spec.driver=="local.csi.storage.deckhouse.io")]}{.metadata.name}{"\t"}{.metadata.deletionTimestamp}{"\t"}{.status.snapshotHandle}{"\t"}{.metadata.finalizers}{"\n"}{end}'
```

Берите только те, у которых есть время удаления и нет идентификатора снимка (snapshot handle): для них на узле ничего не создавалось, поэтому снятие финализатора не приводит к потере данных. VolumeSnapshotContent с идентификатором снимка не трогайте: его снимок существует на узле, и удалить его может только CSI-драйвер. Если в списке финализаторов только указанный выше, снимите его:

```shell
d8 k patch volumesnapshotcontent <name> --type=merge -p '{"metadata":{"finalizers":null}}'
```

Если в списке есть и другие финализаторы, удалите только этот, например через `d8 k edit volumesnapshotcontent <name>`.

Когда VolumeSnapshotContent удалён, storage-foundation снимает оставшийся финализатор VolumeSnapshot, и VolumeSnapshot удаляется. Финализатор `snapshot.storage.kubernetes.io/pvc-as-source-protection` с исходного PVC storage-foundation тоже снимает сам, когда у этого PVC не остаётся незавершённых VolumeSnapshot.

## Что происходит со снимками и восстановленными томами, когда один из них удаляют?

Снимки и восстановление доступны только для томов LVM-thin, и все они остаются в thin pool (пуле тонких томов) и на узле исходного тома.

- Том, восстановленный из снимка, — это тонкий снимок этого снимка: неизменённые блоки он делит со снимком и исходным томом, а новое место в пуле занимают только блоки, записанные позже (copy-on-write — копирование при записи).
- Удалять можно в любом порядке. Удаление PVC исходного тома при `reclaimPolicy: Delete` удаляет его логический том, а его снимки по-прежнему можно восстановить. VolumeSnapshot можно удалить, пока существует восстановленный из него том. С восстановленного тома тоже можно снять снимок и восстановить его.
- Блок пула освобождается, когда его больше не использует ни один логический том — ни исходный, ни снимок, ни восстановленный. `allocationLimit` и свободное по нему место учитывают каждый снимок и каждый восстановленный том в полном размере исходного тома, хотя данные они делят с ним.
