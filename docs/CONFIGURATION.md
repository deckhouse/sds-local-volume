---
title: "The sds-local-volume module: configuration"
force_searchable: true
description: The sds-local-volume module's configuration.
weight: 3
---

## Enabling the module

To enable the `sds-local-volume` module, follow these steps:

{{< alert level="info" >}}
All commands must be run on a machine with access to the Kubernetes API and administrator privileges.
{{< /alert >}}

1. Enable the `sds-local-volume` module:

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
       enableThinProvisioning: true # if you plan to use LVM Thin volumes
   EOF
   ```

1. Wait for the `sds-local-volume` module to transition to the `Ready` state:

   ```shell
   d8 k get modules sds-local-volume -w
   ```

1. Verify that the module pods are running:

   ```shell
   d8 k -n d8-sds-local-volume get pod -owide
   d8 k -n d8-sds-node-configurator get pod -o wide
   ```

For detailed module setup instructions, see the [Quick start](./quick_start.html) section.
