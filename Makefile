GO_MODULES := . cmd/query

.PHONY: tidy
tidy:
	@for dir in $(GO_MODULES); do \
		echo "go mod tidy: $$dir"; \
		(cd $$dir && go mod tidy) || exit 1; \
	done
