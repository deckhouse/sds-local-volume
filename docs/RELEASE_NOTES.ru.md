---
title: "Релизы"
---

## v0.3.11

* Добавлены файлы release notes
* Перевод хуков в модуле c python на golang

## v0.3.10

* Добавлена информация о необходимости snapshot-controller для работы модуля
* Добавлен readonlyRootFilesystem для большей безопасности модуля

## v0.3.9

* Фиксы CVE

## v0.3.8

* Фиксы CVE
* Внутренние изменения для поддержки containerd v2
* Добавлена зависимость от snapshot-controller

## v0.3.7

* Добавлен метод NodeGetVolumeStats (для поддержки метрик kubelet_volume_stats_*)

## v0.3.6

* Правки в документацию

## v0.3.5

* Несколько фиксов для корректной поддержки VolumeSnapshots

## v0.3.4

* До конца исправлена ошибка с включенным csi snapshotter в CE версии

## v0.3.3

* Исправлена ошибка с включенным csi snapshotter в CE версии
* Включен режим HA в CSI controller

## v0.3.2

* Технический релиз, рефакторинг модуля

## v0.3.1

* Правки в документацию
* Рефакторинг модуля

## v0.2.3

* Технический релиз. Убран статус "Превью" в документации

## v0.2.1

* Поправлена ошибка, при которой освобожденная PVC могла считаться используемой
* Уменьшен размер модуля, исключены сборочные образы

## v0.2.0

* Обновлены библиотеки golang API для поддержки sds-node-configurator v0.4.0
* Множественные правки в контроллеры и документации
* Добавлена поддержка файловой системы XFS

## v0.1.2

* Удалено поле isDefault (используйте стандартную аннотацию в k8s SC)
* Добавлена поддержка непрерывных (contiguous) томов
* Добавлены правила antiaffinity для HA режима контроллера
* Добавлена поддержка AllocationLimit
* Добавлены health и readiness проверки в контроллер

## v0.1.1

* Добавили в документацию описание процесса управления подами sds-local-volume

## v0.1.0

* Fix max volumes per node and R/W map
* Add LocalStorageClass validation webhook
* Add logs and upgrading node scoring
* Add module documentation
* Add cache for extender-scheduler
* Add previous channel release version check
* Fix KubeSchedulerConfiguration API version
