package rules

import (
	"strings"
	"testing"
)

func p(n int) *int { return &n }

func keep(id string, proto string, port int) Rule {
	return Rule{ID: id, Proto: proto, PublicPort: port, TargetIP: "10.0.0.10", ViaPeer: "site1"}
}

var reserved = []int{22, 51820, 7443}

// testPeers is the peer list the table-driven cases validate against: one
// site-mode peer that covers every private range, and two real-IP peers.
var testPeers = PeerLookup{
	"site1": {ID: "site1", Mode: "site", TunnelIP: "10.66.66.3",
		LANCIDRs: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}},
	"site2": {ID: "site2", Mode: "site", TunnelIP: "10.66.66.4",
		LANCIDRs: []string{"192.0.2.0/24"}}, // documentation range: nothing private in it
	"wings1": {ID: "wings1", Mode: "real", TunnelIP: "10.66.66.2"},
	"wings2": {ID: "wings2", Mode: "real", TunnelIP: "10.66.66.5"},
}

func reasons(rej []Rejection) map[string]string {
	m := map[string]string{}
	for _, r := range rej {
		m[r.ID] = r.Reason
	}
	return m
}

func TestValidatePerRule(t *testing.T) {
	cases := []struct {
		name   string
		rule   Rule
		reject string // substring of the expected reason; "" means accepted
	}{
		{"plain tcp", keep("a", "tcp", 9000), ""},
		{"proto both", keep("a", "both", 9000), ""},
		{"proto udp", keep("a", "udp", 9000), ""},
		{"empty id", Rule{Proto: "tcp", PublicPort: 9000, TargetIP: "10.0.0.10", ViaPeer: "site1"}, "id is required"},
		{"bad proto", keep("a", "sctp", 9000), "must be tcp, udp or both"},
		{"port zero", keep("a", "tcp", 0), "out of range"},
		{"port too high", keep("a", "tcp", 65536), "out of range"},
		{"port 1 ok", keep("a", "tcp", 1), ""},
		{"port 65535 ok", keep("a", "tcp", 65535), ""},
		{"end below start", Rule{ID: "a", Proto: "tcp", PublicPort: 9100, PublicPortEnd: p(9000), TargetIP: "10.0.0.10", ViaPeer: "site1"}, "below public_port"},
		{"end equal start", Rule{ID: "a", Proto: "tcp", PublicPort: 9000, PublicPortEnd: p(9000), TargetIP: "10.0.0.10", ViaPeer: "site1"}, ""},
		{"end out of range", Rule{ID: "a", Proto: "tcp", PublicPort: 9000, PublicPortEnd: p(70000), TargetIP: "10.0.0.10", ViaPeer: "site1"}, "out of range"},
		{"valid range", Rule{ID: "a", Proto: "both", PublicPort: 9000, PublicPortEnd: p(9100), TargetIP: "10.0.0.10", ViaPeer: "site1"}, ""},
		{"range remap refused", Rule{ID: "a", Proto: "tcp", PublicPort: 9000, PublicPortEnd: p(9100), TargetIP: "10.0.0.10", ViaPeer: "site1", TargetPort: p(25565)}, "cannot be remapped"},
		{"single remap ok", Rule{ID: "a", Proto: "tcp", PublicPort: 9000, TargetIP: "10.0.0.10", ViaPeer: "site1", TargetPort: p(25565)}, ""},
		{"remap port out of range", Rule{ID: "a", Proto: "tcp", PublicPort: 9000, TargetIP: "10.0.0.10", ViaPeer: "site1", TargetPort: p(0)}, "target_port 0 out of range"},
		{"reserved ssh", keep("a", "tcp", 22), "reserved"},
		{"reserved wg", keep("a", "udp", 51820), "reserved"},
		{"reserved agent port", keep("a", "tcp", 7443), "reserved"},
		{"range overlapping reserved", Rule{ID: "a", Proto: "tcp", PublicPort: 7400, PublicPortEnd: p(7500), TargetIP: "10.0.0.10", ViaPeer: "site1"}, "public port 7443 is reserved"},
		{"range just below reserved", Rule{ID: "a", Proto: "tcp", PublicPort: 7400, PublicPortEnd: p(7442), TargetIP: "10.0.0.10", ViaPeer: "site1"}, ""},
		{"public target", Rule{ID: "a", Proto: "tcp", PublicPort: 9000, TargetIP: "8.8.8.8", ViaPeer: "site1"}, "not a private"},
		{"loopback target", Rule{ID: "a", Proto: "tcp", PublicPort: 9000, TargetIP: "127.0.0.1", ViaPeer: "site1"}, "not a private"},
		{"ipv6 target", Rule{ID: "a", Proto: "tcp", PublicPort: 9000, TargetIP: "fd00::1", ViaPeer: "site1"}, "not a private"},
		{"empty target", Rule{ID: "a", Proto: "tcp", PublicPort: 9000, TargetIP: "", ViaPeer: "site1"}, "no target"},
		{"172.15 is public", Rule{ID: "a", Proto: "tcp", PublicPort: 9000, TargetIP: "172.15.0.1", ViaPeer: "site1"}, "not a private"},
		{"172.16 is private", Rule{ID: "a", Proto: "tcp", PublicPort: 9000, TargetIP: "172.16.0.1", ViaPeer: "site1"}, ""},
		{"172.31 is private", Rule{ID: "a", Proto: "tcp", PublicPort: 9000, TargetIP: "172.31.255.254", ViaPeer: "site1"}, ""},
		{"172.32 is public", Rule{ID: "a", Proto: "tcp", PublicPort: 9000, TargetIP: "172.32.0.1", ViaPeer: "site1"}, "not a private"},
		{"10/8 private", Rule{ID: "a", Proto: "tcp", PublicPort: 9000, TargetIP: "10.1.2.3", ViaPeer: "site1"}, ""},
		{"0.0.0.0 refused", Rule{ID: "a", Proto: "tcp", PublicPort: 9000, TargetIP: "0.0.0.0", ViaPeer: "site1"}, "not a private"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Validate([]Rule{tc.rule}, reserved, testPeers)
			if tc.reject == "" {
				if len(got) != 0 {
					t.Fatalf("expected accepted, got %+v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("expected exactly 1 rejection, got %+v", got)
			}
			if !strings.Contains(got[0].Reason, tc.reject) {
				t.Fatalf("reason %q does not contain %q", got[0].Reason, tc.reject)
			}
		})
	}
}

func TestValidateSets(t *testing.T) {
	cases := []struct {
		name     string
		rules    []Rule
		rejected []string // ids expected to be rejected
	}{
		{
			name:  "two unrelated rules",
			rules: []Rule{keep("a", "tcp", 9000), keep("b", "tcp", 9001)},
		},
		{
			name:  "same port different proto is fine",
			rules: []Rule{keep("a", "tcp", 9000), keep("b", "udp", 9000)},
		},
		{
			name:  "identical duplicates are accepted",
			rules: []Rule{keep("a", "tcp", 9000), keep("a", "tcp", 9000)},
		},
		{
			name:     "same port different target rejects both",
			rules:    []Rule{keep("a", "tcp", 9000), {ID: "b", Proto: "tcp", PublicPort: 9000, TargetIP: "10.0.0.11", ViaPeer: "site1"}},
			rejected: []string{"a", "b"},
		},
		{
			name:     "both overlaps tcp",
			rules:    []Rule{keep("a", "both", 9000), {ID: "b", Proto: "tcp", PublicPort: 9000, TargetIP: "10.0.0.11", ViaPeer: "site1"}},
			rejected: []string{"a", "b"},
		},
		{
			name:     "overlapping ranges rejected even with the same target",
			rules:    []Rule{{ID: "a", Proto: "tcp", PublicPort: 9000, PublicPortEnd: p(9100), TargetIP: "10.0.0.10", ViaPeer: "site1"}, {ID: "b", Proto: "tcp", PublicPort: 9050, PublicPortEnd: p(9150), TargetIP: "10.0.0.10", ViaPeer: "site1"}},
			rejected: []string{"a", "b"},
		},
		{
			name:  "adjacent ranges are fine",
			rules: []Rule{{ID: "a", Proto: "tcp", PublicPort: 9000, PublicPortEnd: p(9100), TargetIP: "10.0.0.10", ViaPeer: "site1"}, {ID: "b", Proto: "tcp", PublicPort: 9101, PublicPortEnd: p(9200), TargetIP: "10.0.0.10", ViaPeer: "site1"}},
		},
		{
			name:     "single port inside a range rejects both",
			rules:    []Rule{{ID: "a", Proto: "tcp", PublicPort: 9000, PublicPortEnd: p(9100), TargetIP: "10.0.0.10", ViaPeer: "site1"}, keep("b", "tcp", 9050)},
			rejected: []string{"a", "b"},
		},
		{
			name:     "remap and keep on the same port reject both",
			rules:    []Rule{keep("a", "tcp", 9000), {ID: "b", Proto: "tcp", PublicPort: 9000, TargetIP: "10.0.0.10", ViaPeer: "site1", TargetPort: p(25565)}},
			rejected: []string{"a", "b"},
		},
		{
			name:  "identical remaps are accepted",
			rules: []Rule{{ID: "a", Proto: "tcp", PublicPort: 9000, TargetIP: "10.0.0.10", ViaPeer: "site1", TargetPort: p(25565)}, {ID: "a", Proto: "tcp", PublicPort: 9000, TargetIP: "10.0.0.10", ViaPeer: "site1", TargetPort: p(25565)}},
		},
		{
			name:     "same id different definition",
			rules:    []Rule{keep("a", "tcp", 9000), keep("a", "tcp", 9001)},
			rejected: []string{"a"},
		},
		{
			name:     "an invalid rule does not hide a conflict between the others",
			rules:    []Rule{keep("bad", "tcp", 22), keep("a", "tcp", 9000), {ID: "b", Proto: "tcp", PublicPort: 9000, TargetIP: "10.0.0.5", ViaPeer: "site1"}},
			rejected: []string{"bad", "a", "b"},
		},
		{
			name:     "three-way pile-up on one port",
			rules:    []Rule{keep("a", "tcp", 9000), {ID: "b", Proto: "tcp", PublicPort: 9000, TargetIP: "10.0.0.5", ViaPeer: "site1"}, {ID: "c", Proto: "tcp", PublicPort: 9000, TargetIP: "10.0.0.6", ViaPeer: "site1"}},
			rejected: []string{"a", "b", "c"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Validate(tc.rules, reserved, testPeers)
			m := reasons(got)
			if len(m) != len(tc.rejected) {
				t.Fatalf("expected %d rejected ids %v, got %+v", len(tc.rejected), tc.rejected, got)
			}
			for _, id := range tc.rejected {
				if _, ok := m[id]; !ok {
					t.Fatalf("expected %q rejected, got %+v", id, got)
				}
			}
		})
	}
}

func TestMaxRules(t *testing.T) {
	mk := func(n int) []Rule {
		out := make([]Rule, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, keep("r", "tcp", 10000+i))
		}
		// give every rule its own id
		for i := range out {
			out[i].ID = "r" + strings.Repeat("x", i%3) + itoa(i)
		}
		return out
	}
	if rej := Validate(mk(MaxRules), reserved, testPeers); len(rej) != 0 {
		t.Fatalf("exactly MaxRules should be accepted, got %d rejections (%v)", len(rej), rej[0])
	}
	rej := Validate(mk(MaxRules+1), reserved, testPeers)
	if len(rej) != 1 || !strings.Contains(rej[0].Reason, "too many rules") {
		t.Fatalf("expected a single too-many-rules rejection, got %+v", rej)
	}
	if rej[0].ID != "" {
		t.Fatalf("whole-request rejection should have an empty id, got %q", rej[0].ID)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestEmptySetIsValid(t *testing.T) {
	if rej := Validate(nil, reserved, testPeers); len(rej) != 0 {
		t.Fatalf("an empty rule set must be accepted (it is how you close every port), got %+v", rej)
	}
}

func TestDedupe(t *testing.T) {
	in, bad := Resolve([]Rule{keep("a", "tcp", 9000), keep("a", "tcp", 9000), keep("b", "tcp", 9001)}, testPeers)
	if len(bad) != 0 {
		t.Fatalf("unexpected rejections %+v", bad)
	}
	if got := Dedupe(in); len(got) != 2 {
		t.Fatalf("expected 2 rules after dedupe, got %d", len(got))
	}
}

// --- target selection ------------------------------------------------------

// TestTargetSelection covers the two ways a rule names where it sends traffic,
// and every way of getting that wrong. Picking the wrong one silently is how
// players end up on somebody else's server, so each mistake has its own
// message.
func TestTargetSelection(t *testing.T) {
	cases := []struct {
		name   string
		rule   Rule
		reject string
	}{
		{"real-IP peer", Rule{ID: "a", Proto: "tcp", PublicPort: 9000, TargetPeer: "wings1"}, ""},
		{"site target", Rule{ID: "a", Proto: "tcp", PublicPort: 9000, TargetIP: "10.0.0.10", ViaPeer: "site1"}, ""},
		{"no target at all", Rule{ID: "a", Proto: "tcp", PublicPort: 9000}, "no target"},
		{"both kinds of target", Rule{ID: "a", Proto: "tcp", PublicPort: 9000, TargetPeer: "wings1", TargetIP: "10.0.0.10"}, "not both"},
		{"unknown peer", Rule{ID: "a", Proto: "tcp", PublicPort: 9000, TargetPeer: "ghost"}, `target_peer "ghost" is not a known peer`},
		{"unknown via_peer", Rule{ID: "a", Proto: "tcp", PublicPort: 9000, TargetIP: "10.0.0.10", ViaPeer: "ghost"}, `via_peer "ghost" is not a known peer`},
		{"target_ip without via_peer", Rule{ID: "a", Proto: "tcp", PublicPort: 9000, TargetIP: "10.0.0.10"}, "needs via_peer"},
		{"target_peer with via_peer", Rule{ID: "a", Proto: "tcp", PublicPort: 9000, TargetPeer: "wings1", ViaPeer: "site1"}, "via_peer is only used with target_ip"},
		{"target_peer naming a site peer", Rule{ID: "a", Proto: "tcp", PublicPort: 9000, TargetPeer: "site1"}, "is in site mode"},
		{"via_peer naming a real peer", Rule{ID: "a", Proto: "tcp", PublicPort: 9000, TargetIP: "10.0.0.10", ViaPeer: "wings1"}, "is in real-IP mode"},
		{"target outside the peer's ranges", Rule{ID: "a", Proto: "tcp", PublicPort: 9000, TargetIP: "10.0.0.10", ViaPeer: "site2"}, "outside the ranges"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Validate([]Rule{tc.rule}, reserved, testPeers)
			if tc.reject == "" {
				if len(got) != 0 {
					t.Fatalf("expected accepted, got %+v", got)
				}
				return
			}
			if len(got) != 1 || !strings.Contains(got[0].Reason, tc.reject) {
				t.Fatalf("expected a rejection containing %q, got %+v", tc.reject, got)
			}
		})
	}
}

// TestResolveProducesTheRightAddressAndMasqueradeFlag is the check that keeps
// real-IP mode real: a rule pointed at a peer must resolve to that peer's
// tunnel address AND be marked Direct, because Direct is what keeps the
// masquerade rule from rewriting the player's source address.
func TestResolveProducesTheRightAddressAndMasqueradeFlag(t *testing.T) {
	rs := []Rule{
		{ID: "a", Proto: "tcp", PublicPort: 9000, TargetPeer: "wings1"},
		{ID: "b", Proto: "tcp", PublicPort: 9001, TargetIP: "10.0.0.10", ViaPeer: "site1"},
	}
	got, bad := Resolve(rs, testPeers)
	if len(bad) != 0 {
		t.Fatalf("unexpected rejections %+v", bad)
	}
	if got[0].IP != "10.66.66.2" || !got[0].Direct {
		t.Fatalf("a target_peer rule must resolve to the peer's tunnel IP and be direct, got %+v", got[0])
	}
	if got[1].IP != "10.0.0.10" || got[1].Direct {
		t.Fatalf("a site rule must resolve to the LAN address and be masqueraded, got %+v", got[1])
	}
}

// TestResolveRejectsRulesForADeletedPeer is what a stored rule set looks like
// after the admin removes a node from the panel.
func TestResolveRejectsRulesForADeletedPeer(t *testing.T) {
	rs := []Rule{
		{ID: "gone", Proto: "tcp", PublicPort: 9000, TargetPeer: "deleted"},
		{ID: "ok", Proto: "tcp", PublicPort: 9001, TargetPeer: "wings1"},
	}
	got, bad := Resolve(rs, testPeers)
	if len(got) != 1 || got[0].ID != "ok" {
		t.Fatalf("the surviving rule should still resolve, got %+v", got)
	}
	if len(bad) != 1 || bad[0].ID != "gone" {
		t.Fatalf("expected the stale rule rejected, got %+v", bad)
	}
}

// TestTwoPeersCanShareAPublicPortOnlyIfDisjoint: the same public port cannot
// go to two different peers, even though both are valid targets.
func TestSamePortToTwoPeersIsRejected(t *testing.T) {
	rs := []Rule{
		{ID: "a", Proto: "tcp", PublicPort: 9000, TargetPeer: "wings1"},
		{ID: "b", Proto: "tcp", PublicPort: 9000, TargetPeer: "wings2"},
	}
	got := Validate(rs, reserved, testPeers)
	if len(got) != 2 {
		t.Fatalf("both rules must be rejected, got %+v", got)
	}
}

// A direct forward and a site forward that happen to land on the same address
// are still different rules: one masquerades and one does not, so they must
// not be treated as duplicates of each other.
func TestDirectAndSiteToTheSameAddressConflict(t *testing.T) {
	peers := PeerLookup{
		"wings1": {ID: "wings1", Mode: "real", TunnelIP: "10.0.0.10"},
		"site1":  {ID: "site1", Mode: "site", LANCIDRs: []string{"10.0.0.0/24"}},
	}
	rs := []Rule{
		{ID: "a", Proto: "tcp", PublicPort: 9000, TargetPeer: "wings1"},
		{ID: "b", Proto: "tcp", PublicPort: 9000, TargetIP: "10.0.0.10", ViaPeer: "site1"},
	}
	got := Validate(rs, reserved, peers)
	if len(got) != 2 {
		t.Fatalf("the same address reached two different ways is still a conflict, got %+v", got)
	}
	if !strings.Contains(got[0].Reason, "different target") {
		t.Fatalf("expected a different-target conflict, got %q", got[0].Reason)
	}
}
