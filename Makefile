VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOOS    ?= $(shell go env GOOS)
GOARCH  ?= $(shell go env GOARCH)
EXT     := $(if $(filter windows,$(GOOS)),.exe,)
OUT     := bin/homebackupd-$(GOOS)-$(GOARCH)$(EXT)

.PHONY: build test vet fmt all-platforms clean

build:
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath \
		-ldflags "-s -w -X main.version=$(VERSION)" -o $(OUT) ./cmd/homebackupd

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

all-platforms:
	$(MAKE) build GOOS=darwin  GOARCH=arm64
	$(MAKE) build GOOS=darwin  GOARCH=amd64
	$(MAKE) build GOOS=linux   GOARCH=arm64
	$(MAKE) build GOOS=linux   GOARCH=amd64
	$(MAKE) build GOOS=windows GOARCH=amd64

clean:
	rm -rf bin
