SHELL := /bin/bash
TEST-ARGS=-race -timeout 30s -count 1
base_golang_20_alpine := golang:1.20-alpine3.18
repo_fox := registry.flant.com/deckhouse/storage
image_name := sds-lvm


deployment_name=sds-lvm-scheduler
deployment_container_name=sds-lvm-scheduler-extender
namespace=d8-sds-lvm
system_namespace=d8-system
pull_secret_name=fox-registry

USER := $(shell whoami)
image_tag := scheduler-extender-$(USER)

run: ## run go
		go run -race ./cmd/main.go

test:
		go test $(TEST-ARGS) ./...

fox_build_and_push:
		docker build --build-arg="BASE_GOLANG_20_ALPINE_BUILDER=$(base_golang_20_alpine)" . -t $(repo_fox)/$(image_name):$(image_tag)
		docker push $(repo_fox)/$(image_name):$(image_tag)

redeploy:
		kubectl -n $(namespace) rollout restart deployment $(deployment_name)
		kubectl -n $(namespace) rollout restart daemonset $(daemonset_name)

switch_to_local_dev:
		kubectl -n $(system_namespace) scale deployment deckhouse --replicas=0
		kubectl -n $(namespace) patch deployment $(deployment_name) -p \
		   '{"spec": {"template": {"spec": {"containers": [{"name": "$(deployment_container_name)", "image": "$(repo_fox)/$(image_name):$(image_tag)"}]}}}}'
		kubectl -n $(namespace) patch deployment $(deployment_name) -p \
		   '{"spec": {"template": {"spec": {"containers": [{"name": "$(deployment_container_name)", "imagePullPolicy": "Always"}]}}}}'
		kubectl  -n $(namespace) patch deployment $(deployment_name) --type='json' \
		   -p='[{"op": "add", "path": "/spec/template/spec/imagePullSecrets", "value": [{"name": "$(pull_secret_name)"}]}]'


.PHONY: switch_to_local_dev
