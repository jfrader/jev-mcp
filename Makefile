BINARY := bin/jev-mcp

.PHONY: build install test clean

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BINARY) .

install: build
	install -m 0755 $(BINARY) $(HOME)/.local/bin/jev-mcp

test:
	go test ./...

clean:
	rm -rf bin
