NAME    = efs-provisioner

# This version number should be incremented following semantic versioning for every new change before it is merged to master.
VERSION = 1.0.0

PACKAGE     = github.com/BidPal/$(NAME)
DATE       ?= $(shell date +%FT%T%z)
BRANCH      = $(shell git rev-parse --abbrev-ref HEAD)
BRANCHCLEAN = $(shell echo $(BRANCH) | tr -cd '[A-Za-z0-9\\-\\n]' | tr '[:upper:]' '[:lower:]')
REV         = $(shell git rev-parse --short HEAD)
BIN         = $(GOPATH)/bin
BLDBIN      = $(CURDIR)/bin/$(NAME)
UNAME_S     = $(shell uname -s)

ifeq ($(UNAME_S),Darwin)
	OS = darwin
else
	OS = linux
endif

SOURCES := $(shell find $(CURDIR) -name '*.go')

BASE = $(GOPATH)/src/$(PACKAGE)
PKGS = $(or $(PKG),$(shell cd $(BASE) && env GOPATH=$(GOPATH) $(GO) list ./... | grep -v "^$(PACKAGE)/vendor/"))

TESTDIR       = $(CURDIR)/test
TESTPKGS      = $(shell env GOPATH=$(GOPATH) $(GO) list -f '{{ if or .TestGoFiles .XTestGoFiles }}{{ .ImportPath }}{{ end }}' $(PKGS) | grep -v "^$(PACKAGE)/test-")
TESTINTPKGS   = $(shell env GOPATH=$(GOPATH) $(GO) list -f '{{ if or .TestGoFiles .XTestGoFiles }}{{ .ImportPath }}{{ end }}' $(PKGS) | grep "^$(PACKAGE)/test-")
TESTSMOKEPKGS = $(shell env GOPATH=$(GOPATH) $(GO) list -f '{{ if or .TestGoFiles .XTestGoFiles }}{{ .ImportPath }}{{ end }}' $(PKGS) | grep "^$(PACKAGE)/test-smoke")
COVERAGE_DIR  = $(CURDIR)/test/coverage

RELNAME := $(NAME)-linux-amd64
RELDIR  := $(CURDIR)/out
RELBIN  := $(addprefix $(RELDIR)/,$(RELNAME))
RELTMP   = $(subst -, ,$(notdir $@))
RELOS    = $(word 3, $(RELTMP))
RELARCH  = $(word 4, $(RELTMP))

GO      = go
GODOC   = godoc
GOFMT   = gofmt
TIMEOUT = 15

V = 0
Q = $(if $(filter 1,$V),,@)
M = $(shell printf "\033[34;1m▶\033[0m")

.PHONY: build all
build all: fmt vet | $(BASE) $(BLDBIN) ; @ ## Build program binary

# Tools
GOCOVMERGE = $(BIN)/gocovmerge
$(BIN)/gocovmerge: | $(BASE) ; $(info $(M) building gocovmerge…)
	$Q go get github.com/wadey/gocovmerge

GOCOV = $(BIN)/gocov
$(BIN)/gocov: | $(BASE) ; $(info $(M) building gocov…)
	$Q go get github.com/axw/gocov/...

GOCOVXML = $(BIN)/gocov-xml
$(BIN)/gocov-xml: | $(BASE) ; $(info $(M) building gocov-xml…)
	$Q go get github.com/AlekSi/gocov-xml

GO2XUNIT = $(BIN)/go2xunit
$(BIN)/go2xunit: | $(BASE) ; $(info $(M) building go2xunit…)
	$Q go get github.com/tebeka/go2xunit

GINKGO = $(BIN)/ginkgo
$(BIN)/ginkgo: | $(BASE) ; $(info $(M) building ginkgo…)
	$Q go get github.com/onsi/ginkgo/ginkgo

.PHONY: tools
tools: | $(GOCOVMERGE) $(GOCOV) $(GOCOVXML) $(GO2XUNIT) $(GINKGO)

# Build

$(BLDBIN): $(SOURCES) | $(BASE) ; $(info $(M) building executable…)
	$Q cd $(BASE) && $(GO) build \
		-tags release \
		-ldflags '-X $(PACKAGE)/cmd.Version=$(VERSION)-dev -X $(PACKAGE)/cmd.BuildDate=$(DATE) -X $(PACKAGE)/cmd.Commit=$(REV)' \
		-o $(BLDBIN) main.go

$(BASE): ; $(info $(M) setting GOPATH…)
	@mkdir -p $(dir $@)

$(RELDIR): ; $(info $(M) creating release directory: $(RELDIR))
	@mkdir -p $@

