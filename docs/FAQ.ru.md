---
title: "Модуль sds-local-volume: FAQ"
description: "Модуль sds-local-volume: FAQ"
weight: 6
---

## Когда следует использовать LVM, а когда LVM Thin?

Используйте LVM (Thick), если нужна максимальная производительность, сравнимая с производительностью накопителя. LVM (Thick) проще в настройке.

Используйте LVM Thin, если нужно использовать overprovisioning. Производительность LVM Thin ниже, чем у LVM.

{{< alert level="warning" >}}
Используйте overprovisioning в LVM Thin с осторожностью. Контролируйте наличие свободного места в пуле. В системе мониторинга кластера есть отдельные события при достижении 20%, 10%, 5% и 1% свободного места в пуле.

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
