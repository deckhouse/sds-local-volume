# sds-local-volume e2e

Minimal end-to-end suite for the sds-local-volume module, built on the shared
[`storage-e2e`](https://github.com/deckhouse/storage-e2e) harness (Ginkgo v2).

The suite:

1. Connects to (or provisions) a nested test cluster via `storage-e2e`.
2. Brings up the module stack declared in `tests/cluster_config.yml`:
   `snapshot-controller` (mpo `main`), `sds-node-configurator` (mpo `main`) and
   `sds-local-volume` (mpo `main`; in CI the pipeline overrides it with the PR
   image tag via the `module_image_tag` input).
3. Labels the worker nodes, ensures each has a consumable `BlockDevice`
   (attaching a raw disk at runtime when a base cluster is available), and turns
   each into a labelled `LVMVolumeGroup`.
4. Verifies provisioning through both a **name-based** and a
   **labelSelector-based** `LocalStorageClass`: the managed `StorageClass` is
   created and a PVC binds with a consumer Pod running.

## Running locally

```bash
source ./test_exports        # cluster + registry + SSH/Commander env
cd e2e
make test                    # or: make test-focus FOCUS="labelSelector"
```

Run `make check-env` for the list of environment variables.

## CI

`.github/workflows/e2e-tests.yml` has two jobs calling the reusable `storage-e2e`
pipeline; both install the module under test at the PR image tag (`pr<N>`) with
dependencies from `main`, and are triggered by separate labels so they can run
independently:

- `e2e` — default variant, gated on the `e2e/run` label; uses
  `tests/cluster_config.yml` (the pipeline injects the PR tag).
- `e2e-commander` — commander-provider variant, gated on the `e2e/commander/run`
  label; creates a fresh nested cluster via Deckhouse Commander and uses
  `tests/cluster_config.ci.yml`.
