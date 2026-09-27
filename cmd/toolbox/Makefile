SHELL := /bin/sh
export PATH := $(HOME)/go/bin:$(PATH)

SUBSYSTEMS := $(sort $(notdir $(wildcard ../../subsystems/*)))

# TAGS is empty by default, which is the portable build. `make host TAGS=fuse` is the same binary
# with the knowledge subsystem's mount compiled in, and it exists because the FUSE mount is behind a
# build tag: without a way to ask for that tag, the only obtainable binary is the one without it,
# and the half of M-6 that says the mount is optional is the only half that can be tested.
TAGS ?=
GO_TAGS := $(if $(TAGS),-tags $(TAGS),)

.PHONY: all build test fmt vet clean proto

all: build

proto:
	@for subsystem in $(SUBSYSTEMS); do $(MAKE) -C ../../subsystems/$$subsystem proto; done

build: proto
	@mkdir -p bin
	go build $(GO_TAGS) -o bin/toolbox .

test: proto
	go test $(GO_TAGS) ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -name '*.pb.go' -not -name '*.connect.go')

vet: proto
	go vet $(GO_TAGS) ./...

clean:
	rm -rf bin
