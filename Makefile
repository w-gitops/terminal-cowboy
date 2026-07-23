BINARY  := terminal-cowboy
DIST    := dist
PREFIX  ?= $(HOME)/.local
UNITDIR := $(HOME)/.config/systemd/user
UNIT    := terminal-cowboy.service

# Version: YYYY-MM-DD.HHMM-<shortsha>[-dirty], computed at build time.
SHA     := $(shell git rev-parse --short=7 HEAD 2>/dev/null || echo nogit)
DIRTY   := $(shell git diff --quiet 2>/dev/null || echo -dirty)
VERSION := $(shell date +%Y-%m-%d.%H%M)-$(SHA)$(DIRTY)
LDFLAGS := -X 'main.Version=$(VERSION)'

.PHONY: build run fmt vet test clean cross install uninstall version \
        service-install service-restart service-status service-logs service-uninstall

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
	@$(MAKE) --no-print-directory service-restart

uninstall:
	rm -f $(PREFIX)/bin/$(BINARY) $(PREFIX)/bin/tcow $(PREFIX)/bin/tcowboy
	@echo "removed tcow / tcowboy / $(BINARY) from $(PREFIX)/bin"

## service-install: install + enable the systemd user service (Linux).
service-install:
	install -d $(UNITDIR)
	install -m 0644 packaging/$(UNIT) $(UNITDIR)/$(UNIT)
	systemctl --user daemon-reload
	systemctl --user enable --now $(UNIT)
	@echo "enabled $(UNIT) — starts on graphical login, serving http://127.0.0.1:8787/"

## service-restart: restart the running service if it's installed (best-effort;
## a no-op on machines without the user service, so `make install` is portable).
service-restart:
	@if command -v systemctl >/dev/null 2>&1 && [ -f "$(UNITDIR)/$(UNIT)" ]; then \
		systemctl --user restart $(UNIT) && echo "↻ restarted $(UNIT)"; \
	else \
		echo "run:       tcow --open   ('make service-install' to run it as a service)"; \
	fi

service-status:
	systemctl --user status $(UNIT)

service-logs:
	journalctl --user -u $(UNIT) -f

service-uninstall:
	-systemctl --user disable --now $(UNIT)
	rm -f $(UNITDIR)/$(UNIT)
	systemctl --user daemon-reload
	@echo "removed $(UNIT)"

## cross: build release binaries for the supported hosts.
cross:
	mkdir -p $(DIST)
	GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-darwin-arm64  .
	GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-amd64   .
	GOOS=linux   GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-arm64   .
	@echo "built $(VERSION):" && ls -1 $(DIST)
