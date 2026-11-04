package tcc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// capturedUpool is the subject of the captured prompt: the Homebrew upool the benchmark's create
// hook ran, by its resolved path (testdata/captures/tcc/documents-prompting.ndjson).
const capturedUpool = "/opt/homebrew/Cellar/upool/0.6.1/bin/upool"

// capturedMsgID is the captured prompt's request.
const capturedMsgID = "179.79495"

// capture returns the bytes of the TCC capture file name.
func capture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "captures", "tcc", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeLog answers the prompt query with prompts and the result query with results, and records
// the queries it got.
type fakeLog struct {
	prompts, results []byte
	err              error
	queries          []string
}

func (f *fakeLog) Show(_ context.Context, window time.Duration, predicate string) ([]byte,
	error) {
	if window != Window {
		return nil, errors.New("asked for a window other than Window")
	}
	f.queries = append(f.queries, predicate)
	if f.err != nil {
		return nil, f.err
	}
	if strings.Contains(predicate, "AUTHREQ_RESULT") {
		return f.results, nil
	}
	return f.prompts, nil
}

// The captured prompt is pending while the log holds no result for it, as it was in the minutes
// after the benchmark's hook raised it: Pending returns it, asking for the result of its own msgID.
func TestPendingFindsThePromptBeforeItIsAnswered(t *testing.T) {
	l := &fakeLog{prompts: capture(t, "documents-prompting.ndjson"),
		results: capture(t, "documents-result-pending.ndjson")}
	p, pending, err := Pending(t.Context(), l, capturedUpool)
	want := Prompt{MsgID: capturedMsgID, Service: Documents, Subject: capturedUpool}
	if err != nil || !pending || p != want {
		t.Fatalf("Pending = %+v, %t, %v; want %+v, true and no error", p, pending, err, want)
	}
	if p.Name() != "upool" {
		t.Errorf("Name = %q, want upool, how the prompt names a binary", p.Name())
	}
	if len(l.queries) != 2 || !strings.Contains(l.queries[0], `"AUTHREQ_PROMPTING: "`) ||
		!strings.Contains(l.queries[0], Documents) ||
		!strings.Contains(l.queries[1], `"AUTHREQ_RESULT: msgID=`+capturedMsgID+`,"`) {
		t.Errorf("queries = %q, want the Documents prompts, then the result of %s", l.queries,
			capturedMsgID)
	}
}

// Once the log records the prompt's result, authValue 2 when it was allowed, it is no longer
// pending.
func TestPendingIsOverOnceThePromptIsAnswered(t *testing.T) {
	l := &fakeLog{prompts: capture(t, "documents-prompting.ndjson"),
		results: capture(t, "documents-result-allowed.ndjson")}
	if p, pending, err := Pending(t.Context(), l, capturedUpool); err != nil || pending {
		t.Errorf("Pending = %+v, %t, %v; want no prompt pending", p, pending, err)
	}
}

// A prompt about another client is not the hook's: it is passed over, and its result is not
// asked for. An empty subject counts every client's.
func TestPendingCountsOnlyThePromptsAboutItsSubject(t *testing.T) {
	l := &fakeLog{prompts: capture(t, "documents-prompting.ndjson"),
		results: capture(t, "documents-result-pending.ndjson")}
	p, pending, err := Pending(t.Context(), l, "/opt/homebrew/Cellar/upool/0.6.2/bin/upool")
	if err != nil || pending || len(l.queries) != 1 {
		t.Errorf("Pending = %+v, %t, %v after %d queries; want no prompt, after one", p, pending,
			err, len(l.queries))
	}
	if _, pending, err := Pending(t.Context(), l, ""); err != nil || !pending {
		t.Errorf("Pending with no subject = %t, %v; want the captured prompt", pending, err)
	}
}

// A log with no prompt in it has none pending, and Pending asks for no results.
func TestPendingWithNoPromptLoggedFindsNone(t *testing.T) {
	l := &fakeLog{prompts: capture(t, "documents-result-pending.ndjson")}
	if p, pending, err := Pending(t.Context(), l, capturedUpool); err != nil || pending ||
		len(l.queries) != 1 {
		t.Errorf("Pending = %+v, %t, %v after %d queries; want none, after one", p, pending, err,
			len(l.queries))
	}
}

// Readable reports a log that cannot be read, from a query of the last minute, and nothing for one
// that can.
func TestReadableReportsALogThatCannotBeRead(t *testing.T) {
	boom := errors.New("log: cannot read")
	if err := Readable(t.Context(), &shortLog{fakeLog{err: boom}}); !errors.Is(err, boom) {
		t.Errorf("Readable of a failing log = %v, want %v", err, boom)
	}
	l := &shortLog{fakeLog{prompts: capture(t, "documents-result-pending.ndjson")}}
	if err := Readable(t.Context(), l); err != nil || len(l.queries) != 1 ||
		!strings.Contains(l.queries[0], Documents) {
		t.Errorf("Readable = %v after queries %q; want nil after one for Documents prompts", err,
			l.queries)
	}
	if err := Readable(t.Context(), nil); err == nil {
		t.Error("Readable of no log = nil, want an error")
	}
}

// shortLog is a fakeLog that answers any window, as Readable asks for a minute's.
type shortLog struct{ fakeLog }

func (s *shortLog) Show(ctx context.Context, _ time.Duration, predicate string) ([]byte, error) {
	return s.fakeLog.Show(ctx, Window, predicate)
}

// A log that cannot be read, or that prints something other than ndjson, is an error, not a
// prompt.
func TestPendingReportsALogItCannotRead(t *testing.T) {
	boom := errors.New("log: cannot read")
	for name, l := range map[string]Log{
		"failed":  &fakeLog{err: boom},
		"garbled": &fakeLog{prompts: []byte("Timestamp  Thread  Type\n")},
		"none":    nil,
	} {
		if _, pending, err := Pending(context.Background(), l, capturedUpool); err == nil ||
			pending {
			t.Errorf("%s: Pending = %t, %v; want an error and no prompt", name, pending, err)
		}
	}
}
