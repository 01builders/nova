# Define Go command
GO ?= go

# Run unit tests
test:
	$(GO) test ./... -v

# Run tests with coverage
test-coverage:
	$(GO) tool cover -func=coverage.out

# lint code with golangci-lint
lint-fix:
	golangci-lint run --fix

.PHONY: test test-cover lint-fix