package clients

import (
	"strings"
	"testing"
	"time"
)

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
		ok   bool
	}{
		{"0.3.0", "0.3.0", 0, true},
		{"v0.3.0", "0.3.0", 0, true},
		{"0.2.7", "0.3.0", -1, true},
		{"0.10.0", "0.9.9", 1, true},
		{"1.0.0", "0.99.99", 1, true},
		{"0.3.0-rc1", "0.3.0", -1, true},
		{"0.3.0", "0.3.0-rc1", 1, true},
		{"0.3.0+build5", "0.3.0", 0, true},
		{"dev", "0.3.0", 0, false},
		{"0.3", "0.3.0", 0, false},
		{"0.3.0-", "0.3.0", 0, false},
		{"", "0.3.0", 0, false},
	} {
		got, ok := Compare(c.a, c.b)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("Compare(%q, %q) = %d, %v; want %d, %v", c.a, c.b, got, ok, c.want, c.ok)
		}
	}
}

func TestNormalizeRelease(t *testing.T) {
	for in, want := range map[string]string{"0.3.1": "0.3.1", "v1.2.3": "1.2.3", " 2.0.0 ": "2.0.0"} {
		if got, err := NormalizeRelease(in); err != nil || got != want {
			t.Errorf("NormalizeRelease(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "latest", "1.2", "1.2.3.4", "1.2.3-rc1", "v1.2.3/../x", "1.2.3 && id", "12345.0.0"} {
		if _, err := NormalizeRelease(bad); err == nil {
			t.Errorf("NormalizeRelease(%q) accepted", bad)
		}
	}
}

func TestCheckinLifecycleAndPersistence(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(Store{Dir: dir})
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	r.SetClock(func() time.Time { return now })

	ans, _, changed, err := r.Checkin("p1", Report{Version: "0.3.0", Flavour: "systemd"})
	if err != nil || !changed || ans.DesiredVersion != "" {
		t.Fatalf("first report: %+v %v %v", ans, changed, err)
	}
	// Nothing new: not "changed", and not written again inside SaveEvery.
	now = now.Add(2 * time.Minute)
	if _, _, changed, _ := r.Checkin("p1", Report{Version: "0.3.0", Flavour: "systemd"}); changed {
		t.Fatalf("an identical report must not count as a change")
	}

	rec, err := r.SetDesired("p1", "0.3.1")
	if err != nil || rec.DesiredVersion != "0.3.1" || rec.RequestID == "" {
		t.Fatalf("SetDesired: %+v %v", rec, err)
	}
	first := rec.RequestID
	rec, _ = r.SetDesired("p1", "0.3.1")
	if rec.RequestID == first {
		t.Fatalf("pressing Update again must mint a new request id, so a failed request can be retried")
	}

	// A reload sees the request.
	r2 := NewRegistry(Store{Dir: dir})
	if err := r2.Load(); err != nil {
		t.Fatal(err)
	}
	if got := r2.Get("p1"); got.DesiredVersion != "0.3.1" || got.Version != "0.3.0" {
		t.Fatalf("not persisted: %+v", got)
	}

	// Reaching a newer version than asked also completes the request.
	ans, rec, _, _ = r.Checkin("p1", Report{Version: "0.3.2"})
	if ans.DesiredVersion != "" || rec.Update == nil || rec.Update.State != StateUpdated || rec.DesiredVersion != "" {
		t.Fatalf("request not completed: %+v %+v", ans, rec)
	}
	if rec.Flavour != FlavourUnknown {
		t.Fatalf("a missing flavour is 'unknown', got %q", rec.Flavour)
	}
}

func TestDevBuildNeverCompletesARequest(t *testing.T) {
	r := NewRegistry(Store{Dir: t.TempDir()})
	_, _ = r.SetDesired("p1", "0.3.1")
	ans, _, _, err := r.Checkin("p1", Report{Version: "dev"})
	if err != nil || ans.DesiredVersion != "0.3.1" {
		t.Fatalf("a version that cannot be compared must leave the request standing: %+v %v", ans, err)
	}
}

func TestCheckinValidation(t *testing.T) {
	r := NewRegistry(Store{Dir: t.TempDir()})
	for _, rep := range []Report{
		{Version: ""},
		{Version: "0.3.0 && reboot"},
		{Version: strings.Repeat("9", 40)},
		{Version: "0.3.0", Update: &UpdateReport{State: "exploded"}},
		{Version: "0.3.0", Update: &UpdateReport{State: "failed", Version: "../x"}},
	} {
		if _, _, _, err := r.Checkin("p1", rep); err == nil {
			t.Errorf("accepted %+v", rep)
		}
	}
	_, rec, _, err := r.Checkin("p1", Report{Version: "0.3.0", Update: &UpdateReport{State: "failed", Error: strings.Repeat("e", 1000) + "\x1b[31m"}})
	if err != nil || rec.Update == nil || len(rec.Update.Error) > MaxErrorLen+3 || strings.ContainsRune(rec.Update.Error, 0x1b) {
		t.Fatalf("error text not bounded and cleaned: %v %q", err, rec.Update)
	}
}

func TestPrune(t *testing.T) {
	r := NewRegistry(Store{Dir: t.TempDir()})
	_, _, _, _ = r.Checkin("keep", Report{Version: "0.3.0"})
	_, _, _, _ = r.Checkin("gone", Report{Version: "0.3.0"})
	if err := r.Prune(map[string]bool{"keep": true}); err != nil {
		t.Fatal(err)
	}
	if r.Get("gone").Version != "" || r.Get("keep").Version == "" {
		t.Fatalf("prune removed the wrong record")
	}
}
