package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/rules"
)

func TestLoadMissingIsNotAnError(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	snap, err := s.Load()
	if err != nil || snap != nil {
		t.Fatalf("a fresh VPS has no state file: got %+v, %v", snap, err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := Store{Dir: dir}
	in := Snapshot{
		Rules:     []rules.Rule{{ID: "alloc-1", Proto: "both", PublicPort: 9445, TargetIP: "10.0.0.10"}},
		AppliedAt: time.Unix(1700000000, 0).UTC(),
	}
	if err := s.Save(in); err != nil {
		t.Fatal(err)
	}
	out, err := s.Load()
	if err != nil || out == nil {
		t.Fatalf("load failed: %+v %v", out, err)
	}
	if len(out.Rules) != 1 || out.Rules[0].ID != "alloc-1" || !out.AppliedAt.Equal(in.AppliedAt) {
		t.Fatalf("round trip lost data: %+v", out)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

func TestSaveCreatesTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "autoproxy")
	if err := (Store{Dir: dir}).Save(Snapshot{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); err != nil {
		t.Fatal(err)
	}
}

func TestLoadCorruptFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("{not json"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := (Store{Dir: dir}).Load(); err == nil {
		t.Fatal("a corrupt state file must be reported, not silently ignored")
	}
}
