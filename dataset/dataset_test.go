package dataset_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sequencestream/evalexec/dataset"
	"github.com/sequencestream/evalexec/evalspec"
)

func writeDataset(t *testing.T, content string) string {
	t.Helper()

	p := filepath.Join(t.TempDir(), "dataset.jsonl")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	return p
}

func readAll(t *testing.T, path string) ([]*evalspec.Session, error) {
	t.Helper()

	r, err := dataset.Open(path)
	if err != nil {
		return nil, err
	}

	defer func() { _ = r.Close() }()

	var out []*evalspec.Session

	for {
		s, _, err := r.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}

		if err != nil {
			return out, err
		}

		out = append(out, s)
	}
}

func TestSequenceIsOneBased(t *testing.T) {
	p := writeDataset(t, `{"case_id":"c1","input":{}}
{"case_id":"c2","input":{}}
{"case_id":"c3","input":{}}
`)

	r, err := dataset.Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	defer func() { _ = r.Close() }()

	for want := 1; want <= 3; want++ {
		s, seq, err := r.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}

		if seq != want {
			t.Errorf("sequence = %d, want %d", seq, want)
		}

		if s.CaseID == "" {
			t.Error("case_id must be set")
		}
	}

	if _, _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("after the last row Next = %v, want io.EOF", err)
	}
}

// TestBlankLinesAreNotRows protects the identity between dataset lines and
// result records at its source. A trailing newline is normal in a text file
// and must not become a phantom row that the backfill then has to invent a
// case_id for.
func TestBlankLinesAreNotRows(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
	}{
		{name: "trailing newline", content: "{\"case_id\":\"c1\"}\n", want: 1},
		{name: "no trailing newline", content: `{"case_id":"c1"}`, want: 1},
		{name: "several blank lines", content: "{\"case_id\":\"c1\"}\n\n\n{\"case_id\":\"c2\"}\n\n", want: 2},
		{name: "whitespace-only lines", content: "{\"case_id\":\"c1\"}\n   \n\t\n", want: 1},
		{name: "empty file", content: "", want: 0},
		{name: "only whitespace", content: "\n\n  \n", want: 0},
		{name: "windows line endings", content: "{\"case_id\":\"c1\"}\r\n{\"case_id\":\"c2\"}\r\n", want: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readAll(t, writeDataset(t, tt.content))
			if err != nil {
				t.Fatalf("read: %v", err)
			}

			if len(got) != tt.want {
				t.Errorf("read %d rows, want %d", len(got), tt.want)
			}
		})
	}
}

// TestParseErrorCarriesTheLineNumber matters because a user with a million
// rows needs to know which one to look at.
func TestParseErrorCarriesTheLineNumber(t *testing.T) {
	p := writeDataset(t, `{"case_id":"c1","input":{}}
{"case_id":"c2","input":{}}
{"case_id":"c3","input":
`)

	_, err := readAll(t, p)
	if err == nil {
		t.Fatal("a malformed line must be reported")
	}

	var pe *dataset.ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("error is %T, want *dataset.ParseError", err)
	}

	if pe.Line != 3 {
		t.Errorf("Line = %d, want 3", pe.Line)
	}

	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("error = %q, want it to name line 3", err)
	}
}

func TestEmptyCaseIDIsRejected(t *testing.T) {
	_, err := readAll(t, writeDataset(t, `{"case_id":"","input":{}}`+"\n"))
	if err == nil {
		t.Fatal("an empty case_id must be rejected")
	}

	if !strings.Contains(err.Error(), "case_id") {
		t.Errorf("error = %q, want it to name case_id", err)
	}
}

func TestMissingCaseIDIsRejected(t *testing.T) {
	_, err := readAll(t, writeDataset(t, `{"input":{}}`+"\n"))
	if err == nil {
		t.Fatal("a row without case_id must be rejected")
	}
}

func TestOpenMissingFile(t *testing.T) {
	if _, err := dataset.Open(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Error("opening a missing file must fail")
	}
}

func TestMemoryIndex(t *testing.T) {
	idx := dataset.NewMemoryIndex()

	for _, id := range []string{"c1", "c2", "c3"} {
		if err := idx.Add(id); err != nil {
			t.Fatalf("Add(%q): %v", id, err)
		}
	}

	if idx.Len() != 3 {
		t.Errorf("Len = %d, want 3", idx.Len())
	}

	err := idx.Add("c2")
	if err == nil {
		t.Fatal("a duplicate must be reported")
	}

	if !errors.Is(err, dataset.ErrDuplicateCase) {
		t.Errorf("error = %v, want it to wrap ErrDuplicateCase", err)
	}

	if !strings.Contains(err.Error(), "c2") {
		t.Errorf("error = %q, want it to name the duplicate id", err)
	}
}

// TestSessionThreeStateSurvivesTheReader checks that the distinction the whole
// requires check depends on is preserved by streaming, not just by unmarshal.
func TestSessionThreeStateSurvivesTheReader(t *testing.T) {
	p := writeDataset(t, `{"case_id":"c1","input":{},"output":null}
{"case_id":"c2","input":{}}
`)

	got, err := readAll(t, p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if !got[0].Has(evalspec.FieldOutput) || !got[0].IsNull(evalspec.FieldOutput) {
		t.Error("c1 output should be present and null")
	}

	if got[1].Has(evalspec.FieldOutput) {
		t.Error("c2 output should be absent")
	}
}

func TestCountTracksRowsRead(t *testing.T) {
	r, err := dataset.Open(writeDataset(t, "{\"case_id\":\"c1\"}\n{\"case_id\":\"c2\"}\n"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	defer func() { _ = r.Close() }()

	if r.Count() != 0 {
		t.Errorf("Count before reading = %d, want 0", r.Count())
	}

	if _, _, err := r.Next(); err != nil {
		t.Fatalf("Next: %v", err)
	}

	if r.Count() != 1 {
		t.Errorf("Count after one row = %d, want 1", r.Count())
	}
}
