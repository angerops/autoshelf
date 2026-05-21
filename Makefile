BINARY := autoshelf
PKG    := github.com/angerops/autoshelf

# VERSION resolves to a git tag when present (e.g. v0.1.0), falling back to
# a short SHA + -dirty when local changes exist, and finally "dev" if there's
# no git history at all. Overridable on the command line: make build VERSION=v0.2.0
VERSION ?= $(shell git describe --tags --dirty --always 2>/dev/null || echo dev)

LDFLAGS := -s -w -X $(PKG)/cmd.version=$(VERSION)

.PHONY: all build tidy test vet fmt run-once clean install version

all: tidy vet test build

tidy:
	go mod tidy

vet:
	go vet ./...

fmt:
	gofmt -s -w .

test:
	go test ./... -count=1

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) .

# Cross-builds for distribution. Same ldflags so --version is correct
# regardless of which arch you grabbed.
build-linux-amd64:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY)-linux-amd64 .

build-darwin-arm64:
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY)-darwin-arm64 .

build-darwin-amd64:
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY)-darwin-amd64 .

build-linux-arm64:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY)-linux-arm64 .

install: build
	install -m 0755 $(BINARY) $(HOME)/.local/bin/$(BINARY)

run-once: build
	./$(BINARY) once -c autoshelf.example.yaml --dry-run -v

# Print what VERSION resolves to. Useful for confirming the tag was picked up.
version:
	@echo $(VERSION)

clean:
	rm -f $(BINARY) $(BINARY)-linux-amd64 $(BINARY)-linux-arm64 $(BINARY)-darwin-arm64 $(BINARY)-darwin-amd64
