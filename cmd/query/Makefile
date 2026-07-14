ROOT := ../..
BIN_DIR ?= $(ROOT)/.bin

.PHONY: build lint test ui-build ui-install

build: ui-build
	mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/query .

lint:
	go vet ./...
	pnpm --dir www typecheck

test:
	go test ./...
	pnpm --dir www test

ui-build:
	pnpm --dir www build

ui-install:
	pnpm --dir www install --frozen-lockfile
