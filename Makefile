IMAGE ?= flatfees-oracle
TAG   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)

.PHONY: build test vet lint tidy run docker all

all: vet test build

build:
	go build ./...

test:
	go test ./... -count=1

test-race:
	go test ./... -race -count=1

vet:
	go vet ./...

lint:
	golangci-lint run

tidy:
	go mod tidy

# Local dry run: computes and logs the factor without broadcasting.
run:
	DRY_RUN=true ORACLE_ENV=local go run ./cmd/oracle

docker:
	docker build -t $(IMAGE):$(TAG) .
