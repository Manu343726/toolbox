SHELL := /bin/sh
export PATH := $(HOME)/go/bin:$(PATH)

SUBSYSTEMS := $(sort $(notdir $(wildcard ../../subsystems/*)))

.PHONY: all build test fmt vet clean proto

all: build

proto:
	@for subsystem in $(SUBSYSTEMS); do $(MAKE) -C ../../subsystems/$$subsystem proto; done

build: proto
	@mkdir -p bin
	go build -o bin/toolsbox .

test: proto
	go test ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -name '*.pb.go' -not -name '*.connect.go')

vet: proto
	go vet ./...

clean:
	rm -rf bin
