# Build, lint and test the tracing/traces module: trace plugins whose
# captures run as managed sessions and commit their records to a record store.
.PHONY: build lint test

build:
	go build ./...

lint:
	go vet ./...

test:
	go test ./...
