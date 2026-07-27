SHELL := /bin/bash

MODULE  := github.com/sequencestream/evalexec
BINARY  := bin/evalexec

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X '$(MODULE)/version.Version=$(VERSION)' \
	-X '$(MODULE)/version.Commit=$(COMMIT)' \
	-X '$(MODULE)/version.Date=$(DATE)'

# Direct non-stdlib dependencies allowed in go.mod. dev-plan §1.5 caps this at
# aimodel plus one JSON Schema library, and that budget is now spent.
ALLOWED_DEPS := github.com/vogo/aimodel github.com/santhosh-tekuri/jsonschema/v6

.PHONY: all build test test-e2e test-protocol test-consumer lint lint-terms lint-boundary \
	lint-secrets check-deps apidiff fmt tidy clean

all: build test test-protocol test-consumer lint

build:
	@mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/evalexec

test:
	go test -race ./...

# Real-model end-to-end tests, which live in e2e/ behind a build tag so they
# cannot run by accident: they need credentials and they cost money. Each test
# skips itself when the environment is unset, so this passes on a machine with
# no credentials rather than failing in a way that looks like a defect.
#
# Needs OPENAI_BASE_URL, OPENAI_API_KEY and OPENAI_MODEL.
test-e2e:
	go test -tags e2e -count=1 -v ./e2e/...

# Acceptance criterion 21: the protocol must be checkable without Go. The
# self-test runs first, because a checker that cannot fail proves nothing about
# the fixtures it just accepted.
test-protocol:
	python3 contract/verify_fixtures.py --self-test
	python3 contract/verify_fixtures.py fixtures/data

# The downstream smoke test. Its own module, because an examples/ directory
# inside this one would only prove the code compiles — not that everything a
# downstream program needs is exported.
test-consumer:
	cd examples/consumer && go run .

# The golangci-lint call must not be chained with `|| echo`: a lint failure
# would then be swallowed as "not installed" and the target would still
# succeed. Absence and failure are decided separately.
lint: lint-terms lint-boundary check-deps lint-secrets
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "lint: golangci-lint not installed, skipping"; \
	fi

# dev-plan §1.5 terminology boundary: the scoring component is always a
# "Grader". The word root "evaluator" must not appear in code, fixtures or
# user-facing docs, because getting it wrong on a public API makes the rename
# a breaking change.
#
# doc/ and this Makefile are exempt because they are where the ban is stated:
# the design docs and the guard itself have to name the banned word. Every
# other path — Go sources, fixtures, README — is in scope.
lint-terms:
	@hits=$$(git ls-files --cached --others --exclude-standard | grep -vE '^doc/|^Makefile$$' | while read -r f; do [ -f "$$f" ] && echo "$$f"; done | xargs grep -InE 'evaluator' 2>/dev/null); \
	if [ -n "$$hits" ]; then \
		echo "lint-terms: banned word root 'evaluator' found (use 'grader'):"; \
		echo "$$hits"; \
		exit 1; \
	fi; \
	echo "lint-terms: ok"

# dev-plan §1.5 / §7: evalexec ships as a library too, so *library* code that
# exits the process, grabs signals, or writes to os.Stderr would misbehave
# inside a host process.
#
# Standalone programs are exempt, because that is precisely where these belong:
# cmd/ is the binary, contract/ holds the reference external components, and
# examples/ holds the downstream smoke test — the last two are separate
# processes, and examples/consumer is a separate module besides. Test files are
# exempt too (M5 drives a real subprocess to exercise SIGINT).
#
# This is the cheap pre-check, available without golangci-lint. The
# authoritative check is the forbidigo linter in .golangci.yml, which works on
# the AST. Comments are stripped first because a doc comment explaining the
# rule is not a violation of it — that false positive would pressure us to
# leave the rule undocumented, which is exactly backwards. Stripping is
# line-based, so a `//` inside a string literal truncates the rest of that
# line; forbidigo is what closes that gap.
lint-boundary:
	@files=$$(git ls-files --cached --others --exclude-standard '*.go' | grep -vE '^cmd/|^contract/|^examples/' | grep -v '_test\.go$$' | while read -r f; do [ -f "$$f" ] && echo "$$f"; done); \
	if [ -z "$$files" ]; then echo "lint-boundary: ok (no files)"; exit 0; fi; \
	hits=$$(for f in $$files; do \
		sed 's|//.*||' "$$f" | grep -nE 'os\.Exit|signal\.Notify|os\.Stderr' | sed "s|^|$$f:|"; \
	done); \
	if [ -n "$$hits" ]; then \
		echo "lint-boundary: os.Exit / signal.Notify / os.Stderr are only allowed under cmd/:"; \
		echo "$$hits"; \
		exit 1; \
	fi; \
	echo "lint-boundary: ok"

# dev-plan §1.5: assert no secret ever reaches a result directory.
#
# It runs as a Go test rather than a shell script because it has to produce a
# result directory first — the scan is over real output, not over the sources.
# The companion test proves the scanner can fail: a detector that has never
# detected anything is indistinguishable from a broken one.
lint-secrets:
	go test -count=1 -run 'TestNoSecretReachesTheResultDirectory|TestLeakScannerActuallyFires' -v .

# The consumer example has its own go.mod and is checked by its own CI step;
# this looks only at the main module.
check-deps:
	@deps=$$(go list -m -f '{{if not .Indirect}}{{.Path}}{{end}}' all 2>/dev/null | grep -v '^$(MODULE)$$' | grep -v '^std$$' | grep -v '^$$'); \
	for d in $$deps; do \
		ok=0; \
		for a in $(ALLOWED_DEPS); do [ "$$d" = "$$a" ] && ok=1; done; \
		if [ $$ok -eq 0 ]; then echo "check-deps: unexpected direct dependency $$d"; exit 1; fi; \
	done; \
	echo "check-deps: ok ($$(echo $$deps | wc -w | tr -d ' ') direct dependencies)"

# API compatibility report against the previous tag. Warning-only during v0
# (dev-plan §1.3); becomes a hard failure at v1.0.
apidiff:
	@go run golang.org/x/exp/cmd/gorelease@latest || \
		echo "apidiff: gorelease unavailable or reported changes (advisory during v0)"

fmt:
	go fmt ./...

tidy:
	go mod tidy

clean:
	rm -rf bin
