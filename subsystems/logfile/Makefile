SHELL := /bin/sh
export PATH := $(HOME)/go/bin:$(PATH)

NAME := logfile
MODULE := github.com/Manu343726/toolbox/subsystems/logfile

# This subsystem has no proto, no command and no binary, and that is the point rather than an
# omission: a logging provider has no operations to address, so there is no contract to
# generate and no process to run. It is composed into a deployment in process, through the
# log.Provider it exports, and declared as a provider through Providers so a deployment can
# discover it the same way it discovers a parser or an adapter.
#
# The targets below are therefore the whole set. A subsystem that does produce a binary gets
# the build, proto and clean targets the others have.

.PHONY: all build test test-short fmt vet clean

all: build

build:
	go build ./...

test:
	go test -count=1 ./...

test-short:
	go test -short -count=1 ./...

fmt:
	gofmt -l -w .

vet:
	go vet ./...

clean:
	@:
