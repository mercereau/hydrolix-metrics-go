#!make
BINARY ?= hydrolix-collector
PACKAGE := "github.com/mercereau/hydrolix-metrics-go"
DATE    ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

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

.PHONY: all prepare build run test fmt vet lint clean version docker-up

all: version prepare build

prepare:
	go mod tidy

test:
	go test ./...

fmt:
	go fmt ./...

vet:
	go vet ./...

lint: vet

build: lint fmt test
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .

run:
	go run -ldflags "$(LDFLAGS)" .

clean:
	rm -rf bin

docker-build:
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(GIT_COMMIT) \
		--build-arg DATE=$(DATE) \
		-t hydrolix-collector .

docker-up: docker-build
	docker compose up