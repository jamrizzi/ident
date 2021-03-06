# Bivac v2.0.0 (https://camptocamp.github.io/bivac)
# Copyright (c) 2019 Camptocamp
# Licensed under Apache-2.0 (https://raw.githubusercontent.com/camptocamp/bivac/master/LICENSE)
# Modifications copyright (c) 2019 Jam Risser <jam@codejam.ninja>

DEPS = $(wildcard */*/*/*.go)
VERSION = $(shell git describe --always --dirty)
COMMIT_SHA1 = $(shell git rev-parse HEAD)
BUILD_DATE = $(shell date +%Y-%m-%d)

all: lint vet test volback

.PHONY: publish
publish:
	@docker-compose push

.PHONY: build
build: volback
	@cd docker && make build

.PHONY: push
push: build
	@cd docker && make push

volback: main.go $(DEPS)
	GO111MODULE=on CGO_ENABLED=0 GOOS=linux \
	  go build -mod=vendor -a \
		  -ldflags="-s -X main.version=$(VERSION) -X main.buildDate=$(BUILD_DATE) -X main.commitSha1=$(COMMIT_SHA1)" \
	    -installsuffix cgo -o $@ $<
	strip $@

lint:
	@go get -u -v golang.org/x/lint/golint
	@for file in $$(go list ./... | grep -v '_workspace/' | grep -v 'vendor'); do \
		export output="$$(golint $${file} | grep -v 'type name will be used as docker.DockerInfo')"; \
		[ -n "$${output}" ] && echo "$${output}" && export status=1; \
	done; \
	exit $${status:-0}

vet: main.go
	go vet $<

clean:
	git clean -fXd -e \!vendor -e \!vendor/**/*

test:
	go test -cover -coverprofile=coverage -v ./...

vendor:
	go mod vendor

.PHONY: all vendor lint vet clean test
