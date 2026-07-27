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
	"fmt"
	"sort"
	"sync"

	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/grader/declaration"
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

// Factory builds a Grader from its configuration.
type Factory func(spec evalspec.GraderSpec) (Grader, error)

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
func (r *Registry) Build(spec evalspec.GraderSpec) (Grader, error) {
	f, ok := r.Lookup(spec.Entry)
	if !ok {
		return nil, fmt.Errorf("grader: no grader registered for entry %q (registered: %v)", spec.Entry, r.Entries())
	}

	g, err := f(spec)
	if err != nil {
		return nil, fmt.Errorf("grader: cannot build %q: %w", spec.Entry, err)
	}

	return g, nil
}
