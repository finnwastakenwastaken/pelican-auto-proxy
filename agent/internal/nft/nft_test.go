package nft

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/rules"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

func p(n int) *int { return &n }

// testPeers: two Wings hosts in real-IP mode and one site-mode client that
// forwards on to other machines on its LAN.
var testPeers = rules.PeerLookup{
	"wings1": {ID: "wings1", Mode: "real", TunnelIP: "10.66.66.2"},
	"wings2": {ID: "wings2", Mode: "real", TunnelIP: "10.66.66.3"},
	"site1":  {ID: "site1", Mode: "site", TunnelIP: "10.66.66.4", LANCIDRs: []string{"10.0.0.0/24", "172.16.4.0/24"}},
}

// directTargets is what Render puts in @direct_targets: every real-IP peer's
// tunnel address, whether or not a rule currently points at it.
var directTargets = []string{"10.66.66.2", "10.66.66.3"}

func site(id, proto string, port int) rules.Rule {
	return rules.Rule{ID: id, Proto: proto, PublicPort: port, TargetIP: "10.0.0.10", ViaPeer: "site1"}
}

var goldens = map[string][]rules.Rule{
	"empty": nil,
	"single-keep": {
		{ID: "alloc-1", Proto: "both", PublicPort: 9445, TargetIP: "10.0.0.10", ViaPeer: "site1", Note: "Palworld"},
	},
	"range": {
		{ID: "alloc-2", Proto: "both", PublicPort: 9400, PublicPortEnd: p(9899), TargetIP: "10.0.0.10", ViaPeer: "site1", Note: "batch"},
	},
	"remap": {
		{ID: "manual-1", Proto: "tcp", PublicPort: 443, TargetIP: "10.0.0.11", ViaPeer: "site1", TargetPort: p(8443), Note: "panel"},
	},
	// real-ip: every forward goes straight to a peer's tunnel address, so
	// @direct_targets covers all of them and nothing is masqueraded.
	"real-ip": {
		{ID: "alloc-1", Proto: "both", PublicPort: 9445, TargetPeer: "wings1", Note: "Palworld, real player IPs"},
		{ID: "alloc-2", Proto: "both", PublicPort: 27015, TargetPeer: "wings2", Note: "second node host"},
		{ID: "alloc-3", Proto: "tcp", PublicPort: 2022, TargetPeer: "wings1", TargetPort: p(2022), Note: "Wings SFTP"},
	},
	"mixed": {
		{ID: "manual-1", Proto: "tcp", PublicPort: 443, TargetIP: "10.0.0.11", ViaPeer: "site1", TargetPort: p(8443), Note: "panel"},
		{ID: "manual-2", Proto: "tcp", PublicPort: 2022, TargetIP: "10.0.0.10", ViaPeer: "site1", TargetPort: p(2022), Note: "sftp"},
		{ID: "manual-3", Proto: "udp", PublicPort: 27015, TargetIP: "10.0.0.7", ViaPeer: "site1", Note: "source query"},
		{ID: "alloc-1", Proto: "both", PublicPort: 9445, TargetPeer: "wings1", Note: "Palworld, real player IPs"},
		{ID: "alloc-2", Proto: "both", PublicPort: 9400, PublicPortEnd: p(9420), TargetPeer: "wings2"},
		{ID: "alloc-3", Proto: "udp", PublicPort: 8211, TargetIP: "172.16.4.9", ViaPeer: "site1"},
		{ID: "alloc-1", Proto: "both", PublicPort: 9445, TargetPeer: "wings1", Note: "Palworld, real player IPs"}, // exact duplicate
	},
}

// resolve is the pipeline the agent runs before rendering. A golden built any
// other way would not prove the renderer gets what the API gives it.
func resolve(t *testing.T, rs []rules.Rule) []rules.Resolved {
	t.Helper()
	got, bad := rules.Resolve(rs, testPeers)
	if len(bad) != 0 {
		t.Fatalf("golden fixture does not resolve: %+v", bad)
	}
	return got
}

