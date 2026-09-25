SHELL := /bin/sh

export PATH := $(HOME)/go/bin:$(PATH)

SUBSYSTEMS := $(sort $(notdir $(wildcard subsystems/*)))
BIN_DIR := bin

.PHONY: all build test test-short fmt vet clean proto-tools host

all: build

# Build each independent subsystem through its own Makefile.
build:
	@mkdir -p $(BIN_DIR)
	@for subsystem in $(SUBSYSTEMS); do \
		$(MAKE) -C subsystems/$$subsystem build; \
		cp subsystems/$$subsystem/bin/$$subsystem $(BIN_DIR)/$$subsystem; \
	done
	@$(MAKE) -C cmd/toolsbox build
	@cp cmd/toolsbox/bin/toolsbox $(BIN_DIR)/toolsbox

host:
	@$(MAKE) -C cmd/toolsbox build

test:
	@for subsystem in $(SUBSYSTEMS); do \
		$(MAKE) -C subsystems/$$subsystem test; \
	done
	@$(MAKE) -C cmd/toolsbox test
	@go test -count=1 ./...

test-short:
	@for subsystem in $(SUBSYSTEMS); do \
		$(MAKE) -C subsystems/$$subsystem test-short; \
	done
	@$(MAKE) -C cmd/toolsbox test
	@go test -short -count=1 ./...

fmt:
	@gofmt -w $$(find pkg -name '*.go' -not -name '*.pb.go' -not -name '*.connect.go')
	@for subsystem in $(SUBSYSTEMS); do $(MAKE) -C subsystems/$$subsystem fmt; done
	@$(MAKE) -C cmd/toolsbox fmt

vet:
	@for subsystem in $(SUBSYSTEMS); do $(MAKE) -C subsystems/$$subsystem vet; done
	@$(MAKE) -C cmd/toolsbox vet
	@go vet ./...

proto-tools:
	@for subsystem in $(SUBSYSTEMS); do $(MAKE) -C subsystems/$$subsystem proto-tools; done

clean:
	@for subsystem in $(SUBSYSTEMS); do $(MAKE) -C subsystems/$$subsystem clean; done
	@$(MAKE) -C cmd/toolsbox clean
	rm -rf $(BIN_DIR)
