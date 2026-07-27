// Package e2e holds the tests that talk to a real model.
//
// They are separated from the rest of the suite for a practical reason: they
// need credentials and they cost money, so they must never run by accident.
// Every file here carries the `e2e` build tag, which `make test` does not set;
// `make test-e2e` does.
//
// Each test also skips itself when the environment is not configured, so the
// tagged build still passes on a machine with no credentials rather than
// failing in a way that looks like a defect.
//
//	export OPENAI_BASE_URL=https://api.deepseek.com
//	export OPENAI_API_KEY=...
//	export OPENAI_MODEL=deepseek-v4-flash
//	make test-e2e
//
// What lives here is what a replayed fixture cannot check: that a real endpoint
// accepts the request EvalExec builds, that a real model can be made to answer
// in the agreed shape, and that its token accounting arrives intact.
package e2e
