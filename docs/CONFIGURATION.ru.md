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
     version: 1
     settings:
       enableThinProvisioning: true # если планируете использовать LVM Thin-тома
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
