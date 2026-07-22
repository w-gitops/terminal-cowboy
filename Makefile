BINARY := terminal-cowboy
DIST   := dist

.PHONY: build run fmt vet test clean cross

build:
	go build -o $(BINARY) .

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

## cross: build release binaries for the supported hosts.
cross:
	mkdir -p $(DIST)
	GOOS=darwin  GOARCH=arm64 go build -o $(DIST)/$(BINARY)-darwin-arm64  .
	GOOS=linux   GOARCH=amd64 go build -o $(DIST)/$(BINARY)-linux-amd64   .
	GOOS=linux   GOARCH=arm64 go build -o $(DIST)/$(BINARY)-linux-arm64   .
	@echo "built:" && ls -1 $(DIST)
