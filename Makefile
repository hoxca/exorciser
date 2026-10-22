MODULE   = $(shell env GO111MODULE=on $(GO) list -m)
DATE    ?= $(shell date +%FT%T%z)
VERSION ?= $(shell git describe --tags --always --match=v* 2> /dev/null || \
			cat $(CURDIR)/.version 2> /dev/null || echo v0)
PKGS     = $(or $(PKG),$(shell env GO111MODULE=on $(GO) list ./...))
TESTPKGS = $(shell env GO111MODULE=on $(GO) list -f \
			'{{ if or .TestGoFiles .XTestGoFiles }}{{ .ImportPath }}{{ end }}' \
			$(PKGS))
BIN      = $(CURDIR)/bin
BINNAME  = $(notdir $(MODULE))

GO      = go
TIMEOUT = 300
V = 0
Q = $(if $(filter 1,$V),,@)
M = $(shell printf "\033[34;1m▶\033[0m")

export GO111MODULE=on

.PHONY: all
all: fmt lint | $(BIN) ; $(info $(M) building executable…) @ ## Build program binary
	$Q $(GO) build \
		-tags release \
		-ldflags '-X $(MODULE)/cmd.Version=$(VERSION) -X $(MODULE)/cmd.BuildDate=$(DATE)' \
		-o $(BIN)/$(BINNAME) .

# Tools

$(BIN):
	@mkdir -p $@
$(BIN)/%: | $(BIN) ; $(info $(M) building $(PACKAGE)…)
	$Q tmp=$$(mktemp -d); \
	   env GO111MODULE=on GOPATH=$$tmp GOBIN=$(BIN) $(GO) get $(PACKAGE) \
		|| ret=$$?; \
	   rm -rf $$tmp ; exit $$ret

GOLINT = golangci-lint

GOTEST = gotestsum

# Tests

TEST_TARGETS := test-default test-bench test-race test-short
.PHONY: $(TEST_TARGETS) test-xml check test tests
test-bench:   ARGS=-run=__absolutelynothing__ -bench=. ## Run benchmarks
test-short:   ARGS=-short        ## Run only short tests
test-race:    ARGS=-race         ## Run tests with race detector
$(TEST_TARGETS): NAME=$(MAKECMDGOALS:test-%=%)
$(TEST_TARGETS): test
check test tests: fmt lint ; $(info $(M) running $(NAME:%=% )tests…) @ ## Run tests
	$Q $(GOTEST) -- -timeout $(TIMEOUT)s $(ARGS) $(TESTPKGS)

.PHONY: test-integration
test-integration: fmt lint ; $(info $(M) running $(NAME:%=% )tests…) @ ## Run tests
	$Q $(GOTEST) -f testdox -- -tags=integration -timeout $(TIMEOUT)s $(ARGS) $(TESTPKGS)

.PHONY: test-slower
test-slower: fmt lint ; $(info $(M) running $(NAME:%=% )tests…) @ ## Run tests
	$Q $(GO) test -json $(TESTPKGS) | $(GOTEST) tool slowest --skip-stmt "testing.Short" --threshold 200ms

.PHONY: test-verbose
test-verbose: fmt lint ; $(info $(M) running $(NAME:%=% )tests…) @ ## Run tests
	$Q $(GOTEST) -f testdox -- -timeout $(TIMEOUT)s $(ARGS) $(TESTPKGS)

COVERAGE_MODE    = atomic
COVERAGE_PROFILE = $(COVERAGE_DIR)/coverage.out
COVERAGE_DIR := $(CURDIR)/test
.PHONY: test-coverage
test-coverage: fmt lint; $(info $(M) running coverage tests…) @ ## Run coverage tests
	$Q mkdir -p $(COVERAGE_DIR)
	$Q $(GOTEST) -- \
		-coverpkg=$$($(GO) list -f '{{ join .Deps "\n" }}' $(TESTPKGS) | \
					grep '^$(MODULE)/' | \
					tr '\n' ',' | sed 's/,$$//') \
		-covermode=$(COVERAGE_MODE) \
		-coverprofile="$(COVERAGE_PROFILE)" $(TESTPKGS)

.PHONY: lint
lint: ; $(info $(M) running golint…  $Q $(GOLINT) $(PKGS)) @ ## Run golint
	$Q $(GOLINT) run ./...

.PHONY: fmt
fmt: ; $(info $(M) running gofmt…) @ ## Run gofmt on all source files
	$Q $(GO) fmt $(PKGS)

.PHONY: build
build: | $(BIN) ; $(info $(M) building executable…) @ ## Build program binary
	$Q $(GO) build \
		-tags release \
		-ldflags '-X $(MODULE)/cmd.Version=$(VERSION) -X $(MODULE)/cmd.BuildDate=$(DATE)' \
		-o $(BIN)/$(BINNAME) .

.PHONY: detect
detect: build ; $(info $(M) detecting diffuse artefacts…) @ ## Detect artefacts against a reference frame
	$Q $(BIN)/$(BINNAME) detect $(DETECT_ARGS)

.PHONY: scan
scan: build ; $(info $(M) scanning a stacked difference…) @ ## Catalogue artefacts in a stacked difference
	$Q $(BIN)/$(BINNAME) scan $(SCAN_ARGS)

.PHONY: winbuild
winbuild: | $(BIN) ; $(info $(M) building executable…) @ ## Build program binary
	$Q GOOS=windows GOARCH=amd64 $(GO) build \
		-tags release \
		-ldflags '-X $(MODULE)/cmd.Version=$(VERSION) -X $(MODULE)/cmd.BuildDate=$(DATE)' \
		-o $(BIN)/$(BINNAME).exe

# Misc

.PHONY: clean
clean: ; $(info $(M) cleaning…)	@ ## Cleanup everything
	@rm -rf $(BIN)
	@rm -rf test/tests.* test/coverage.*

.PHONY: help
help:
	@grep -hE '^[ a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-17s\033[0m %s\n", $$1, $$2}'

.PHONY: version
version:
	@echo $(VERSION)
