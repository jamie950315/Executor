package audit

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAppendAndListKeepsMetadataWithoutCommandOutput(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "audit.jsonl"), 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	want := Event{Time: time.Unix(100, 0).UTC(), Actor: "oauth:owner", Tool: "terminal", SessionID: "s-1", Identity: "administrator", CWD: "/tmp", Outcome: "ok"}
	if err := store.Append(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.List(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Tool != want.Tool || got[0].Outcome != want.Outcome {
		t.Fatalf("unexpected events: %#v", got)
	}
}

func TestPruneRemovesExpiredEvents(t *testing.T) {
	now := time.Now().UTC()
	store, err := Open(filepath.Join(t.TempDir(), "audit.jsonl"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []Event{{Time: now.Add(-2 * time.Hour), Tool: "old"}, {Time: now, Tool: "new"}} {
		if err := store.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Prune(now); err != nil {
		t.Fatal(err)
	}
	events, err := store.List(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Tool != "new" {
		t.Fatalf("events after prune: %#v", events)
	}
}
