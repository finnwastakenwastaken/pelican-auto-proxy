package wg

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

type recorder struct {
	cmds []string
	out  string
	err  error
	// failOn, when set, limits out/err to commands containing this
	// substring. Without it a recorder fails every command alike, which
	// cannot tell "the step we expected to fail did" from "everything
	// failed, including the step under test".
	failOn string
}

func (r *recorder) run(name string, args ...string) (string, error) {
	cmd := name + " " + strings.Join(args, " ")
	r.cmds = append(r.cmds, cmd)
	if r.failOn != "" && !strings.Contains(cmd, r.failOn) {
		return "", nil
	}
	return r.out, r.err
}

func TestSetPeerJoinsAllowedIPsWithCommas(t *testing.T) {
	r := &recorder{}
	m := Manager{Run: r.run}
	if err := m.SetPeer("pubkey-1", []string{"10.66.66.2/32", "10.0.0.0/24"}); err != nil {
		t.Fatal(err)
	}
	want := "wg set wg0 peer pubkey-1 allowed-ips 10.66.66.2/32,10.0.0.0/24"
	if r.cmds[0] != want {
		t.Fatalf("got %q, want %q", r.cmds[0], want)
	}
}

// wg silently accepts an empty allowed-ips argument, producing a peer that can
// neither send nor receive while looking configured.
func TestSetPeerRefusesEmptyInput(t *testing.T) {
	m := Manager{Run: (&recorder{}).run}
	if err := m.SetPeer("pubkey-1", nil); err == nil {
		t.Fatal("expected an error for no allowed-ips")
	}
	if err := m.SetPeer("", []string{"10.66.66.2/32"}); err == nil {
		t.Fatal("expected an error for an empty public key")
	}
}

func TestRoutesUseReplaceAndTolerateAMissingRoute(t *testing.T) {
	r := &recorder{}
	m := Manager{Run: r.run}
	if err := m.AddRoute("10.0.0.0/24"); err != nil {
		t.Fatal(err)
	}
	// A peer route must never land in the main table (that's the routing-loop
	// bug this package exists to prevent): AddRoute first tries to migrate
	// away any route a previous agent version left in main, then "replace"s
	// the CIDR into the dedicated table. "replace" rather than "add":
	// re-applying the peer list on every start must not fail because a route
	// is already there.
	wantCmds := []string{
		"ip route del 10.0.0.0/24 dev wg0",
		"ip route replace 10.0.0.0/24 dev wg0 scope link table 201",
	}
	if fmt.Sprint(r.cmds) != fmt.Sprint(wantCmds) {
		t.Fatalf("got %v, want %v", r.cmds, wantCmds)
	}

	gone := &recorder{out: "RTNETLINK answers: No such process", err: fmt.Errorf("exit 2")}
	m2 := Manager{Run: gone.run}
	if err := m2.DelRoute("10.0.0.0/24"); err != nil {
		t.Fatalf("deleting an already-absent route must not be an error: %v", err)
	}

	broken := &recorder{out: "RTNETLINK answers: Operation not permitted", err: fmt.Errorf("exit 2")}
	m3 := Manager{Run: broken.run}
	if err := m3.DelRoute("10.0.0.0/24"); err == nil {
		t.Fatal("a real failure must still be reported")
	}
}

// AddRoute's migration step must ignore whatever the main-table delete
// returns: on a box that never had the old bug, there is nothing to migrate,
// and that must not stop the real route from being added.
func TestAddRouteMigrationFailureIsIgnored(t *testing.T) {
	r := &recorder{out: "RTNETLINK answers: No such process", err: fmt.Errorf("exit 2"), failOn: "route del"}
	m := Manager{Run: r.run}
	if err := m.AddRoute("10.0.0.0/24"); err != nil {
		t.Fatalf("a failed main-table migration delete must not fail AddRoute: %v", err)
	}
	// ... and the real add must still have been attempted, which is the part
	// a recorder that fails everything alike cannot show.
	if r.cmds[len(r.cmds)-1] != "ip route replace 10.0.0.0/24 dev wg0 scope link table 201" {
		t.Fatalf("the route was not added after the migration delete failed: %v", r.cmds)
	}
}

