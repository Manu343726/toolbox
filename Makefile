SHELL := /bin/sh

export PATH := $(HOME)/go/bin:$(PATH)

SUBSYSTEMS := $(sort $(notdir $(wildcard subsystems/*)))
BIN_DIR := bin

.PHONY: all build test test-short fmt vet clean proto-tools host check-tests

all: build

# Build each independent subsystem through its own Makefile.
build:
	@mkdir -p $(BIN_DIR)
	@for subsystem in $(SUBSYSTEMS); do \
		$(MAKE) -C subsystems/$$subsystem build; \
		cp subsystems/$$subsystem/bin/$$subsystem $(BIN_DIR)/$$subsystem; \
	done
	@$(MAKE) -C cmd/toolbox build
	@cp cmd/toolbox/bin/toolbox $(BIN_DIR)/toolbox

host:
	@$(MAKE) -C cmd/toolbox build

check-tests:
	@for subsystem in $(SUBSYSTEMS); do \
		if [ -z "$$(find subsystems/$$subsystem -maxdepth 1 -name '*_test.go' -print -quit)" ]; then \
			echo "subsystem $$subsystem has no unit tests" >&2; \
			exit 1; \
		fi; \
	done

test: check-tests
	@for subsystem in $(SUBSYSTEMS); do \
		$(MAKE) -C subsystems/$$subsystem test; \
	done
	@$(MAKE) -C cmd/toolbox test
	@go test -count=1 ./...

test-short: check-tests
	@for subsystem in $(SUBSYSTEMS); do \
		$(MAKE) -C subsystems/$$subsystem test-short; \
	done
	@$(MAKE) -C cmd/toolbox test
	@go test -short -count=1 ./...

fmt:
	@gofmt -w $$(find pkg -name '*.go' -not -name '*.pb.go' -not -name '*.connect.go')
	@for subsystem in $(SUBSYSTEMS); do $(MAKE) -C subsystems/$$subsystem fmt; done
	@$(MAKE) -C cmd/toolbox fmt

vet:
	@for subsystem in $(SUBSYSTEMS); do $(MAKE) -C subsystems/$$subsystem vet; done
	@$(MAKE) -C cmd/toolbox vet
	@go vet ./...

proto-tools:
	@for subsystem in $(SUBSYSTEMS); do $(MAKE) -C subsystems/$$subsystem proto-tools; done

clean:
	@for subsystem in $(SUBSYSTEMS); do $(MAKE) -C subsystems/$$subsystem clean; done
	@$(MAKE) -C cmd/toolbox clean
	rm -rf $(BIN_DIR)
