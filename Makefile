# pairmux — build/test automation. Zero external dependencies; stdlib + tmux.

BIN     := bin/pairmux
PKG     := ./cmd/pairmux
VERSION ?= 0.1.0-dev
LDFLAGS := -X github.com/treeleaves30760/pairmux/internal/version.Version=$(VERSION)

.PHONY: all build test vet integration fmt clean release-check installsh-check \
	pypi-test local-wheel

# Default: the green tree (vet + unit tests + binary). Integration is separate
# because it needs a real tmux.
all: vet test build

build:
	@mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o $(BIN) $(PKG)

test:
	go test ./...

vet:
	go vet ./...

# Runs the tmux-backed integration suite (build tag `integration`). Skips
# cleanly when tmux is absent.
integration: build
	go test -tags integration -count=1 ./test/...

fmt:
	gofmt -w .

# Validate the GoReleaser config and build the complete archive/package set
# without publishing. Needs goreleaser (brew install goreleaser).
release-check:
	goreleaser check
	goreleaser release --snapshot --clean --skip=publish

# Lint the POSIX install script. Needs shellcheck (brew install shellcheck).
installsh-check:
	shellcheck install.sh

# Packaging unit tests: release wheel builder, release verifier, and the
# source-install backend. With go and git on PATH this also builds and
# pip-installs this checkout into a temporary venv (PAIRMUX_TEST_SOURCE_INSTALL=0
# skips that).
pypi-test:
	python3 -m unittest discover -s packaging/pypi -p 'test_*.py'

# Host-only development wheel from this checkout through the in-tree PEP 517
# backend (packaging/pypi/README.md#install-from-a-source-checkout). Never a
# release artifact: release wheels come only from the tag workflow.
local-wheel:
	python3 -m pip wheel --no-deps --no-index -w dist/local-wheel .

clean:
	rm -rf bin dist
