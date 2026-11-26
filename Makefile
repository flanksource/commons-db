GO_MODULES := . recordstore tracing cmd/query

.PHONY: build install tidy tidy-check

build install:
	$(MAKE) -C cmd/query $@

tidy:
	@for dir in $(GO_MODULES); do \
		echo "go mod tidy: $$dir"; \
		(cd $$dir && go mod tidy) || exit 1; \
	done

tidy-check:
	@for dir in $(GO_MODULES); do \
		echo "go mod tidy -diff: $$dir"; \
		(cd $$dir && GOWORK=off go mod tidy -diff) || exit 1; \
	done
