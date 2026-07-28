# Agent Guide

## Documentation

- `doc/architecture.md` — How the pieces fit together: program structure and execution order.
- `doc/api.md` — Reference for driving EvalExec via the command line or the Go library.
- `doc/protocol.md` — Data model and wire specification for producing/consuming EvalExec documents, or implementing a Grader/Judge in any language.

## Code Style

- Do not record development-process information in comments.

## Build/Test/Lint commands

```bash
make build      # build bin/evalexec (version injected via -ldflags)
make test       # go test -race ./...
make lint       # terminology, library-boundary and dependency guards + golangci-lint
make test-e2e   # against a real model; needs OPENAI_BASE_URL / OPENAI_API_KEY / OPENAI_MODEL
```
