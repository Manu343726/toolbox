SHELL := /bin/sh
export PATH := $(HOME)/go/bin:$(PATH)

NAME := apimcp
MODULE := github.com/Manu343726/toolbox/subsystems/apimcp
PROTO_SRCS := $(shell find proto -name '*.proto' -print | sort)
FRAMEWORK_PROTO_DIR := ../../pkg/api/proto
BIN := bin/$(NAME)

.PHONY: all build test test-short proto proto-tools fmt vet clean

all: build

build: proto
	@mkdir -p $(dir $(BIN))
	go build -o $(BIN) ./cmd/$(NAME)

test: proto
	go test -count=1 ./...

test-short: proto
	go test -short -count=1 ./...

# This subsystem implements the framework's parser and adapter contracts and owns
# no feature proto of its own, so there is nothing to generate unless the
# subsystem gains a document of its own later.
proto: $(PROTO_SRCS)
ifeq ($(strip $(PROTO_SRCS)),)
	@:
else
	@command -v protoc >/dev/null || (echo "protoc is required" >&2; exit 1)
	@command -v protoc-gen-go >/dev/null || (echo "run 'make proto-tools' first" >&2; exit 1)
	@command -v protoc-gen-connect-go >/dev/null || (echo "run 'make proto-tools' first" >&2; exit 1)
	protoc --proto_path=. --proto_path=$(FRAMEWORK_PROTO_DIR) --go_out=. --go_opt=module=$(MODULE) --connect-go_out=. --connect-go_opt=module=$(MODULE) --descriptor_set_out=proto/$(NAME).pb --include_imports --include_source_info $(PROTO_SRCS)
endif

fmt:
	gofmt -w $$(find . -name '*.go' -not -name '*.pb.go' -not -name '*.connect.go')

vet: proto
	go vet ./...

clean:
	rm -rf bin
	find . -type f \( -name '*.pb.go' -o -name '*.connect.go' \) -delete
	-[ ! -d proto ] || find proto -name '*.pb' -delete
