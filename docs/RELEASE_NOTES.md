---
title: "Release Notes"
---

## v0.3.11

* Added release notes
* Hooks switched from python to golang

## v0.3.10

* Added information about the need for snapshot-controller for module operation
* Added readonlyRootFilesystem for enhanced module security

## v0.3.9

* CVE fixes

## v0.3.8

* CVE fixes
* Internal changes for containerd v2 support
* Added dependency on snapshot-controller

## v0.3.7

* Added NodeGetVolumeStats method (for kubelet_volume_stats_* metrics support)

## v0.3.6

* Documentation fixes

## v0.3.5

* Several fixes for proper VolumeSnapshots support

## v0.3.4

* Completely fixed the issue with enabled csi snapshotter in CE version

## v0.3.3

* Fixed the issue with enabled csi snapshotter in CE version
* Enabled HA mode in CSI controller

## v0.3.2

* Technical release, module refactoring

## v0.3.1

* Documentation fixes
* Module refactoring

## v0.2.3

* Technical release. Removed "Preview" status from documentation

## v0.2.1

* Fixed issue where freed PVC could be considered as used
* Reduced module size, excluded build images

## v0.2.0

* Updated golang API libraries for sds-node-configurator v0.4.0 support
* Multiple fixes in controllers and documentation
* Added XFS filesystem support

## v0.1.2

* Removed isDefault field (use standard k8s SC annotation)
* Added support for contiguous volumes
* Added antiaffinity rules for controller HA mode
* Added AllocationLimit support
* Added health and readiness checks in controller

## v0.1.1

* Added documentation description of sds-local-volume pod management process

## v0.1.0

* Fix max volumes per node and R/W map
* Add LocalStorageClass validation webhook
* Add logs and upgrading node scoring
* Add module documentation
* Add cache for extender-scheduler
* Add previous channel release version check
* Fix KubeSchedulerConfiguration API version
