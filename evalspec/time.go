package evalspec

import (
	"encoding/json"
	"time"
)

// Timestamp is an RFC 3339 UTC instant with second precision, the only time
// format the protocol admits.
//
// It exists instead of a plain time.Time because time.Time marshals with
// nanoseconds and the local zone offset — "2026-07-27T09:00:00.123456789+08:00"
// — which is neither RFC 3339 UTC as specified nor stable enough for golden
// file comparison. Second precision loses nothing: elapsed time is reported
// separately in whole milliseconds by latency_ms and duration_ms.
type Timestamp struct {
	t time.Time
}

// NewTimestamp truncates t to the second and converts it to UTC.
func NewTimestamp(t time.Time) Timestamp {
	return Timestamp{t: t.UTC().Truncate(time.Second)}
}

// Time returns the underlying instant, always in UTC.
func (ts Timestamp) Time() time.Time { return ts.t }

// IsZero reports whether the timestamp is unset.
func (ts Timestamp) IsZero() bool { return ts.t.IsZero() }

// String renders the RFC 3339 UTC form, or the empty string when unset.
func (ts Timestamp) String() string {
	if ts.t.IsZero() {
		return ""
	}

	return ts.t.Format(time.RFC3339)
}

// MarshalJSON writes the RFC 3339 UTC form. An unset timestamp marshals to
// null rather than to the year-one instant.
func (ts Timestamp) MarshalJSON() ([]byte, error) {
	if ts.t.IsZero() {
		return jsonNull, nil
	}

	return json.Marshal(ts.t.Format(time.RFC3339))
}

// UnmarshalJSON accepts an RFC 3339 string or null.
func (ts *Timestamp) UnmarshalJSON(data []byte) error {
	if isJSONNull(data) {
		ts.t = time.Time{}

		return nil
	}

	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}

	if s == "" {
		ts.t = time.Time{}

		return nil
	}

	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return err
	}

	ts.t = parsed.UTC()

	return nil
}
