# Build, lint and test the tracing/sqltrace module: SQL Server Extended Events
# captures that commit their rows to a recordstore backend.
.PHONY: build lint test

build:
	go build ./...

lint:
	go vet ./...

test:
	go test ./...
