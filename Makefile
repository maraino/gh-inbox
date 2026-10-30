BINARY := gh-inbox

# The race detector needs cgo; run "make test RACE=" where there is no C compiler.
RACE ?= -race

.PHONY: all
all: lint test build

.PHONY: build
build: ## Build the gh-inbox binary
	go build -o $(BINARY) .

.PHONY: test
test: ## Run the tests (with the race detector unless RACE= is given)
	CGO_ENABLED=$(if $(RACE),1,0) go test $(RACE) ./...

.PHONY: lint
lint: ## Run golangci-lint
	go tool golangci-lint run

.PHONY: fmt
fmt: ## Format the code with golangci-lint's formatters
	go tool golangci-lint fmt

.PHONY: govulncheck
govulncheck: ## Check for known vulnerabilities
	go tool govulncheck ./...

.PHONY: tidy
tidy: ## Tidy go.mod and go.sum
	go mod tidy

.PHONY: install
install: build ## Install the local build as a gh extension
	gh extension install .

.PHONY: demo
demo: ## Record demo/demo.gif from a made-up inbox (needs Python 3 with Pillow)
	demo/record.sh

.PHONY: clean
clean: ## Remove build artifacts
	rm -f $(BINARY)

.PHONY: help
help: ## Show this help
	@grep -E '^[a-z]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'
