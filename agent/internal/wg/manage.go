package wg

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Runner executes a command and returns its combined output. It exists so
// tests can record exactly which wg/ip commands the agent would run without
// touching the host's network stack.
type Runner func(name string, args ...string) (string, error)

// ExecRunner is the default Runner.
func ExecRunner(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Default fwmark/table/priority for the peer-routing rule (see EnsureFWMark
// and EnsureRoutingRule). Chosen distinct from the node client's own
// fwmark 0x2a / table 200 / priority 100 (client/autoproxy-client) because
// this is a different machine solving a mirror-image problem: the client
// marks game traffic to steer it INTO the tunnel; the VPS marks the tunnel's
// own transport traffic to steer it OUT of a peer's routing table. Reusing
// the client's numbers would have been harmless (different hosts, different
// namespace) but choosing different ones makes a mixed-role box (never
// expected, but "never" is not "impossible") fail loudly instead of
// colliding silently. See docs/dev/decisions.md.
const (
	DefaultFWMark       = "0x2b"
	DefaultTable        = "201"
	DefaultRulePriority = "90"
)

// Manager drives the live WireGuard interface and the routes that reach it.
//
// wg itself never touches the routing table (wg-quick does, and we deliberately
// do not use wg-quick for dynamic peers: two writers on wg0.conf is how config
// gets lost). So every AllowedIP a peer gets also needs an explicit
// "ip route replace <cidr> dev wg0" from us.
//
// Peer routes do NOT go into the main table. A site peer's lan_cidrs can
// legitimately cover the address WireGuard uses as that peer's own transport
// endpoint (its public IP), because the agent validates lan_cidrs against the
// tunnel subnet and other peers, never against an endpoint it may not know yet
// (a roaming client) or that may change without the agent being told. When
// that happens and the route lives in the main table, it wins over the
// interface route to the real endpoint, so the VPS's own encrypted handshake
// and keepalive packets to that peer get routed back into the tunnel they are
// trying to open -- a loop that never completes a handshake and shows as
// "waiting for the first handshake" forever with no error anywhere. See
// docs/dev/decisions.md and docs/troubleshooting.md.
//
// The fix is the same construction wg-quick uses for AllowedIPs 0.0.0.0/0:
// peer routes go into a dedicated table (EnsureRoutingRule's Table), the
// interface's own transport socket is tagged with a fwmark (EnsureFWMark),
// and a "not fwmark <mark> lookup <table>" ip rule sends everything that is
// NOT that marked traffic through the dedicated table first, while the
// interface's own marked packets fall through to the main table and reach
// the peer's real endpoint.
type Manager struct {
	WGBin        string // default "wg"
	IPBin        string // default "ip"
	Iface        string // default "wg0"
	FWMark       string // default DefaultFWMark
	Table        string // default DefaultTable
	RulePriority string // default DefaultRulePriority
	Run          Runner // default ExecRunner
	DryRun       bool   // when true, commands are not executed
}

func (m Manager) wgBin() string {
	if m.WGBin == "" {
		return "wg"
	}
	return m.WGBin
}

func (m Manager) ipBin() string {
	if m.IPBin == "" {
		return "ip"
	}
	return m.IPBin
}

func (m Manager) iface() string {
	if m.Iface == "" {
		return "wg0"
	}
	return m.Iface
}

func (m Manager) fwmark() string {
	if m.FWMark == "" {
		return DefaultFWMark
	}
	return m.FWMark
}

func (m Manager) table() string {
	if m.Table == "" {
		return DefaultTable
	}
	return m.Table
}

func (m Manager) rulePriority() string {
	if m.RulePriority == "" {
		return DefaultRulePriority
	}
	return m.RulePriority
}

func (m Manager) run(name string, args ...string) (string, error) {
	if m.DryRun {
		return "", nil
	}
	r := m.Run
	if r == nil {
		r = ExecRunner
	}
	out, err := r(name, args...)
	if err != nil {
		if out != "" {
			return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, out)
		}
		return out, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}

// query runs a read-only command even under DryRun: deciding whether the
// rule already exists must not itself be treated as a mutation.
func (m Manager) query(name string, args ...string) (string, error) {
	r := m.Run
	if r == nil {
		r = ExecRunner
	}
	return r(name, args...)
}

// GenKey returns a fresh WireGuard keypair (private, public). The private key
// is handed to exactly one client in a join code and never written to disk by
// the agent.
func (m Manager) GenKey() (priv, pub string, err error) {
	priv, err = m.run(m.wgBin(), "genkey")
	if err != nil {
		return "", "", err
	}
	priv = strings.TrimSpace(priv)
	pub, err = m.PubKey(priv)
	if err != nil {
		return "", "", err
	}
	return priv, pub, nil
}

// PubKey derives the public key of a private key by piping it through
// "wg pubkey".
func (m Manager) PubKey(priv string) (string, error) {
	if m.DryRun {
		return "", nil
	}
	cmd := exec.Command(m.wgBin(), "pubkey")
	cmd.Stdin = strings.NewReader(priv + "\n")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("wg pubkey: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// SetPeer adds or updates a peer in one call. wg set is declarative for the
// named peer: repeating it with the same arguments is a no-op, which is what
// makes re-applying the whole peer list on start safe.
func (m Manager) SetPeer(pubkey string, allowedIPs []string) error {
	if pubkey == "" {
		return fmt.Errorf("refusing to set a peer with an empty public key")
	}
	if len(allowedIPs) == 0 {
		// A peer with no AllowedIPs can neither send nor receive, and an
		// empty argument to wg is silently accepted. Fail loudly instead.
		return fmt.Errorf("refusing to set peer %s with no allowed-ips", short(pubkey))
	}
	_, err := m.run(m.wgBin(), "set", m.iface(), "peer", pubkey, "allowed-ips", strings.Join(allowedIPs, ","))
	return err
}

// RemovePeer drops a peer from the live interface.
func (m Manager) RemovePeer(pubkey string) error {
	if pubkey == "" {
		return fmt.Errorf("refusing to remove a peer with an empty public key")
	}
	_, err := m.run(m.wgBin(), "set", m.iface(), "peer", pubkey, "remove")
	return err
}

// AddRoute points a CIDR at the tunnel, in the dedicated routing table (see
// EnsureRoutingRule) rather than the main table -- a peer route in the main
// table can shadow the path back to that same peer's own transport endpoint,
// which is the routing-loop bug this package exists to prevent (see the
// Manager doc comment). "replace" rather than "add": it succeeds whether or
// not the route is already there, so re-applying the peer list on every start
// stays idempotent.
//
// Before adding to the dedicated table, it also removes any route for the
// same CIDR that a previous version of this agent may have left in the main
// table. That migration is best effort and its result is deliberately
// ignored: on a box that never had the old bug there is nothing to remove,
// and a route left there by something unrelated to this agent is not this
// call's business to diagnose.
func (m Manager) AddRoute(cidr string) error {
	_, _ = m.run(m.ipBin(), "route", "del", cidr, "dev", m.iface())
	_, err := m.run(m.ipBin(), "route", "replace", cidr, "dev", m.iface(), "scope", "link", "table", m.table())
	return err
}

// DelRoute removes a CIDR from the dedicated routing table. A route that is
// already gone is not an error: peer deletion must not get stuck because a
// reboot (or "ip link delete", which drops every route referencing the
// deleted device regardless of table) already cleaned up.
func (m Manager) DelRoute(cidr string) error {
	out, err := m.run(m.ipBin(), "route", "del", cidr, "dev", m.iface(), "table", m.table())
	if err != nil && strings.Contains(strings.ToLower(out), "no such process") {
		return nil
	}
	return err
}

// EnsureFWMark tags the interface's own transport socket (handshake,
// keepalive, and every encrypted packet it sends) with a fixed mark. It is
// what lets the "not fwmark ... lookup ..." rule installed by
// EnsureRoutingRule tell the interface's own traffic apart from everything
// else. "wg set ... fwmark" is declarative like "wg set ... peer", so
// repeating it on every start is a no-op when it is already set.
func (m Manager) EnsureFWMark() error {
	_, err := m.run(m.wgBin(), "set", m.iface(), "fwmark", m.fwmark())
	return err
}

// hasRule reports whether "ip rule show" output already contains the rule
// this package installs.
//
// This is a field scan rather than a substring match on the command we would
// issue, because iproute2 does not print a rule back the way you spell it:
//
//	added:   ip rule add not fwmark 0x2b lookup 201 priority 90
//	printed: 90:\tnot from all fwmark 0x2b lookup 201
//
// The implied "from all" selector in the middle is what a naive
// strings.Contains(out, "not fwmark 0x2b lookup 201") misses, and missing it
// is worse than it sounds: the check says "absent", the add then fails with
// "File exists", and on this code path that error aborts the whole peer
// restore. The container gate caught exactly that.
//
// Fields are compared one by one so "fwmark 0x2b" does not also match a
// hypothetical "fwmark 0x2bc", and so a masked form ("fwmark 0x2b/0xff",
// which we never write but something else might) is not silently treated as
// ours.
func (m Manager) hasRule(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		// Every printed rule starts "<priority>:". Ours is an inverted
		// ("not") rule; an un-inverted rule on the same table is somebody
		// else's and must not be mistaken for it, because deleting it would
		// be deleting their rule.
		if len(f) < 2 || f[1] != "not" {
			continue
		}
		var mark, table bool
		for i := 0; i+1 < len(f); i++ {
			switch f[i] {
			case "fwmark":
				mark = f[i+1] == m.fwmark()
			case "lookup":
				table = f[i+1] == m.table()
			}
		}
		if mark && table {
			return true
		}
	}
	return false
}

// EnsureRoutingRule installs, once, the ip rule that sends everything which is
// NOT the interface's own fwmarked traffic through the dedicated peer-routing
// table first. This is the same construction wg-quick uses for
// AllowedIPs 0.0.0.0/0. Checked before adding (mirroring the same
// check-then-add pattern the node client already uses in
// client/autoproxy-client) because "ip rule add" has no "replace" form and
// running it twice would leave two identical rules rather than failing
// cleanly.
func (m Manager) EnsureRoutingRule() error {
	out, _ := m.query(m.ipBin(), "rule", "show")
	if m.hasRule(out) {
		return nil
	}
	added, err := m.run(m.ipBin(), "rule", "add", "not", "fwmark", m.fwmark(), "lookup", m.table(), "priority", m.rulePriority())
	// "File exists" means the rule is there and hasRule did not recognise
	// it -- most plausibly because /etc/iproute2/rt_tables gives table
	// m.table() a name and iproute2 printed the name instead of the number.
	// The goal ("the rule is installed") is met either way, and treating it
	// as a failure would abort the peer restore on a box where nothing is
	// actually wrong.
	if err != nil && strings.Contains(strings.ToLower(added), "file exists") {
		return nil
	}
	return err
}

// HasRoutingRule reports whether the peer-routing rule is installed, using the
// same field-by-field match as EnsureRoutingRule. Anything that asks "is the
// rule there?" goes through here, so no caller can fall back into matching the
// command as it was typed instead of as "ip rule show" prints it.
func (m Manager) HasRoutingRule() bool {
	out, _ := m.query(m.ipBin(), "rule", "show")
	return m.hasRule(out)
}

// RemoveRoutingRule undoes EnsureRoutingRule. The rule outlives the wg
// interface -- it names a table and a fwmark, not a device -- so deleting the
// interface (what uninstall and peer teardown otherwise rely on to clean up
// routes) does not remove it. Already absent is not an error.
func (m Manager) RemoveRoutingRule() error {
	out, _ := m.query(m.ipBin(), "rule", "show")
	if !m.hasRule(out) {
		return nil
	}
	_, err := m.run(m.ipBin(), "rule", "del", "not", "fwmark", m.fwmark(), "lookup", m.table(), "priority", m.rulePriority())
	return err
}

// PeerStat is the live counter set for one peer.
type PeerStat struct {
	HandshakeAge *int64 `json:"handshake_age_s"` // nil = never handshaked
	RX           int64  `json:"rx"`
	TX           int64  `json:"tx"`
}

// PeerStats returns live statistics keyed by peer public key.
func (m Manager) PeerStats() map[string]PeerStat {
	r := m.Run
	if r == nil {
		r = ExecRunner
	}
	out, err := r(m.wgBin(), "show", m.iface(), "dump")
	if err != nil {
		return map[string]PeerStat{}
	}
	return ParsePeerStats(out, time.Now())
}

// ParsePeerStats reads "wg show <iface> dump". Line 0 is the interface; every
// later line is a peer: pubkey, psk, endpoint, allowed-ips, handshake, rx, tx,
// keepalive.
func ParsePeerStats(dump string, now time.Time) map[string]PeerStat {
	stats := map[string]PeerStat{}
	lines := strings.Split(strings.TrimRight(dump, "\n"), "\n")
	for i, line := range lines {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 8 {
			continue
		}
		hs, _ := strconv.ParseInt(f[4], 10, 64)
		rx, _ := strconv.ParseInt(f[5], 10, 64)
		tx, _ := strconv.ParseInt(f[6], 10, 64)
		st := PeerStat{RX: rx, TX: tx}
		if hs > 0 {
			age := now.Unix() - hs
			if age < 0 {
				age = 0
			}
			st.HandshakeAge = &age
		}
		stats[f[0]] = st
	}
	return stats
}

// short trims a key for log and error messages: full public keys in logs are
// noise, and the first characters are enough to tell two peers apart.
func short(key string) string {
	if len(key) > 8 {
		return key[:8] + "..."
	}
	return key
}
