// Package judge turns a judge_model configuration into a single chat
// completion capability, so that a Grader needing an LLM Judge never sees the
// protocol it is talking to.
//
// It is deliberately the only package in this module that imports
// github.com/vogo/aimodel. Every protocol EvalExec supports — openai-chat,
// anthropic-messages, http-json and stdio-jsonl — is resolved here down to one
// aimodel.ChatCompleter, which also confines the blast radius of an aimodel
// upgrade to this package.
//
// The implementation lands in M4 (openai-chat / anthropic-messages) and M6
// (the two custom providers). M0 only carries the connectivity smoke test that
// proves the pinned aimodel version can reach a real endpoint.
//
// # Stability
//
// L2 extension point. From v1.0 it follows the Go compatibility promise.
// Adding a method to the Judge interface is a breaking change, so the
// interface is kept to a single method by design.
package judge
