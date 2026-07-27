// Package grader defines what a Grader is and how one is found.
//
// A Grader turns one session into one evaluation. It is the extension point of
// EvalExec: downstream Go programs can register their own and run them under
// protocol "builtin" with a custom entry name, without a subprocess and
// without forking anything.
//
// That does not widen what the evalexec binary can do — it registers only the
// five built-in entries — but it does mean an orchestrator embedding this
// module gets first-class Graders for free.
//
// # Stability
//
// L2 extension point. From v1.0 this follows the Go compatibility promise, and
// adding a method to the Grader interface would be a breaking change. The
// interface is therefore deliberately two methods wide: future capabilities
// must arrive as fields on Declaration or GradeCall, which are additive.
package grader

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/grader/declaration"
	"github.com/sequencestream/evalexec/judge"
)

// Declaration is re-exported so implementers need not import a second package
// to satisfy the interface.
type Declaration = declaration.Declaration

// Grader evaluates one session.
type Grader interface {
	// Declare states what this Grader needs, so a run can be validated
	// end to end before the Grader is ever called.
	Declare() Declaration

	// Grade evaluates one sample.
	//
	// The two results say different things, and conflating them is the most
	// natural mistake here:
	//
	//   - A returned Evaluation with status "fail" means the Grader ran and
	//     could not reach a conclusion — insufficient evidence, a Judge
	//     failure, a timeout. That is a normal return value, not an error: the
	//     sample was processed, so it still counts as completed, and the
	//     reason travels in Evaluation.Error.
	//   - A returned error means the Grader itself broke. The runner turns it
	//     into an internal_error failure for that one sample and carries on.
	//
	// A low score is neither. An evaluation that concluded "this answer is
	// wrong" succeeded at its job.
	Grade(ctx context.Context, call evalspec.GradeCall) (evalspec.Evaluation, error)
}

// Deps carries what a Grader needs beyond its own configuration.
//
// It is a struct rather than extra parameters so that a future need can be
// added without changing every factory ever written — the same reasoning that
// keeps the Grader interface at two methods.
type Deps struct {
	// Judge is non-nil exactly when the Grader's declaration says it needs
	// one. A Grader that declared requires_judge can rely on it: the
	// pre-check refuses to start a run that would leave it nil.
	Judge judge.Judge
}

// Factory builds a Grader from its configuration.
type Factory func(spec evalspec.GraderSpec, deps Deps) (Grader, error)

// ErrUnknownEntry reports an entry name no Grader is registered for.
//
// It exists as a sentinel so a caller can tell "no Grader by that name" apart
// from "that Grader exists and refused to be configured". The two want
// different diagnostics: the first may fall back to the built-in table, the
// second is a configuration error to report as-is.
var ErrUnknownEntry = errors.New("grader: no grader is registered for this entry")

// Registry maps entry names to factories.
//
// It is a type rather than a package-level map so tests can use an isolated
// one; a shared global would let registrations leak between them.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{factories: make(map[string]Factory)}
}

// defaultRegistry holds the built-in Graders, which register themselves in
// their packages' init functions.
var defaultRegistry = NewRegistry()

// Default returns the registry holding the built-in Graders, plus anything a
// downstream program registered into it.
func Default() *Registry { return defaultRegistry }

// Register adds a factory under an entry name.
//
// A duplicate name panics rather than overwriting. Silent replacement would
// make which Grader runs depend on package import order, and a run that graded
// with the wrong Grader is worse than one that refused to start.
func (r *Registry) Register(entry string, f Factory) {
	if entry == "" {
		panic("grader: Register called with an empty entry name")
	}

	if f == nil {
		panic("grader: Register called with a nil factory for entry " + entry)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, dup := r.factories[entry]; dup {
		panic(fmt.Sprintf("grader: entry %q is already registered", entry))
	}

	r.factories[entry] = f
}

// Lookup finds a factory by entry name.
func (r *Registry) Lookup(entry string) (Factory, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	f, ok := r.factories[entry]

	return f, ok
}

// Entries lists the registered entry names, sorted.
func (r *Registry) Entries() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

// Register adds a factory to the default registry. Built-in Graders call it
// from init; a downstream program may call it at startup to add its own.
func Register(entry string, f Factory) { defaultRegistry.Register(entry, f) }

// Build constructs the Grader named by a specification.
func (r *Registry) Build(spec evalspec.GraderSpec, deps Deps) (Grader, error) {
	f, ok := r.Lookup(spec.Entry)
	if !ok {
		return nil, fmt.Errorf("%w: %q (registered: %v)", ErrUnknownEntry, spec.Entry, r.Entries())
	}

	g, err := f(spec, deps)
	if err != nil {
		return nil, fmt.Errorf("grader: cannot build %q: %w", spec.Entry, err)
	}

	return g, nil
}

// Resolve reports what the Grader named by spec declares about itself, by
// building it and asking.
//
// Building during the pre-check is deliberate: a Grader whose parameters make
// it unconstructible — an uncompilable pattern, a malformed schema — should
// fail before the run starts rather than on the first sample and then on every
// one after it.
func (r *Registry) Resolve(spec evalspec.GraderSpec, deps Deps) (declaration.Declaration, error) {
	g, err := r.Build(spec, deps)
	if err != nil {
		return declaration.Declaration{}, err
	}

	return g.Declare(), nil
}
