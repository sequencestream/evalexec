package grader

import (
	"fmt"

	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/grader/declaration"
	"github.com/sequencestream/evalexec/validate"
)

// Resolve reports what the Grader named by spec declares about itself, by
// building it and asking.
//
// Building during the pre-check is deliberate: a Grader whose parameters make
// it unconstructible — an uncompilable pattern, a malformed schema — should
// fail before the run starts, not on the first sample and then on every one
// after it.
func (r *Registry) Resolve(spec evalspec.GraderSpec) (declaration.Declaration, error) {
	if _, ok := r.Lookup(spec.Entry); !ok {
		return declaration.Declaration{}, fmt.Errorf("%w: %q", validate.ErrUnknownEntry, spec.Entry)
	}

	g, err := r.Build(spec)
	if err != nil {
		return declaration.Declaration{}, err
	}

	return g.Declare(), nil
}

// compile-time check that a registry satisfies the pre-check contract.
var _ validate.GraderResolver = (*Registry)(nil)
