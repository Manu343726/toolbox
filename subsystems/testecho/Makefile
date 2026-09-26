SHELL := /bin/sh
export PATH := $(HOME)/go/bin:$(PATH)

NAME := testecho
MODULE := github.com/Manu343726/toolbox/subsystems/testecho
PROTO_SRCS := $(shell find proto -name '*.proto' -print | sort)
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

proto-tools:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
	go install connectrpc.com/connect/cmd/protoc-gen-connect-go@v1.20.0

proto: $(PROTO_SRCS)
	@command -v protoc >/dev/null || (echo "protoc is required" >&2; exit 1)
	@command -v protoc-gen-go >/dev/null || (echo "run 'make proto-tools' first" >&2; exit 1)
	@command -v protoc-gen-connect-go >/dev/null || (echo "run 'make proto-tools' first" >&2; exit 1)
	protoc --proto_path=. --go_out=. --go_opt=module=$(MODULE) --connect-go_out=. --connect-go_opt=module=$(MODULE) $(PROTO_SRCS)

fmt:
	gofmt -w $$(find . -name '*.go' -not -name '*.pb.go' -not -name '*.connect.go')

vet: proto
	go vet ./...

clean:
	rm -rf bin
	find . -type f \( -name '*.pb.go' -o -name '*.connect.go' \) -delete
	find proto -name '*.pb' -delete