// The inverse: a failing add IS an error. Without this, the test above would
// still pass if AddRoute swallowed every error it saw.
func TestAddRouteReportsARealFailure(t *testing.T) {
	r := &recorder{out: "RTNETLINK answers: Operation not permitted", err: fmt.Errorf("exit 2"), failOn: "route replace"}
	m := Manager{Run: r.run}
	if err := m.AddRoute("10.0.0.0/24"); err == nil {
		t.Fatal("a failing route add must be reported, not swallowed with the migration delete")
	}
}

func TestDelRouteTargetsTheDedicatedTable(t *testing.T) {
	r := &recorder{}
	m := Manager{Run: r.run}
	if err := m.DelRoute("10.0.0.0/24"); err != nil {
		t.Fatal(err)
	}
	if r.cmds[0] != "ip route del 10.0.0.0/24 dev wg0 table 201" {
		t.Fatalf("got %q", r.cmds[0])
	}
}

// Captured verbatim from "ip rule show" in the gate container after
// "ip rule add not fwmark 0x2b lookup 201 priority 90". The selector order is
// NOT the order the rule is written in, and an implied "from all" appears in
// the middle; a test that invents this string instead of copying it is how
// the idempotency check shipped broken.
const realIPRuleShow = `0:	from all lookup local
90:	not from all fwmark 0x2b lookup 201
32766:	from all lookup main
32767:	from all lookup default`

// Rules that are nearly ours must not be mistaken for ours: deleting somebody
// else's rule on uninstall would be worse than leaving ours behind.
func TestRuleMatchingIsNotFooledByNeighbours(t *testing.T) {
	m := Manager{}
	if !m.hasRule(realIPRuleShow) {
		t.Fatal("the real iproute2 output must be recognised")
	}
	for _, out := range []string{
		"90:\tfrom all fwmark 0x2b lookup 201",  // not inverted: someone else's
		"90:\tnot from all fwmark 0x2b lookup 200", // another table
		"90:\tnot from all fwmark 0x2a lookup 201", // the client's mark
		"90:\tnot from all fwmark 0x2bc lookup 201", // longer mark, must not prefix-match
		"0:\tfrom all lookup local",
		"",
	} {
		if m.hasRule(out) {
			t.Errorf("must not match %q", out)
		}
	}
}

func TestEnsureFWMarkSetsTheInterfaceMark(t *testing.T) {
	r := &recorder{}
	m := Manager{Run: r.run}
	if err := m.EnsureFWMark(); err != nil {
		t.Fatal(err)
	}
	if r.cmds[0] != "wg set wg0 fwmark 0x2b" {
		t.Fatalf("got %q", r.cmds[0])
	}
}

// EnsureRoutingRule must not add a second copy of the rule: "ip rule add" has
// no "replace" form, so running it twice leaves two identical rules rather
// than failing loudly.
func TestEnsureRoutingRuleIsIdempotent(t *testing.T) {
	present := &recorder{out: realIPRuleShow}
	m := Manager{Run: present.run}
	if err := m.EnsureRoutingRule(); err != nil {
		t.Fatal(err)
	}
	// The check ("ip rule show") always runs; only the mutating add must be
	// skipped when the rule is already there.
	wantPresent := []string{"ip rule show"}
	if fmt.Sprint(present.cmds) != fmt.Sprint(wantPresent) {
		t.Fatalf("the rule was already present; expected only the check, got %v", present.cmds)
	}

	absent := &recorder{}
	m2 := Manager{Run: absent.run}
	if err := m2.EnsureRoutingRule(); err != nil {
		t.Fatal(err)
	}
	want := []string{"ip rule show", "ip rule add not fwmark 0x2b lookup 201 priority 90"}
	if fmt.Sprint(absent.cmds) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", absent.cmds, want)
	}
}

