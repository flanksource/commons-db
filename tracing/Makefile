# Build, lint and test the tracing module: trace plugins whose captures run as
# managed sessions and commit their records to a record store, their kinds,
# and the SQL Server capture packages under sqlserver/.
.PHONY: build lint test

build:
	go build ./...

lint:
	go vet ./...

test:
	go test ./...