func TestRenderGolden(t *testing.T) {
	for name, rs := range goldens {
		t.Run(name, func(t *testing.T) {
			got := Render(resolve(t, rs), "eth0", "wg0", directTargets, "10.66.66.1")
			path := filepath.Join("testdata", name+".nft")
			if *update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("missing golden file (run: go test ./internal/nft -update): %v", err)
			}
			if got != string(want) {
				t.Errorf("rendered output differs from %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
			}
		})
	}
}

func TestRenderIsATransaction(t *testing.T) {
	got := Render(resolve(t, goldens["mixed"]), "eth0", "wg0", directTargets, "10.66.66.1")
	lines := strings.Split(got, "\n")
	var meaningful []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		meaningful = append(meaningful, l)
	}
	if meaningful[0] != "table inet autoproxy_rules" {
		t.Fatalf("first statement must create the table if missing, got %q", meaningful[0])
	}
	if meaningful[1] != "delete table inet autoproxy_rules" {
		// "flush table" leaves map elements behind: ports that should have
		// closed stay open. See the package doc.
		t.Fatalf("second statement must delete the table, got %q", meaningful[1])
	}
	if meaningful[2] != "table inet autoproxy_rules {" {
		t.Fatalf("third statement must redefine the table, got %q", meaningful[2])
	}
}

func TestRenderDeduplicatesAndSorts(t *testing.T) {
	rs := []rules.Rule{
		{ID: "b", Proto: "tcp", PublicPort: 9100, TargetIP: "10.0.0.10", ViaPeer: "site1"},
		{ID: "a", Proto: "tcp", PublicPort: 9000, TargetIP: "10.0.0.10", ViaPeer: "site1"},
		{ID: "a", Proto: "tcp", PublicPort: 9000, TargetIP: "10.0.0.10", ViaPeer: "site1"},
	}
	got := Render(resolve(t, rs), "eth0", "wg0", nil, "10.66.66.1")
	if strings.Count(got, "9000 : 10.0.0.10") != 1 {
		t.Fatalf("duplicate element rendered twice:\n%s", got)
	}
	if strings.Index(got, "9000 :") > strings.Index(got, "9100 :") {
		t.Fatalf("elements are not sorted by port:\n%s", got)
	}
}

func TestRenderUsesTheGivenInterfaces(t *testing.T) {
	got := Render(nil, "ens3", "wgx", nil, "10.66.66.1")
	if !strings.Contains(got, `iifname "ens3"`) || !strings.Contains(got, `oifname "wgx"`) {
		t.Fatalf("interfaces not substituted:\n%s", got)
	}
}

func TestCount(t *testing.T) {
	c := Count(resolve(t, goldens["mixed"]))
	// tcp: 443 remap, 2022 remap, 9445 (both), 9400-9420 (both) = 4
	// udp: 27015, 8211, 9445 (both), 9400-9420 (both) = 4
	// rules: 6 after dropping the exact duplicate
	if c.TCP != 4 || c.UDP != 4 || c.Rules != 6 {
		t.Fatalf("unexpected counts %+v", c)
	}
}

func TestEmptyRenderHasNoElements(t *testing.T) {
	got := Render(nil, "eth0", "wg0", nil, "10.66.66.1")
	if strings.Contains(got, "elements") {
		t.Fatalf("an empty rule set must not emit an elements block:\n%s", got)
	}
	for _, m := range []string{"tcp_addr", "udp_addr", "tcp_remap", "udp_remap"} {
		if !strings.Contains(got, "map "+m+" {") {
			t.Fatalf("map %s missing from the empty ruleset:\n%s", m, got)
		}
	}
	if !strings.Contains(got, "set direct_targets {") {
		t.Fatalf("direct_targets must exist even when empty, or the masquerade rule cannot reference it:\n%s", got)
	}
}

