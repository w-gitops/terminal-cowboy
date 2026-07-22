BINARY := terminal-cowboy
DIST   := dist
PREFIX ?= $(HOME)/.local

# Version: YYYY-MM-DD.HHMM-<shortsha>[-dirty], computed at build time.
SHA     := $(shell git rev-parse --short=7 HEAD 2>/dev/null || echo nogit)
DIRTY   := $(shell git diff --quiet 2>/dev/null || echo -dirty)
VERSION := $(shell date +%Y-%m-%d.%H%M)-$(SHA)$(DIRTY)
LDFLAGS := -X 'main.Version=$(VERSION)'

.PHONY: build run fmt vet test clean cross install uninstall version

version:
	@echo $(VERSION)

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

run: build
	./$(BINARY) --open

fmt:
	gofmt -w .

vet:
	go vet ./...

test:
	go test ./...

clean:
	rm -rf $(BINARY) $(DIST)

## install: build and install as terminal-cowboy with tcow / tcowboy launchers.
install: build
	install -d $(PREFIX)/bin
	install -m 0755 $(BINARY) $(PREFIX)/bin/$(BINARY)
	ln -sf $(BINARY) $(PREFIX)/bin/tcow
	ln -sf $(BINARY) $(PREFIX)/bin/tcowboy
	@echo "installed: $(PREFIX)/bin/{$(BINARY),tcow,tcowboy}"
	@echo "run:       tcow --open"

uninstall:
	rm -f $(PREFIX)/bin/$(BINARY) $(PREFIX)/bin/tcow $(PREFIX)/bin/tcowboy
	@echo "removed tcow / tcowboy / $(BINARY) from $(PREFIX)/bin"

## cross: build release binaries for the supported hosts.
cross:
	mkdir -p $(DIST)
	GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-darwin-arm64  .
	GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-amd64   .
	GOOS=linux   GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-arm64   .
	@echo "built $(VERSION):" && ls -1 $(DIST)
