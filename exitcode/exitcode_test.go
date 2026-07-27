package exitcode_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/sequencestream/evalexec/evalerr"
	"github.com/sequencestream/evalexec/evalspec"
	"github.com/sequencestream/evalexec/exitcode"
)

func TestFromError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "no error", err: nil, want: 0},
		{name: "argument", err: evalerr.Argument("arguments", "bad flag"), want: 2},
		{name: "precheck", err: evalerr.Precheck("dataset_parse", "bad line"), want: 2},
		{name: "output", err: evalerr.Output("output", "directory not empty"), want: 4},
		{name: "runtime", err: evalerr.Runtime("summary", "counts do not add up"), want: 3},
		{
			name: "interrupt",
			err:  &evalerr.Error{Kind: evalerr.KindInterrupt, Step: "run"},
			want: 130,
		},
		{
			name: "wrapped several layers deep",
			err:  fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", evalerr.Output("output", "boom"))),
			want: 4,
		},
		{
			// An unknown failure is a fault. Reporting success for something
			// nobody classified would let a bug read as a clean run.
			name: "unclassified",
			err:  errors.New("something nobody labelled"),
			want: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitcode.FromError(tt.err); got != tt.want {
				t.Errorf("FromError(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

// TestFromResultFailFastIsSuccess covers the mapping most likely to be written
// backwards. Fail-fast is a stopping policy the caller asked for, so the
// command succeeded; the incompleteness is reported by status and counts.
func TestFromResultFailFastIsSuccess(t *testing.T) {
	failFast := evalspec.StopFailFast
	interrupt := evalspec.StopInterrupt

	tests := []struct {
		name   string
		result *evalspec.EvalResult
		want   int
	}{
		{
			name:   "completed",
			result: &evalspec.EvalResult{Status: evalspec.RunCompleted},
			want:   0,
		},
		{
			name: "completed with every evaluation failed",
			result: &evalspec.EvalResult{
				Status:     evalspec.RunCompleted,
				Counts:     evalspec.Counts{Total: 3, Completed: 3},
				Evaluation: evalspec.EvaluationSummary{Evaluated: 3, Fail: 3},
			},
			// The run processed everything it was given. That the agent did
			// badly, or that no evaluation concluded, is not the command's
			// failure to report through an exit code.
			want: 0,
		},
		{
			name:   "cancelled by fail-fast",
			result: &evalspec.EvalResult{Status: evalspec.RunCancelled, StopReason: &failFast},
			want:   0,
		},
		{
			name:   "cancelled by interrupt",
			result: &evalspec.EvalResult{Status: evalspec.RunCancelled, StopReason: &interrupt},
			want:   130,
		},
		{
			name:   "failed",
			result: &evalspec.EvalResult{Status: evalspec.RunFailed},
			want:   3,
		},
		{
			name:   "nil result",
			result: nil,
			want:   3,
		},
		{
			name:   "unknown status",
			result: &evalspec.EvalResult{Status: "weird"},
			want:   3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitcode.FromResult(tt.result); got != tt.want {
				t.Errorf("FromResult = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestCodeValuesAreStable pins the user-facing contract. These numbers appear
// in shell scripts and CI configuration; changing one silently breaks them.
func TestCodeValuesAreStable(t *testing.T) {
	got := map[string]int{
		"OK": exitcode.OK, "Argument": exitcode.Argument, "Runtime": exitcode.Runtime,
		"Output": exitcode.Output, "Interrupt": exitcode.Interrupt,
	}

	want := map[string]int{"OK": 0, "Argument": 2, "Runtime": 3, "Output": 4, "Interrupt": 130}

	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s = %d, want %d", name, got[name], w)
		}
	}
}

func TestEveryKindMaps(t *testing.T) {
	for k := evalerr.KindArgument; k <= evalerr.KindInterrupt; k++ {
		if !k.IsValid() {
			t.Fatalf("kind %d should be valid", k)
		}

		if code := exitcode.FromKind(k); code == exitcode.OK {
			t.Errorf("kind %v maps to 0; every failure kind must map to a non-zero code", k)
		}
	}
}