// If the check cannot recognise the rule (a named routing table, say) the add
// fails with "File exists". The rule IS installed, so that must not abort the
// peer restore.
func TestEnsureRoutingRuleToleratesAnAlreadyPresentRule(t *testing.T) {
	r := &recorder{out: "RTNETLINK answers: File exists", err: fmt.Errorf("exit 2"), failOn: "rule add"}
	m := Manager{Run: r.run}
	if err := m.EnsureRoutingRule(); err != nil {
		t.Fatalf("an already-present rule must not be an error: %v", err)
	}
	// Any other failure still is one.
	r2 := &recorder{out: "RTNETLINK answers: Operation not permitted", err: fmt.Errorf("exit 2"), failOn: "rule add"}
	if err := (Manager{Run: r2.run}).EnsureRoutingRule(); err == nil {
		t.Fatal("a real ip rule failure must be reported")
	}
}

func TestRemoveRoutingRuleIsIdempotent(t *testing.T) {
	absent := &recorder{}
	m := Manager{Run: absent.run}
	if err := m.RemoveRoutingRule(); err != nil {
		t.Fatal(err)
	}
	wantAbsent := []string{"ip rule show"}
	if fmt.Sprint(absent.cmds) != fmt.Sprint(wantAbsent) {
		t.Fatalf("the rule was never there; expected only the check, got %v", absent.cmds)
	}

	present := &recorder{out: realIPRuleShow}
	m2 := Manager{Run: present.run}
	if err := m2.RemoveRoutingRule(); err != nil {
		t.Fatal(err)
	}
	want := []string{"ip rule show", "ip rule del not fwmark 0x2b lookup 201 priority 90"}
	if fmt.Sprint(present.cmds) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", present.cmds, want)
	}
}

// A custom fwmark/table/priority (tests only; production uses the fixed
// defaults) must be reflected consistently across every command.
func TestCustomFWMarkTableAndPriority(t *testing.T) {
	r := &recorder{}
	m := Manager{Run: r.run, FWMark: "0x99", Table: "250", RulePriority: "42"}
	if err := m.EnsureFWMark(); err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureRoutingRule(); err != nil {
		t.Fatal(err)
	}
	if err := m.AddRoute("10.0.0.0/24"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"wg set wg0 fwmark 0x99",
		"ip rule show",
		"ip rule add not fwmark 0x99 lookup 250 priority 42",
		"ip route del 10.0.0.0/24 dev wg0",
		"ip route replace 10.0.0.0/24 dev wg0 scope link table 250",
	}
	if fmt.Sprint(r.cmds) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", r.cmds, want)
	}
}

func TestDryRunIssuesNothing(t *testing.T) {
	r := &recorder{}
	m := Manager{Run: r.run, DryRun: true}
	if err := m.SetPeer("pubkey-1", []string{"10.66.66.2/32"}); err != nil {
		t.Fatal(err)
	}
	if err := m.AddRoute("10.0.0.0/24"); err != nil {
		t.Fatal(err)
	}
	if len(r.cmds) != 0 {
		t.Fatalf("dry run must touch nothing, ran: %v", r.cmds)
	}
}

func TestParsePeerStats(t *testing.T) {
	now := time.Unix(1700000000, 0)
	dump := "ifacekey\tifacepub\t51820\toff\n" +
		"pubkey-1\t(none)\t198.51.100.7:51820\t10.66.66.2/32\t1699999940\t1024\t2048\t25\n" +
		"pubkey-2\t(none)\t(none)\t10.66.66.3/32\t0\t0\t0\toff\n"
	stats := ParsePeerStats(dump, now)
	if len(stats) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(stats))
	}
	a := stats["pubkey-1"]
	if a.HandshakeAge == nil || *a.HandshakeAge != 60 || a.RX != 1024 || a.TX != 2048 {
		t.Fatalf("peer 1 wrong: %+v", a)
	}
	b := stats["pubkey-2"]
	if b.HandshakeAge != nil {
		// A peer that has never handshaked must read as "never", not as
		// "handshaked in 1970": the plugin shows this to the admin.
		t.Fatalf("a peer that never handshaked must have a nil age, got %+v", b.HandshakeAge)
	}
}

func TestParsePeerStatsIgnoresRubbish(t *testing.T) {
	if got := ParsePeerStats("", time.Now()); len(got) != 0 {
		t.Fatalf("expected no peers, got %+v", got)
	}
	if got := ParsePeerStats("iface\nshort\tline\n", time.Now()); len(got) != 0 {
		t.Fatalf("a truncated line must be skipped, got %+v", got)
	}
}
