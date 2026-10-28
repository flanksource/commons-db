ROOT := ../..
BIN_DIR ?= $(ROOT)/.bin
PNPM ?= pnpm

.PHONY: build lint test ui-build ui-install

build: ui-build
	mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/query .

lint: ui-build
	go vet ./...
	$(PNPM) --dir www typecheck

test: ui-build
	go test ./...
	$(PNPM) --dir www test

ui-build:
	$(PNPM) --dir www build

ui-install:
	$(PNPM) --dir www install --frozen-lockfile
