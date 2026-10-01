GO_MODULES := . recordstore tracing/sqltrace cmd/query

.PHONY: tidy tidy-check
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
