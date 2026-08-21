GO      ?= go
DIST    := dist
CLI     := $(DIST)/aviflph
SO      := $(DIST)/libaviflph.so
WASM    := $(DIST)/aviflph.wasm

.PHONY: all cli capi wasm cross test clean

all: cli capi wasm

cross:
	./scripts/cross-build.sh

cli:
	$(GO) build -trimpath -ldflags "-s -w" -o $(CLI) ./cmd/aviflph

capi:
	$(GO) build -buildmode=c-shared -o $(SO) ./cmd/lpshared
	cp capi/aviflph.h $(DIST)/aviflph.h

wasm:
	GOOS=js GOARCH=wasm $(GO) build -trimpath -ldflags "-s -w" -o $(WASM) ./cmd/lpwasm

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

clean:
	rm -rf $(DIST)
