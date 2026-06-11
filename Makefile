BINARY := ephemera
VERSION ?= dev
GOFLAGS := -trimpath
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build run test fmt vet tidy clean install static

all: build

build:
	mkdir -p bin
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/ephemera

static:
	mkdir -p bin
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS) -extldflags=-static" -o bin/$(BINARY) ./cmd/ephemera

run:
	go run ./cmd/ephemera

test:
	go test ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

vet:
	go vet ./...

tidy:
	go mod tidy

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/ephemera

clean:
	rm -rf bin dist
