# Define Go command
GO ?= go

# Run unit tests
test:
	$(GO) test ./... -v

# Run tests with coverage
test-cover:
	$(GO) test ./... -cover -coverprofile=coverage.out


.PHONY: test test-cover coverage coverage-html