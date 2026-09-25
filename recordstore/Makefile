# Build, lint and test the recordstore module: the record-stream store,
# its backends, and the result types served over them.
.PHONY: build lint test

build:
	go build ./...

lint:
	go vet ./...

test:
	go test ./...
