package zoom

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// FuzzParse: Parse and everything derived from an Event must never panic, and
// successful results must keep the documented invariants.
func FuzzParse(f *testing.F) {
	files, err := filepath.Glob("testdata/*.json")
	if err != nil {
		f.Fatal(err)
	}
	for _, name := range files {
		b, err := os.ReadFile(name)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	for _, s := range []string{
		``, `null`, `[]`, `{}`, `{"event":5}`, `{"event":"meeting.participant_joined"}`,
		`{"event":"meeting.participant_left","payload":{"object":{"id":1e400,"participant":{"leave_time":"x","leave_reason":"<br>Reason:<"}}}}`,
		`{"event":"meeting.started","event_ts":"9223372036854775808","payload":{"object":{"id":-0.5e-10,"start_time":"9999-12-31T23:59:59.999999999+23:59"}}}`,
		`{"event":"meeting.participant_joined_breakout_room","payload":{"object":{"participant":{"user_name":"\ud83e","join_time":"0000-01-01T00:00:00Z"}}}}`,
	} {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		e, err := Parse(body)
		if err != nil {
			if !errors.Is(err, ErrBadPayload) {
				t.Fatalf("error does not wrap ErrBadPayload: %v", err)
			}
			if e != nil {
				t.Fatal("event returned together with an error")
			}
			if k := DedupeKey(nil, body); !hexKeyRe.MatchString(k) {
				t.Fatalf("DedupeKey(nil) = %q", k)
			}
			return
		}
		if e.Type == "" || e.Type != strings.TrimSpace(e.Type) {
			t.Fatalf("bad Type %q", e.Type)
		}
		if e.MeetingID != strings.TrimSpace(e.MeetingID) {
			t.Fatalf("MeetingID not trimmed: %q", e.MeetingID)
		}
		if IsParticipantEvent(e.Type) && e.Participant == nil {
			t.Fatal("participant event without Participant")
		}
		utc := func(name string, tm *time.Time) {
			if tm != nil && tm.Location() != time.UTC {
				t.Fatalf("%s not UTC: %v", name, tm.Location())
			}
		}
		utc("StartTime", e.StartTime)
		utc("EndTime", e.EndTime)
		if p := e.Participant; p != nil {
			utc("JoinTime", p.JoinTime)
			utc("LeaveTime", p.LeaveTime)
			_ = p.Key()
			if r := p.LeaveReasonText(); r != strings.TrimSpace(r) {
				t.Fatalf("LeaveReasonText not trimmed: %q", r)
			}
		}
		if at := e.OccurredAt(); !at.IsZero() && at.Location() != time.UTC {
			t.Fatalf("OccurredAt not UTC: %v", at)
		}
		if k := DedupeKey(e, body); !hexKeyRe.MatchString(k) {
			t.Fatalf("DedupeKey = %q", k)
		}
	})
}

// FuzzVerifyRoundTrip: whatever Sign produces verifies, and any change to the
// body breaks it.
func FuzzVerifyRoundTrip(f *testing.F) {
	f.Add(knownSecret, []byte(knownBody), uint32(1739923528), uint16(0))
	f.Add("s", []byte{}, uint32(0), uint16(3))
	f.Add("секрет", []byte(`{"user_name":"Фёдорова 🦊"}`), uint32(1791277275), uint16(7))
	f.Fuzz(func(t *testing.T, secret string, body []byte, unix uint32, flip uint16) {
		if secret == "" {
			return // Verify refuses an empty secret by design
		}
		now := time.Unix(int64(unix), 0)
		ts, sig := Sign(secret, now, body)
		if err := Verify(secret, signedHeader(ts, sig), body, now, time.Hour); err != nil {
			t.Fatalf("round trip failed: %v", err)
		}
		if len(body) == 0 {
			return
		}
		tampered := append([]byte(nil), body...)
		tampered[int(flip)%len(tampered)] ^= 0x80
		if err := Verify(secret, signedHeader(ts, sig), tampered, now, time.Hour); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("tampered body: Verify = %v, want ErrBadSignature", err)
		}
	})
}

// FuzzVerifyHostileHeaders: arbitrary header values never panic, every rejection
// wraps a sentinel, and nothing but the exact signature (modulo hex case and
// surrounding spaces) is accepted.
func FuzzVerifyHostileHeaders(f *testing.F) {
	f.Add(knownTS, knownSig, []byte(knownBody))
	f.Add("", "", []byte{})
	f.Add("-1", "v0=", []byte("x"))
	f.Add("099999999999", "v0="+strings.Repeat("A", 64), []byte("{}"))
	f.Add("1739923528 ", " v0=49FCF7B91015963AC5D59F505698FABD2BD5A57BAF24D083BEF0407EEFA4DD49", []byte(knownBody))
	now := time.Unix(1739923528, 0)
	f.Fuzz(func(t *testing.T, ts, sig string, body []byte) {
		err := Verify(knownSecret, signedHeader(ts, sig), body, now, 100*365*24*time.Hour)
		if err == nil {
			want := refSignature(knownSecret, strings.TrimSpace(ts), body)
			if !strings.EqualFold(strings.TrimSpace(sig), want) {
				t.Fatalf("accepted a forged signature: ts=%q sig=%q", ts, sig)
			}
			return
		}
		if !errors.Is(err, ErrMissingHeaders) && !errors.Is(err, ErrBadSignature) && !errors.Is(err, ErrStale) {
			t.Fatalf("error wraps no sentinel: %v", err)
		}
		if strings.Contains(err.Error(), knownSecret) {
			t.Fatalf("error leaks the secret: %v", err)
		}
	})
}

// FuzzLeaveReasonText: arbitrary text never panics and comes back trimmed.
func FuzzLeaveReasonText(f *testing.F) {
	for _, s := range []string{
		"left the meeting. Reason : left the meeting",
		"Jill Chill left the meeting.<br>Reason: Host ended the meeting.",
		"<<<>>>Reason::::", "&#x;&amp;<br", "reason\u00a0:\u2028x",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		r := (&Participant{LeaveReason: s}).LeaveReasonText()
		if r != strings.TrimSpace(r) {
			t.Fatalf("not trimmed: %q", r)
		}
	})
}
