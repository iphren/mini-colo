# The box's CPU architecture: amd64 for Intel/AMD, arm64 for 64-bit ARM.
GOARCH ?= amd64

.PHONY: build linux test

build:
	go build -o mini-colo .

# Cross-compile for the box. CGO_ENABLED=0 makes the binary fully static,
# so it does not depend on the box's C library version.
linux:
	GOOS=linux GOARCH=$(GOARCH) CGO_ENABLED=0 go build -o dist/mini-colo-linux-$(GOARCH) .

test:
	go vet ./...
	go test ./...
