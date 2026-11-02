.PHONY: all fmt vet test build

all: fmt vet test build

fmt:
	@files="$$(gofmt -l .)" || exit 1; test -z "$$files" || { printf '%s\n' "$$files"; exit 1; }

vet:
	go vet ./...

test:
	# The e2e package needs more than the default 10 minutes on a slow CI machine.
	go test -race -timeout 30m ./...

build:
	go build ./...
	mkdir -p bin
	CGO_ENABLED=0 go build -o bin/amg ./cmd/amg
	go build -o bin/amgd ./cmd/amgd