.PHONY: version
version: | $(BASE) $(RELDIR)
	$(eval SEMVER := $(VERSION)$(if $(filter $(BRANCH),master),,-$(BRANCHCLEAN)-$(REV)$(if $(BUILD_NUMBER),-$(BUILD_NUMBER),)))
	$(info $(M) Determined version to be $(SEMVER)…)
	$Q git rev-parse --verify -q v$(VERSION) > /dev/null && { echo "Version $(VERSION) already tagged; increase VERSION in Makefile"; exit 1; } || true
	$Q echo -n $(SEMVER) > $(RELDIR)/VERSION

.PHONY: rel release
rel release: version | $(BASE) $(RELBIN) ; @ ## Build release binary

.PHONY: install
install: release | $(BASE) $(RELBIN) ; $(info $(M) installing to $$GOPATH/bin…) @ ## Install release binary in go path
	$Q cp $(RELBIN) $(BIN)/$(NAME)

$(RELBIN): $(SOURCES) | $(BASE) $(RELDIR) ; $(info $(M) building release executable: $(@)…)
	$Q cd $(BASE) && CGO_ENABLED=0 GOOS=$(RELOS) GOARCH=$(RELARCH) $(GO) build \
		-tags release \
		-installsuffix cgo \
		-ldflags '-X $(PACKAGE)/cmd.Version=$(SEMVER) -X $(PACKAGE)/cmd.BuildDate=$(DATE) -X $(PACKAGE)/cmd.Commit=$(REV)' \
		-o $(@) main.go

# Tests

$(TESTDIR): ; $(info $(M) creating test results directory: $(TESTDIR))
	@mkdir -p $@

.PHONY: test-xml test tests

test tests: fmt vet | $(BASE) $(TESTDIR) ; $(info $(M) running $(NAME:%=% )tests…) @ ## Run tests
	$Q cd $(BASE) && $(GO) test -tags test -timeout $(TIMEOUT)s $(ARGS) $(TESTPKGS)

test-xml: fmt vet | $(BASE) $(GO2XUNIT) $(TESTDIR) ; $(info $(M) running $(NAME:%=% )tests…) @ ## Run tests with xUnit output
	$Q cd $(BASE) && 2>&1 $(GO) test -tags test -timeout 20s -v $(TESTPKGS) | tee test/tests.output
	$(GO2XUNIT) -fail -input test/tests.output -output test/tests.xml

COVERAGE_MODE = atomic
COVERAGE_PROFILE = $(COVERAGE_DIR)/profile.out
COVERAGE_XML = $(COVERAGE_DIR)/coverage.xml
COVERAGE_HTML = $(COVERAGE_DIR)/index.html
.PHONY: test-coverage test-coverage-tools
test-coverage-tools: | $(GOCOVMERGE) $(GOCOV) $(GOCOVXML)
test-coverage: fmt vet test-coverage-tools | $(BASE) ; $(info $(M) running coverage tests…) @ ## Run coverage tests
	$Q mkdir -p $(COVERAGE_DIR)/coverage
	$Q cd $(BASE) && for pkg in $(TESTPKGS); do \
		$(GO) test \
			-count=1 \
			-tags test \
			-covermode=$(COVERAGE_MODE) \
			-coverprofile="$(COVERAGE_DIR)/coverage/`echo $$pkg | tr "/" "-"`.cover" $$pkg ;\
	 done
	$Q $(GOCOVMERGE) $(COVERAGE_DIR)/coverage/*.cover > $(COVERAGE_PROFILE)
	$Q $(GO) tool cover -html=$(COVERAGE_PROFILE) -o $(COVERAGE_HTML)
	$Q $(GOCOV) convert $(COVERAGE_PROFILE) | $(GOCOVXML) > $(COVERAGE_XML)

.PHONY: vet
vet: | $(BASE) ; $(info $(M) running go vet…) @ ## Run go vet
	go vet ./...

.PHONY: fmt
fmt: ; $(info $(M) running gofmt…) @ ## Run gofmt on all source files
	@ret=0 && for d in $$($(GO) list -f '{{.Dir}}' ./... | grep -v /vendor/); do \
		$(GOFMT) -l -w $$d/*.go || ret=$$? ; \
	 done ; exit $$ret

# Packaging and Publishing

.PHONY: package
package: release ; $(info $(M) packaging project…) @ ## Package project

.PHONY: publish
publish: version
ifeq ($(BRANCH),master)
	git tag -a v$(SEMVER) -m "Release $(SEMVER)"
	git push origin v$(SEMVER)
else
	$(info $(M) not on master, skipping tag…)
endif

# Misc

.PHONY: clean
clean: ; $(info $(M) cleaning…)	@ ## Cleanup everything
	@rm -rf bin
	@rm -rf test/tests.* test/coverage.*
	@rm -rf $(RELDIR)
	@rm -rf $(TESTDIR)

.PHONY: help
help:
	@grep -E '^[ a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}'

# Dev

.PHONY: test-watch
test-watch:
	$(GINKGO) watch -r -tags test -cover
