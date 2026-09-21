package peers

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/wg"
)

// fakeWG records every wg and ip command instead of running it. The whole
// point of this package is which commands it issues, so the test has to see
// them: a manager that keeps perfect state and never calls "wg set" would look
// completely healthy and forward nothing.
type fakeWG struct {
	mu   sync.Mutex
	cmds []string
	fail string // a substring; any command containing it fails
}

func (f *fakeWG) run(name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	line := name + " " + strings.Join(args, " ")
	if f.fail != "" && strings.Contains(line, f.fail) {
		return "boom", fmt.Errorf("command failed")
	}
	f.cmds = append(f.cmds, line)
	return "", nil
}

func (f *fakeWG) has(sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.cmds {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func (f *fakeWG) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.cmds...)
}

// testManager wires a manager to a temp dir and a fake wg. PubKey is derived
// from the private key by the fake, so keys stay readable in failure output.
func testManager(t *testing.T) (*Manager, *fakeWG, Store) {
	t.Helper()
	f := &fakeWG{}
	wgm := wg.Manager{Run: f.run}
	store := Store{Dir: t.TempDir()}
	cfg := Config{
		Subnet:    netip.MustParsePrefix("10.66.66.0/24"),
		VPSIP:     netip.MustParseAddr("10.66.66.1"),
		Endpoint:  "203.0.113.10:51820",
		VPSPubKey: "vps-pubkey",
	}
	m := NewManager(cfg, store, wgm, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.SetClock(func() time.Time { return time.Unix(1700000000, 0).UTC() })
	return m, f, store
}

// stubKeys stands in for "wg genkey | wg pubkey". Readable keys keep failure
// output legible, and a real 44-character WireGuard key literal in this
// repository would trip the secret sweep.
type stubKeys struct{ n int }

func (s *stubKeys) gen() (string, string, error) {
	s.n++
	return fmt.Sprintf("privkey-%d", s.n), fmt.Sprintf("pubkey-%d", s.n), nil
}

func TestCreateAllocatesFromTheTopOfThePool(t *testing.T) {
	m, _, _ := testManager(t)
	m.keygen = (&stubKeys{}).gen

	a, code, err := m.Create("wings-1", ModeReal, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.TunnelIP != "10.66.66.2" {
		t.Fatalf("first peer should get .2 (.1 is the VPS), got %s", a.TunnelIP)
	}
	b, _, err := m.Create("wings-2", ModeReal, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.TunnelIP != "10.66.66.3" {
		t.Fatalf("second peer should get .3, got %s", b.TunnelIP)
	}
	if a.ID == b.ID {
		t.Fatal("peers must get distinct ids")
	}
	if code.ClientAddr != "10.66.66.2/32" {
		t.Fatalf("join code address wrong: %s", code.ClientAddr)
	}
	if code.VPSTunnelIP != "10.66.66.1" || code.Endpoint != "203.0.113.10:51820" {
		t.Fatalf("join code endpoint wrong: %+v", code)
	}
	if code.Keepalive != 25 {
		t.Fatalf("keepalive must be 25: the client dials out and nothing else holds the NAT mapping open")
	}
}

// A peer that exists in state but was never added to the interface forwards
// nothing. These are the two commands that make it real.
func TestCreateAppliesPeerAndRoute(t *testing.T) {
	m, f, _ := testManager(t)
	m.keygen = (&stubKeys{}).gen
	if _, _, err := m.Create("wings-1", ModeReal, nil); err != nil {
		t.Fatal(err)
	}
	if !f.has("wg set wg0 peer pubkey-1 allowed-ips 10.66.66.2/32") {
		t.Fatalf("peer was not added to the interface: %v", f.all())
	}
	if !f.has("ip route replace 10.66.66.2/32 dev wg0 scope link table 201") {
		t.Fatalf("wg never adds routes itself; the agent must: %v", f.all())
	}
}

func TestSiteModeRoutesEveryLANRange(t *testing.T) {
	m, f, _ := testManager(t)
	m.keygen = (&stubKeys{}).gen
	p, code, err := m.Create("lan-box", ModeSite, []string{"10.0.0.0/24", "172.16.4.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if !f.has("allowed-ips 10.66.66.2/32,10.0.0.0/24,172.16.4.0/24") {
		t.Fatalf("allowed-ips must cover the LAN ranges: %v", f.all())
	}
	for _, c := range []string{"10.66.66.2/32", "10.0.0.0/24", "172.16.4.0/24"} {
		if !f.has("ip route replace " + c + " dev wg0 scope link table 201") {
			t.Fatalf("missing route for %s: %v", c, f.all())
		}
	}
	if len(p.LANCIDRs) != 2 || code.Mode != ModeSite {
		t.Fatalf("unexpected peer %+v / code %+v", p, code)
	}
}

func TestLANCIDRValidation(t *testing.T) {
	cases := []struct {
		name   string
		mode   string
		cidrs  []string
		reject string
	}{
		{"real mode, no cidrs", ModeReal, nil, ""},
		{"real mode with cidrs", ModeReal, []string{"10.0.0.0/24"}, "only valid in site mode"},
		{"site mode without cidrs", ModeSite, nil, "at least one lan_cidr"},
		{"public range", ModeSite, []string{"203.0.113.0/24"}, "not a private"},
		{"host bits set", ModeSite, []string{"10.0.0.5/24"}, "host bits set"},
		{"not a cidr", ModeSite, []string{"10.0.0.0"}, "not a valid IPv4 CIDR"},
		{"ipv6", ModeSite, []string{"fd00::/64"}, "not IPv4"},
		{"overlaps the tunnel", ModeSite, []string{"10.66.66.0/25"}, "overlaps the tunnel subnet"},
		{"contains the tunnel", ModeSite, []string{"10.0.0.0/8"}, "overlaps the tunnel subnet"},
		{"two of its own overlap", ModeSite, []string{"192.168.0.0/16", "192.168.1.0/24"}, "overlap each other"},
		{"fine", ModeSite, []string{"192.168.1.0/24", "172.16.0.0/16"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _, _ := testManager(t)
			m.keygen = (&stubKeys{}).gen
			_, _, err := m.Create("p", tc.mode, tc.cidrs)
			if tc.reject == "" {
				if err != nil {
					t.Fatalf("expected accepted, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.reject) {
				t.Fatalf("expected a rejection containing %q, got %v", tc.reject, err)
			}
		})
	}
}

// Two peers claiming the same LAN range give the kernel two answers for one
// address, and which one a packet reaches depends on apply order.
func TestOverlappingCIDRsAcrossPeersAreRefused(t *testing.T) {
	m, _, _ := testManager(t)
	m.keygen = (&stubKeys{}).gen
	if _, _, err := m.Create("a", ModeSite, []string{"192.168.1.0/24"}); err != nil {
		t.Fatal(err)
	}
	_, _, err := m.Create("b", ModeSite, []string{"192.168.1.128/25"})
	if err == nil || !strings.Contains(err.Error(), "already claimed by peer") {
		t.Fatalf("expected an overlap rejection naming the other peer, got %v", err)
	}
	// And the rejected peer must not have consumed an address or been applied.
	if len(m.List()) != 1 {
		t.Fatalf("a rejected peer must not be stored: %+v", m.List())
	}
}

func TestDeleteRemovesPeerAndRoutes(t *testing.T) {
	m, f, store := testManager(t)
	m.keygen = (&stubKeys{}).gen
	p, _, err := m.Create("lan-box", ModeSite, []string{"10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Delete(p.ID); err != nil {
		t.Fatal(err)
	}
	if !f.has("wg set wg0 peer pubkey-1 remove") {
		t.Fatalf("peer not removed from the interface: %v", f.all())
	}
	for _, c := range []string{"10.66.66.2/32", "10.0.0.0/24"} {
		if !f.has("ip route del " + c + " dev wg0 table 201") {
			t.Fatalf("route %s not removed: %v", c, f.all())
		}
	}
	stored, err := store.Load()
	if err != nil || len(stored) != 0 {
		t.Fatalf("peer still in state: %+v %v", stored, err)
	}
}

func TestDeleteUnknownPeer(t *testing.T) {
	m, _, _ := testManager(t)
	if _, err := m.Delete("nope"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// A deleted peer's address goes back in the pool. Without this, a deployment that
// re-adds a node a few times slowly exhausts the /24.
func TestDeletedAddressIsReused(t *testing.T) {
	m, _, _ := testManager(t)
	m.keygen = (&stubKeys{}).gen
	a, _, _ := m.Create("a", ModeReal, nil)
	b, _, _ := m.Create("b", ModeReal, nil)
	if _, err := m.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	c, _, err := m.Create("c", ModeReal, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.TunnelIP != a.TunnelIP {
		t.Fatalf("expected the freed address %s to be reused, got %s (b has %s)", a.TunnelIP, c.TunnelIP, b.TunnelIP)
	}
}

func TestRotateKeepsAddressAndDropsTheOldKeyFirst(t *testing.T) {
	m, f, _ := testManager(t)
	m.keygen = (&stubKeys{}).gen
	p, first, err := m.Create("wings-1", ModeReal, nil)
	if err != nil {
		t.Fatal(err)
	}
	rotated, second, err := m.Rotate(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.TunnelIP != p.TunnelIP || rotated.ID != p.ID {
		t.Fatalf("rotate must keep the id and address: %+v vs %+v", rotated, p)
	}
	if rotated.PublicKey == p.PublicKey {
		t.Fatal("rotate must change the public key")
	}
	if first.ClientPriv == second.ClientPriv {
		t.Fatal("rotate must issue a new private key")
	}
	cmds := f.all()
	var removeAt, setAt = -1, -1
	for i, c := range cmds {
		if strings.Contains(c, "peer pubkey-1 remove") {
			removeAt = i
		}
		if strings.Contains(c, "peer pubkey-2 allowed-ips") {
			setAt = i
		}
	}
	if removeAt < 0 || setAt < 0 || removeAt > setAt {
		// Leaving the old key in place would keep the old client connected
		// with the same AllowedIPs: a rotate that rotates nothing.
		t.Fatalf("the old key must be removed before the new one is added: %v", cmds)
	}
}

func TestRestoreReAppliesEveryPeer(t *testing.T) {
	m, _, store := testManager(t)
	m.keygen = (&stubKeys{}).gen
	if _, _, err := m.Create("a", ModeReal, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Create("b", ModeSite, []string{"10.0.0.0/24"}); err != nil {
		t.Fatal(err)
	}

	// A fresh manager over the same state file: this is what a reboot looks
	// like. wg0.conf holds no peers, so nothing is live until Restore runs.
	f2 := &fakeWG{}
	m2 := NewManager(m.cfg, store, wg.Manager{Run: f2.run}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := m2.Restore(); err != nil {
		t.Fatal(err)
	}
	if len(m2.List()) != 2 {
		t.Fatalf("expected 2 restored peers, got %d", len(m2.List()))
	}
	for _, want := range []string{
		"wg set wg0 fwmark 0x2b",
		"ip rule add not fwmark 0x2b lookup 201 priority 90",
		"wg set wg0 peer pubkey-1 allowed-ips 10.66.66.2/32",
		"wg set wg0 peer pubkey-2 allowed-ips 10.66.66.3/32,10.0.0.0/24",
		"ip route replace 10.0.0.0/24 dev wg0 scope link table 201",
	} {
		if !f2.has(want) {
			t.Fatalf("restore did not issue %q: %v", want, f2.all())
		}
	}
}

// Regression test for the live routing-loop bug: a site peer's lan_cidrs can
// legitimately cover an address the VPS has to reach directly -- the peer's
// own WireGuard transport endpoint when that endpoint is on a private network
// (a VPS and its nodes on a provider's internal LAN, or a home network), another
// peer's endpoint, or the VPS's own subnet. The agent cannot rule that out at
// peer-creation time: a peer's endpoint is not part of the create request at
// all, and a roaming client's endpoint changes later without telling the
// agent. (The one address it CAN check, its own public IP, it now refuses --
// see TestLANCIDRCoveringTheVPSOwnAddressIsRefused.) The fix therefore has to
// be unconditional: every peer route goes into the dedicated table, never
// into the main table, regardless of what the CIDR happens to cover, and the
// interface fwmark plus the "not fwmark ... lookup ..." rule are always in
// place first. See docs/dev/decisions.md.
func TestSitePeerWhoseLANCIDRCoversItsOwnEndpointStillGetsAUsableRuleset(t *testing.T) {
	m, f, _ := testManager(t)
	m.keygen = (&stubKeys{}).gen

	if _, _, err := m.Create("site-1", ModeSite, []string{"10.10.0.75/32"}); err != nil {
		t.Fatal(err)
	}

	cmds := f.all()
	fwmarkAt, ruleAt, peerAt, routeAt := -1, -1, -1, -1
	for i, c := range cmds {
		switch {
		case c == "wg set wg0 fwmark 0x2b":
			fwmarkAt = i
		case c == "ip rule add not fwmark 0x2b lookup 201 priority 90":
			ruleAt = i
		case strings.Contains(c, "peer pubkey-1 allowed-ips"):
			peerAt = i
		case c == "ip route replace 10.10.0.75/32 dev wg0 scope link table 201":
			routeAt = i
		}
	}
	if fwmarkAt < 0 {
		t.Fatalf("expected the interface fwmark to be set: %v", cmds)
	}
	if ruleAt < 0 {
		t.Fatalf("expected the peer-routing ip rule to be installed: %v", cmds)
	}
	if routeAt < 0 {
		t.Fatalf("expected the lan_cidr route in the dedicated table: %v", cmds)
	}
	if fwmarkAt > peerAt || ruleAt > peerAt {
		t.Fatalf("the fwmark and rule must be in place before the peer is set: %v", cmds)
	}
	if fwmarkAt > routeAt || ruleAt > routeAt {
		t.Fatalf("the fwmark and rule must be in place before the route is added: %v", cmds)
	}

	// The whole point: this exact route must never appear in the main table.
	forbidden := "ip route replace 10.10.0.75/32 dev wg0"
	for _, c := range cmds {
		if c == forbidden {
			t.Fatalf("a peer route must never go into the main table (that is the live bug): %v", cmds)
		}
	}
}

// The one overlap the agent can detect it must refuse, with a message the
// plugin shows the admin verbatim. This matters most when the VPS's public
// address is itself RFC1918 (a VPS behind NAT), because the RFC1918 check
// cannot catch those and the operator gets a tunnel that works while the VPS
// quietly loses the path to its own network.
func TestLANCIDRCoveringTheVPSOwnAddressIsRefused(t *testing.T) {
	m, _, _ := testManager(t)
	m.keygen = (&stubKeys{}).gen
	m.cfg.PublicIP = netip.MustParseAddr("10.10.0.5")

	_, _, err := m.Create("site-1", ModeSite, []string{"10.10.0.0/24"})
	if err == nil {
		t.Fatal("expected a lan_cidr containing the VPS's own address to be refused")
	}
	var ve ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("must be a ValidationError so the API answers 422, got %T: %v", err, err)
	}
	for _, want := range []string{"10.10.0.0/24", "10.10.0.5"} {
		if !strings.Contains(ve.Reason, want) {
			t.Fatalf("the message must name %q so the admin can fix it: %q", want, ve.Reason)
		}
	}
	if len(m.List()) != 0 {
		t.Fatalf("a refused peer must not be created: %+v", m.List())
	}

	// A range next door is fine: the check is about containment, not about
	// refusing every range on a private network.
	if _, _, err := m.Create("site-2", ModeSite, []string{"10.11.0.0/24"}); err != nil {
		t.Fatalf("a range that does not contain the VPS address must be accepted: %v", err)
	}
}

// With no public IP configured the check is skipped rather than guessed at:
// an agent that cannot parse AUTOPROXY_PUBLIC_IP must still create peers.
func TestLANCIDRCheckIsSkippedWithoutAKnownPublicIP(t *testing.T) {
	m, _, _ := testManager(t)
	m.keygen = (&stubKeys{}).gen
	if _, _, err := m.Create("site-1", ModeSite, []string{"10.10.0.0/24"}); err != nil {
		t.Fatalf("unknown public IP must not block peer creation: %v", err)
	}
}

// If applying a peer fails, the peer must not end up in state pretending to
// work: the admin would hand out a join code for a tunnel that does not exist.
func TestCreateRollsBackWhenApplyFails(t *testing.T) {
	m, f, store := testManager(t)
	m.keygen = (&stubKeys{}).gen
	f.fail = "wg set"
	if _, _, err := m.Create("a", ModeReal, nil); err == nil {
		t.Fatal("expected an error when wg set fails")
	}
	if len(m.List()) != 0 {
		t.Fatalf("a failed create must not leave a peer behind: %+v", m.List())
	}
	stored, _ := store.Load()
	if len(stored) != 0 {
		t.Fatalf("a failed create must not persist: %+v", stored)
	}
}

func TestJoinCodeRoundTrip(t *testing.T) {
	m, _, _ := testManager(t)
	m.keygen = (&stubKeys{}).gen
	_, code, err := m.Create("wings-1", ModeSite, []string{"10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	enc, err := code.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(enc, "+/=") {
		t.Fatalf("the join code goes on a shell command line; it must be base64url without padding: %s", enc)
	}
	back, err := DecodeJoinCode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if back.ClientPriv != code.ClientPriv || back.ClientAddr != code.ClientAddr ||
		back.Mode != code.Mode || len(back.LANCIDRs) != 1 || back.V != JoinCodeVersion {
		t.Fatalf("round trip lost data: %+v vs %+v", back, code)
	}
}

func TestDecodeJoinCodeRejectsRubbish(t *testing.T) {
	if _, err := DecodeJoinCode("not base64!!"); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := DecodeJoinCode("eyJ2Ijo5OTl9"); err == nil || !strings.Contains(err.Error(), "version 999") {
		t.Fatalf("an unknown version must be named, got %v", err)
	}
}

// The private key is the one thing we must never write down.
func TestPrivateKeyIsNeverStored(t *testing.T) {
	m, _, store := testManager(t)
	m.keygen = (&stubKeys{}).gen
	_, code, err := m.Create("wings-1", ModeReal, nil)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range stored {
		if strings.Contains(fmt.Sprintf("%+v", p), code.ClientPriv) {
			t.Fatalf("the client private key was persisted: %+v", p)
		}
	}
}

func TestPoolExhaustion(t *testing.T) {
	m, _, _ := testManager(t)
	m.keygen = (&stubKeys{}).gen
	m.cfg.Subnet = netip.MustParsePrefix("10.66.66.0/29") // .1 VPS, .2-.7 usable
	var last error
	n := 0
	for i := 0; i < 20; i++ {
		if _, _, err := m.Create(fmt.Sprintf("p%d", i), ModeReal, nil); err != nil {
			last = err
			break
		}
		n++
	}
	if last == nil || !strings.Contains(last.Error(), "no free addresses") {
		t.Fatalf("expected the pool to run out with a clear message, got %v after %d peers", last, n)
	}
}
