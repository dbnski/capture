TARGET = capture
VERSION := $(shell cat VERSION)
COMMIT_HASH := $(shell git rev-parse --short HEAD)
BUILD_TIME := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
GOARCH ?= $(shell go env GOARCH)
CGO_ENABLED ?= 1

export VERSION
export COMMIT_HASH
export BUILD_TIME

all:
	@echo "Building with version: $(VERSION), commit: $(COMMIT_HASH), time: $(BUILD_TIME)"
	CGO_ENABLED=$(CGO_ENABLED) GOARCH=$(GOARCH) go build -trimpath -ldflags "-w -s -X 'main.Version=$(VERSION)' -X 'main.CommitHash=$(COMMIT_HASH)' -X 'main.Build=release' -X 'main.BuildTime=$(BUILD_TIME)'" -o $(TARGET) .

release:
	@echo "Building with version: $(VERSION), commit: $(COMMIT_HASH), time: $(BUILD_TIME)"
	CGO_ENABLED=$(CGO_ENABLED) GOARCH=$(GOARCH) go build -a -trimpath -ldflags "-w -s -X 'main.Version=$(VERSION)' -X 'main.CommitHash=$(COMMIT_HASH)' -X 'main.Build=release' -X 'main.BuildTime=$(BUILD_TIME)'" -o $(TARGET) .
