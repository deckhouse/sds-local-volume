.PHONY: test test-focus check-env

# Run the full e2e suite. Requires a storage-e2e nested-cluster environment
# (see check-env). Source your env first, e.g. `source ./test_exports`.
test:
	go test -v -count=1 -timeout 60m ./tests/ -ginkgo.timeout 60m

# Run a focused subset, e.g. `make test-focus FOCUS="labelSelector"`.
test-focus:
	go test -v -count=1 -timeout 60m ./tests/ -ginkgo.timeout 60m -ginkgo.focus "$(FOCUS)"

check-env:
	@echo "Required (read by storage-e2e):"
	@echo "  TEST_CLUSTER_CREATE_MODE   alwaysUseExisting | alwaysCreateNew | commander"
	@echo "  TEST_CLUSTER_NAMESPACE     namespace for PVCs/Pods (default e2e-sds-local-volume)"
	@echo "  TEST_CLUSTER_STORAGE_CLASS base-cluster StorageClass for raw disk attach (create modes)"
	@echo "  DKP_LICENSE_KEY, REGISTRY_DOCKER_CFG, SSH_* / COMMANDER_* per provider"
	@echo "Optional (suite):"
	@echo "  SDS_LOCAL_VOLUME_MODULE_PULL_OVERRIDE  module tag for local runs (default main)"
	@echo "  E2E_PVC_SIZE, E2E_DISK_SIZE, E2E_MODULE_READY_TIMEOUT"