// TestRealIPTargetsAreNotMasqueraded is the renderer half of the headline
// feature. If the masquerade rule ever loses its "!= @direct_targets"
// exclusion, every player arrives at the game server as the VPS, IP bans and
// per-player limits stop working, and nothing else looks broken.
func TestRealIPTargetsAreNotMasqueraded(t *testing.T) {
	got := Render(resolve(t, goldens["real-ip"]), "eth0", "wg0", directTargets, "10.66.66.1")
	if !strings.Contains(got, `oifname "wg0" ip daddr != @direct_targets masquerade`) {
		t.Fatalf("the masquerade rule must exclude @direct_targets:\n%s", got)
	}
	if strings.Contains(got, `oifname "wg0" masquerade`) {
		t.Fatalf("an unconditional masquerade would hide every player's IP:\n%s", got)
	}
	for _, ip := range directTargets {
		if !strings.Contains(got, ip) {
			t.Fatalf("real-IP peer %s missing from direct_targets:\n%s", ip, got)
		}
	}
}

// Every real-IP peer belongs in the set, not only the ones a rule currently
// points at: a peer with no forwards yet still must not be masqueraded the
// moment its first rule lands.
func TestDirectTargetsCoverPeersWithoutRules(t *testing.T) {
	got := Render(nil, "eth0", "wg0", []string{"10.66.66.9"}, "10.66.66.1")
	if !strings.Contains(got, "elements = { 10.66.66.9 }") {
		t.Fatalf("direct_targets must list peers even with no rules:\n%s", got)
	}
}

func TestApplyKeepsTheOldFileWhenNftRejects(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.nft")
	if err := os.WriteFile(path, []byte("previous\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	// /bin/false stands in for an nft that refuses the ruleset.
	a := Applier{Bin: "/bin/false"}
	err := a.Apply(path, "anything\n")
	if err == nil {
		t.Fatal("expected an error when nft -c fails")
	}
	b, rerr := os.ReadFile(path)
	if rerr != nil || string(b) != "previous\n" {
		t.Fatalf("the previous ruleset file must survive a failed apply, got %q (%v)", string(b), rerr)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

func TestApplyInstallsTheFileOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.nft")
	a := Applier{Bin: "/bin/true"} // stands in for a happy nft
	if err := a.Apply(path, "hello\n"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "hello\n" {
		t.Fatalf("expected the new ruleset in place, got %q (%v)", string(b), err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

func TestForwardPolicyIsDrop(t *testing.T) {
	// Nothing crosses this box unless we DNAT'd it. Every base chain at a hook
	// must accept for a packet to pass, so this policy is only safe because
	// setup removes orphaned Docker tables that also sit at the forward hook.
	got := Render(resolve(t, goldens["mixed"]), "eth0", "wg0", directTargets, "10.66.66.1")
	if !strings.Contains(got, "type filter hook forward priority filter; policy drop;") {
		t.Fatalf("forward chain must have policy drop:\n%s", got)
	}
	if strings.Contains(got, "hook forward priority filter; policy accept") {
		t.Fatalf("forward policy accept would forward traffic we never DNAT'd:\n%s", got)
	}
	// With policy drop these two accepts are the entire allow list.
	if !strings.Contains(got, "ct state established,related accept") {
		t.Fatalf("return traffic would be dropped without an established accept:\n%s", got)
	}
	if !strings.Contains(got, `iifname "eth0" oifname "wg0" ct status dnat accept`) {
		t.Fatalf("new DNAT'd sessions would be dropped without this accept:\n%s", got)
	}
}

// The tunnel check-in route has no token; what makes that safe is that only a
// packet that came through the tunnel can be addressed to the tunnel address.
// If this chain loses the drop, or starts dropping the tunnel interface
// itself, either the guard is gone or every check-in is.
func TestTunnelGuardDropsTunnelAddressFromOutsideTheTunnel(t *testing.T) {
	got := Render(nil, "eth0", "wg0", nil, "10.66.66.1")
	want := `ip daddr 10.66.66.1 iifname != { "lo", "wg0" } drop`
	if !strings.Contains(got, "chain tunnel_guard {") || !strings.Contains(got, want) {
		t.Fatalf("tunnel_guard missing or wrong, want %q in:\n%s", want, got)
	}
	if !strings.Contains(got, "type filter hook input priority filter - 10; policy accept;") {
		t.Fatalf("tunnel_guard must be an input chain with policy accept (it only ever adds a drop):\n%s", got)
	}
	if strings.Contains(Render(nil, "eth0", "wg0", nil, ""), "tunnel_guard") {
		t.Fatalf("no guard address must mean no guard chain")
	}
}
