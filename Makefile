VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOOS    ?= $(shell go env GOOS)
GOARCH  ?= $(shell go env GOARCH)
EXT     := $(if $(filter windows,$(GOOS)),.exe,)
OUT     := bin/homebackupd-$(GOOS)-$(GOARCH)$(EXT)

.PHONY: build test vet fmt all-platforms sign clean

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

# Sign the macOS binary with a Developer ID so macOS remembers its privacy
# permissions (removable volumes, Full Disk Access) across updates.
# Usage: make sign CODESIGN_IDENTITY="Developer ID Application: Name (TEAMID)"
sign:
	codesign --force --sign "$(CODESIGN_IDENTITY)" --identifier com.github.leohchen.homebackupd \
		--options runtime --timestamp $(OUT)
	codesign --verify --strict $(OUT)

clean:
	rm -rf bin
