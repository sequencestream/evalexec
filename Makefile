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
# aimodel plus one JSON Schema library (added in M3).
ALLOWED_DEPS := github.com/vogo/aimodel

.PHONY: all build test test-e2e lint lint-terms lint-boundary lint-secrets check-deps apidiff fmt tidy clean

all: build test lint

build:
	@mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/evalexec

test:
	go test -race ./...

# Real-model end-to-end tests. Needs OPENAI_BASE_URL / OPENAI_API_KEY / OPENAI_MODEL;
# individual tests skip themselves when those are unset.
test-e2e:
	go test -tags e2e -count=1 -v ./...

# The golangci-lint call must not be chained with `|| echo`: a lint failure
# would then be swallowed as "not installed" and the target would still
# succeed. Absence and failure are decided separately.
lint: lint-terms lint-boundary check-deps
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
	@hits=$$(git ls-files --cached --others --exclude-standard | grep -vE '^doc/|^Makefile$$' | xargs grep -InE 'evaluator' 2>/dev/null); \
	if [ -n "$$hits" ]; then \
		echo "lint-terms: banned word root 'evaluator' found (use 'grader'):"; \
		echo "$$hits"; \
		exit 1; \
	fi; \
	echo "lint-terms: ok"

# dev-plan §1.5 / §7: evalexec ships as a library too, so anything outside
# cmd/ that exits the process, grabs signals, or writes to os.Stderr would
# misbehave inside a host process. Test files are exempt (M5 drives a real
# subprocess to test SIGINT).
lint-boundary:
	@files=$$(git ls-files --cached --others --exclude-standard '*.go' | grep -v '^cmd/' | grep -v '_test\.go$$'); \
	if [ -z "$$files" ]; then echo "lint-boundary: ok (no files)"; exit 0; fi; \
	hits=$$(grep -InE 'os\.Exit|signal\.Notify|os\.Stderr' $$files 2>/dev/null); \
	if [ -n "$$hits" ]; then \
		echo "lint-boundary: os.Exit / signal.Notify / os.Stderr are only allowed under cmd/:"; \
		echo "$$hits"; \
		exit 1; \
	fi; \
	echo "lint-boundary: ok"

# dev-plan §1.5: assert no secret ever reaches a result directory or logs/.
# Implemented in M4, once there is a Judge and a logs/ directory to scan.
lint-secrets:
	@echo "lint-secrets: not implemented until M4 (needs Judge + logs/)"

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
