# ====================================================================
# Media Scheduler Makefile
# ====================================================================
# This Makefile should be run from the project root directory.
# ====================================================================

# Image build variables
IMG_NAME ?= media-scheduler
IMG_TAG ?= v1.0.0
IMG_REGISTRY ?= ghcr.io/$(USER)
FULL_IMG ?= $(IMG_REGISTRY)/$(IMG_NAME):$(IMG_TAG)

# Build variables
GOOS ?= linux
GOARCH ?= amd64
OUT_DIR ?= ./bin

# ====================================================================
# Targets
# ====================================================================

.PHONY: all
all: build

## build: Build the Go binary locally
.PHONY: build
build:
	@echo "Building $(OUT_DIR)/media-scheduler..."
	@mkdir -p $(OUT_DIR)
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build \
		-ldflags="-w -s" \
		-o $(OUT_DIR)/media-scheduler \
		./pkg/media-scheduler/cmd/scheduler

## build-local: Build for local platform
.PHONY: build-local
build-local:
	@echo "Building $(OUT_DIR)/media-scheduler (local)..."
	@mkdir -p $(OUT_DIR)
	go build \
		-o $(OUT_DIR)/media-scheduler \
		./pkg/media-scheduler/cmd/scheduler

## docker-build: Build Docker image
.PHONY: docker-build
docker-build:
	@echo "Building Docker image: $(FULL_IMG)"
	docker build -t $(FULL_IMG) -f pkg/media-scheduler/Dockerfile .

## docker-push: Push Docker image to registry
.PHONY: docker-push
docker-push: docker-build
	@echo "Pushing Docker image: $(FULL_IMG)"
	docker push $(FULL_IMG)

## docker-load: Load image from tarball (for kind/minikube)
.PHONY: docker-load
docker-load: docker-build
	@echo "Loading image to kind/minikube..."
	kind load docker-image $(FULL_IMG) 2>/dev/null || \
	minikube image load $(FULL_IMG) 2>/dev/null || \
	echo "Skipping kind/minikube load (not found)"

## run: Run locally with config file
.PHONY: run
run: build-local
	@echo "Running scheduler with local config..."
	./$(OUT_DIR)/media-scheduler --config=pkg/media-scheduler/config/media-scheduler.profile.yaml

## test: Run tests
.PHONY: test
test:
	go test -v ./pkg/media-scheduler/...

## clean: Clean build artifacts
.PHONY: clean
clean:
	@echo "Cleaning..."
	rm -rf $(OUT_DIR)

## help: Show this help message
.PHONY: help
help:
	@echo "Media Scheduler Makefile"
	@echo ""
	@echo "Usage (from project root):"
	@echo "  make [target]"
	@echo ""
	@echo "Targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'