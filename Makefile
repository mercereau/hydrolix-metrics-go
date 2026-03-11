#!make
BINARY ?= hydrolix-collector
PACKAGE := "gitlab-master.nvidia.com/it/sre/cdn/observability/hydrolix-collector"
DATE    ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

DL             := it-cdn-admin-vault-admins
NAMESPACE      := it-cdn-services
APE_BASE       := gitlab-master.nvidia.com/ape-repo/projects
APE_PROJECT    := cdn-metrics
CONTAINER_NAME := hydrolix-collector

BASE_VERSION := v1.1.0
GIT_BRANCH := $(shell git rev-parse --abbrev-ref HEAD | sed 's|/|-|g')
GIT_COMMIT := $(shell git rev-parse --short HEAD)
GIT_MAIN   := $(shell git rev-list --left-right --count main...origin/main)
# Map certain branches to version labels
ifeq ($(GIT_BRANCH),main)
	# if on main branch, mark as clean
  VERSION := $(BASE_VERSION)-$(GIT_COMMIT)
else
  VERSION := $(BASE_VERSION)-$(GIT_COMMIT)-dirty
endif

LDFLAGS := -s -w -X '$(PACKAGE)/internal/build.Version=$(VERSION)' -X '$(PACKAGE)/internal/build.Commit=$(GIT_COMMIT)' -X '$(PACKAGE)/internal/build.Date=$(DATE)'
DOCKER  := $(APE_BASE)/$(APE_PROJECT)/$(CONTAINER_NAME):$(VERSION)

.PHONY: all prepare build run test fmt vet lint clean version build-docker

all: version prepare build

version:
	@echo $(DOCKER)

prepare:
	go mod tidy

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .

run:
	go run -ldflags "$(LDFLAGS)" .

test:
	go test ./...

fmt:
	go fmt ./...

vet:
	go vet ./...

lint: vet

clean:
	rm -rf bin

vault-login:
	export VAULT_ADDR=https://stg.vault.nvidia.com && export VAULT_NAMESPACE=it-cdn-services && vault login -method=oidc -path=oidc-admins role=namespace-admin

docker-login:
	@if [ -z "$(GITLAB_TOKEN)" ]; then \
		echo "GITLAB_TOKEN is not set. Please set it to your GitLab personal access token."; \
		exit 0; \
	fi
	@echo "Logging in to gitlab-master.nvidia.com:5005/ape-repo/projects/cdn-metrics..."; 
	@echo "$(GITLAB_TOKEN)" | docker login gitlab-master.nvidia.com:5005/ape-repo/projects/cdn-metrics --username "$$(echo $$USER)" --password-stdin \

docker-build:
	@docker buildx build --platform linux/amd64,linux/arm64 --builder mybuilder -t $(DOCKER) .

docker-push:
	@docker buildx build --platform linux/amd64,linux/arm64 --builder mybuilder -t $(DOCKER) --push --provenance false .

docker-run:
	docker run -it $(DOCKER)

deploy: docker-login docker-build docker-push