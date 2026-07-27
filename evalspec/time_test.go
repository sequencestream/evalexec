package evalspec_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sequencestream/evalexec/evalspec"
)

// TestTimestampIsRFC3339UTC covers why this type exists: a plain time.Time
// marshals with nanoseconds and a local offset, which is neither the format
// the protocol specifies nor stable enough for golden-file comparison.
func TestTimestampIsRFC3339UTC(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("timezone data unavailable: %v", err)
	}

	local := time.Date(2026, 7, 27, 9, 0, 0, 123456789, shanghai)

	data, err := json.Marshal(evalspec.NewTimestamp(local))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	const want = `"2026-07-27T01:00:00Z"`
	if string(data) != want {
		t.Errorf("marshal = %s, want %s (UTC, second precision)", data, want)
	}
}

func TestTimestampZeroMarshalsToNull(t *testing.T) {
	data, err := json.Marshal(evalspec.Timestamp{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if string(data) != "null" {
		t.Errorf("zero timestamp = %s, want null (not the year-one instant)", data)
	}
}

func TestTimestampRoundTrip(t *testing.T) {
	for _, in := range []string{`"2026-07-27T01:00:00Z"`, `null`} {
		var ts evalspec.Timestamp
		if err := json.Unmarshal([]byte(in), &ts); err != nil {
			t.Fatalf("unmarshal %s: %v", in, err)
		}

		out, err := json.Marshal(ts)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		if string(out) != in {
			t.Errorf("round trip of %s produced %s", in, out)
		}
	}
}

func TestTimestampAcceptsOffsetAndNormalizes(t *testing.T) {
	var ts evalspec.Timestamp
	if err := json.Unmarshal([]byte(`"2026-07-27T09:00:00+08:00"`), &ts); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got := ts.String(); got != "2026-07-27T01:00:00Z" {
		t.Errorf("String() = %q, want the UTC form", got)
	}
}

func TestTimestampRejectsGarbage(t *testing.T) {
	var ts evalspec.Timestamp
	if err := json.Unmarshal([]byte(`"27/07/2026"`), &ts); err == nil {
		t.Error("a non-RFC-3339 string must be rejected")
	}
}
