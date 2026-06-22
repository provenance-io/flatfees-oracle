IMAGE ?= flatfees-oracle
TAG   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
BIN   ?= bin/flatfees-oracle

.PHONY: build compile test vet lint tidy run docker clean all

all: vet test build

# build produces the runnable binary at $(BIN).
build:
	go build -o $(BIN) ./cmd/oracle

# compile type-checks every package without producing an artifact (fast CI check).
compile:
	go build ./...

clean:
	rm -rf bin

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
