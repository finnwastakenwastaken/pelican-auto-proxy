package peers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/state"
	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/wg"
)

// FileName is the peer state file inside the state directory.
const FileName = "peers.json"

type snapshot struct {
	Peers []Peer `json:"peers"`
}

// Store reads and writes peers.json.
type Store struct{ Dir string }

func (s Store) path() string { return filepath.Join(s.Dir, FileName) }

// Load returns the stored peers, or nil on a fresh install.
func (s Store) Load() ([]Peer, error) {
	b, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snap snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path(), err)
	}
	return snap.Peers, nil
}

// Save writes the peer list atomically. 0640: it holds no private keys, but it
// does describe the whole tunnel topology.
func (s Store) Save(ps []Peer) error {
	b, err := json.MarshalIndent(snapshot{Peers: ps}, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return state.WriteFileAtomic(s.path(), b, 0o640)
}

// Config is the fixed tunnel description the manager works within.
type Config struct {
	Subnet   netip.Prefix // 10.66.66.0/24
	VPSIP    netip.Addr   // 10.66.66.1
	Endpoint string       // "203.0.113.10:51820", handed to clients
	// PublicIP is this VPS's own public address, the one clients dial. It is
	// here only so a site peer's lan_cidrs can be refused when they cover it
	// -- see checkCIDRsLocked. Zero value means "unknown", and the check is
	// skipped rather than guessed at.
	PublicIP  netip.Addr
	VPSPubKey string
	// APIPort is copied into join codes (JoinCode.APIPort). Zero leaves it out.
	APIPort int
}

// Manager owns the peer list: allocation, validation, persistence and the live
// wg/ip commands that make a stored peer real.
type Manager struct {
	cfg   Config
	store Store
	wgm   wg.Manager
	log   *slog.Logger
	now   func() time.Time

	// keygen mints a WireGuard keypair. It is a field rather than a direct
	// call so tests can supply readable keys: real keys are 44-character
	// base64 literals, and one of those committed to this repository would
	// trip the secret sweep.
	keygen func() (priv, pub string, err error)

	mu    sync.Mutex
	peers []Peer
}

// NewManager builds a manager. Call Restore before serving.
func NewManager(cfg Config, store Store, wgm wg.Manager, log *slog.Logger) *Manager {
	m := &Manager{cfg: cfg, store: store, wgm: wgm, log: log, now: time.Now, peers: []Peer{}}
	m.keygen = wgm.GenKey
	return m
}

// SetClock replaces the clock. Tests only.
func (m *Manager) SetClock(f func() time.Time) { m.now = f }

// SetKeygen replaces key generation. Tests only: it exists so tests can use
// readable keys instead of 44-character base64 literals, which the repository's
// secret sweep refuses.
func (m *Manager) SetKeygen(f func() (priv, pub string, err error)) { m.keygen = f }

// Restore loads peers.json and re-applies every peer and route to the live
// interface. wg set and ip route replace are both idempotent, so this is safe
// to run on every start, and it is the only thing that survives a reboot:
// wg0.conf holds no peers at all.
//
// It also (re)installs the interface fwmark and the peer-routing ip rule
// before touching any peer -- see the wg.Manager doc comment for why a peer
// route must never land in the main table. That is unconditional and does not
// depend on any peer existing yet, so a freshly set-up VPS with zero peers
// still has the loop protection in place the moment the first peer is
// created. applyLocked ensures the same two calls again per peer (cheap and
// idempotent) so the protection is never missing regardless of which code
// path added a peer.
func (m *Manager) Restore() error {
	ps, err := m.store.Load()
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []string
	if err := m.wgm.EnsureFWMark(); err != nil {
		errs = append(errs, fmt.Sprintf("set interface fwmark: %v", err))
		m.log.Error("could not set the WireGuard interface fwmark; a site peer's lan_cidrs covering its own endpoint would loop the handshake back into the tunnel", "error", err)
	} else if err := m.wgm.EnsureRoutingRule(); err != nil {
		errs = append(errs, fmt.Sprintf("install peer-routing rule: %v", err))
		m.log.Error("could not install the peer-routing ip rule; peer routes would compete with return traffic in the main table", "error", err)
	}

	if ps == nil {
		ps = []Peer{}
	}
	m.peers = ps
	var failed []string
	for _, p := range ps {
		if err := m.applyLocked(p); err != nil {
			failed = append(failed, p.Name)
			m.log.Error("could not re-apply peer", "peer", p.Name, "id", p.ID, "error", err)
		}
	}
	m.log.Info("restored peers", "peers", len(ps), "failed", len(failed))
	if len(failed) > 0 {
		errs = append(errs, fmt.Sprintf("%d of %d peers could not be applied: %v", len(failed), len(ps), failed))
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// applyLocked makes one stored peer live: the interface fwmark, the
// peer-routing ip rule, the wg peer entry, and one route per AllowedIP into
// the dedicated table. wg never adds routes itself.
//
// EnsureFWMark and EnsureRoutingRule run here too, not only in Restore: a
// peer can be created live (POST /v1/peers) long after the daemon started,
// and the loop-protection rule must be in place before that peer's routes
// are, not after. Both calls are idempotent and cheap (a declarative "wg set"
// and an "ip rule show" check), so paying for them on every peer apply is not
// worth branching around.
func (m *Manager) applyLocked(p Peer) error {
	if err := m.wgm.EnsureFWMark(); err != nil {
		return fmt.Errorf("set interface fwmark: %w", err)
	}
	if err := m.wgm.EnsureRoutingRule(); err != nil {
		return fmt.Errorf("install peer-routing rule: %w", err)
	}
	allowed := p.AllowedIPs()
	if err := m.wgm.SetPeer(p.PublicKey, allowed); err != nil {
		return err
	}
	for _, cidr := range allowed {
		if err := m.wgm.AddRoute(cidr); err != nil {
			return err
		}
	}
	return nil
}

// removeLocked undoes applyLocked. Best effort in order: the peer entry first,
// so no traffic can arrive while the routes are being torn down.
func (m *Manager) removeLocked(p Peer) error {
	var firstErr error
	if err := m.wgm.RemovePeer(p.PublicKey); err != nil {
		firstErr = err
	}
	for _, cidr := range p.AllowedIPs() {
		if err := m.wgm.DelRoute(cidr); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// List returns a copy of the peer list in creation order, which is the order
// the plugin displays.
func (m *Manager) List() []Peer {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Peer, len(m.peers))
	copy(out, m.peers)
	return out
}

// Get returns one peer by id.
func (m *Manager) Get(id string) (Peer, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.peers {
		if p.ID == id {
			return p, true
		}
	}
	return Peer{}, false
}

// allocIPLocked returns the lowest free tunnel address. The pool is host .2 to
// .254 of the configured subnet: .1 is the VPS and the last address of a /24
// is the broadcast address, which some clients refuse to bind.
func (m *Manager) allocIPLocked() (netip.Addr, error) {
	taken := map[netip.Addr]bool{m.cfg.VPSIP: true}
	for _, p := range m.peers {
		if a, err := netip.ParseAddr(p.TunnelIP); err == nil {
			taken[a] = true
		}
	}
	base := m.cfg.Subnet.Masked().Addr()
	candidate := base.Next() // .1
	for host := 1; host <= 254; host++ {
		candidate = candidate.Next()
		if !m.cfg.Subnet.Contains(candidate) {
			break
		}
		if !taken[candidate] {
			return candidate, nil
		}
	}
	return netip.Addr{}, invalid("the tunnel subnet %s has no free addresses left", m.cfg.Subnet)
}

// checkCIDRsLocked validates the LAN ranges of a new or rotated peer against
// the tunnel subnet and every other peer.
//
// Overlaps are refused rather than merged: two peers claiming 10.0.0.0/24 give
// one kernel routing table two answers for the same address, and which peer a
// packet reaches then depends on the order the agent happened to apply them.
func (m *Manager) checkCIDRsLocked(mode string, raw []string, selfID string) ([]string, error) {
	if mode == ModeReal {
		if len(raw) > 0 {
			return nil, invalid("lan_cidrs is only valid in site mode; a real-IP peer forwards to itself")
		}
		return nil, nil
	}
	if len(raw) == 0 {
		return nil, invalid("site mode needs at least one lan_cidr: it describes which addresses this peer may reach")
	}
	var out []string
	var parsed []netip.Prefix
	for _, s := range raw {
		p, err := parseCIDR(s)
		if err != nil {
			return nil, err
		}
		if p.Overlaps(m.cfg.Subnet) {
			return nil, invalid("lan_cidrs: %s overlaps the tunnel subnet %s. The tunnel's own addresses are handed out by this VPS; a LAN range may not claim them.", p, m.cfg.Subnet)
		}
		// The VPS's own public address, and anything containing it, is
		// refused outright. Accepting it is the routing loop this whole
		// fwmark/table construction exists to survive, but surviving it is
		// not the same as it being sensible: a range that swallows the
		// address every client dials means the VPS can no longer reach its
		// own network from the peer table's point of view, and the operator
		// almost certainly meant a narrower range. Refusing at creation time
		// gives them a message; letting it through gives them a tunnel that
		// works and a VPS that behaves strangely in ways nothing explains.
		//
		// This matters most where the "public" IP is itself RFC1918 -- a VPS
		// behind NAT, or a VPS on a home LAN -- because the RFC1918
		// check above cannot catch those.
		if m.cfg.PublicIP.IsValid() && p.Contains(m.cfg.PublicIP) {
			return nil, invalid("lan_cidrs: %s contains this VPS's own address %s. The VPS has to reach that address directly; routing it into the tunnel would cut it off from its own network. Use a range that leaves %s out, or list the individual hosts you need as /32s.", p, m.cfg.PublicIP, m.cfg.PublicIP)
		}
		for _, q := range parsed {
			if p.Overlaps(q) {
				return nil, invalid("lan_cidrs: %s and %s overlap each other", p, q)
			}
		}
		for _, other := range m.peers {
			if other.ID == selfID {
				continue
			}
			for _, oc := range other.LANCIDRs {
				q, err := netip.ParsePrefix(oc)
				if err != nil {
					continue
				}
				if p.Overlaps(q) {
					return nil, invalid("lan_cidrs: %s overlaps %s, already claimed by peer %q", p, q, other.Name)
				}
			}
		}
		parsed = append(parsed, p)
		out = append(out, p.String())
	}
	return out, nil
}

func validName(name string) error {
	name = trimSpace(name)
	if name == "" {
		return invalid("name is required")
	}
	if len(name) > 64 {
		return invalid("name is longer than 64 characters")
	}
	return nil
}

// Create allocates a tunnel address, mints a keypair, applies the peer live
// and persists it. The returned join code holds the only copy of the private
// key.
func (m *Manager) Create(name, mode string, lanCIDRs []string) (Peer, JoinCode, error) {
	if err := validName(name); err != nil {
		return Peer{}, JoinCode{}, err
	}
	if mode != ModeReal && mode != ModeSite {
		return Peer{}, JoinCode{}, invalid("mode %q must be %q or %q", mode, ModeReal, ModeSite)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.peers) >= MaxPeers {
		return Peer{}, JoinCode{}, invalid("too many peers: %d (max %d)", len(m.peers), MaxPeers)
	}
	cidrs, err := m.checkCIDRsLocked(mode, lanCIDRs, "")
	if err != nil {
		return Peer{}, JoinCode{}, err
	}
	addr, err := m.allocIPLocked()
	if err != nil {
		return Peer{}, JoinCode{}, err
	}
	priv, pub, err := m.keygen()
	if err != nil {
		return Peer{}, JoinCode{}, fmt.Errorf("generate keypair: %w", err)
	}

	p := Peer{
		ID:        newID(),
		Name:      trimSpace(name),
		PublicKey: pub,
		TunnelIP:  addr.String(),
		Mode:      mode,
		LANCIDRs:  cidrs,
		CreatedAt: m.now().UTC(),
	}

	// Apply first, persist second, and undo the apply if the persist fails.
	// The other order can hand out a join code for a peer that is not live.
	if err := m.applyLocked(p); err != nil {
		_ = m.removeLocked(p)
		return Peer{}, JoinCode{}, fmt.Errorf("apply peer: %w", err)
	}
	next := append(append([]Peer{}, m.peers...), p)
	if err := m.store.Save(next); err != nil {
		_ = m.removeLocked(p)
		return Peer{}, JoinCode{}, fmt.Errorf("save peers: %w", err)
	}
	m.peers = next
	m.log.Info("peer created", "id", p.ID, "name", p.Name, "mode", p.Mode, "tunnel_ip", p.TunnelIP)
	return p, m.joinCode(p, priv), nil
}

func (m *Manager) joinCode(p Peer, priv string) JoinCode {
	return JoinCode{
		V:            JoinCodeVersion,
		Endpoint:     m.cfg.Endpoint,
		VPSWGPubKey:  m.cfg.VPSPubKey,
		ClientPriv:   priv,
		ClientAddr:   p.TunnelIP + "/32",
		TunnelSubnet: m.cfg.Subnet.String(),
		VPSTunnelIP:  m.cfg.VPSIP.String(),
		Mode:         p.Mode,
		LANCIDRs:     p.LANCIDRs,
		Keepalive:    Keepalive,
		APIPort:      m.cfg.APIPort,
	}
}

// Delete removes a peer, its wg entry and its routes. Rules that pointed at it
// are the caller's problem: it re-renders without them, and the next push that
// still mentions this peer is rejected.
func (m *Manager) Delete(id string) (Peer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := -1
	for i, p := range m.peers {
		if p.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return Peer{}, ErrNotFound
	}
	p := m.peers[idx]
	next := append(append([]Peer{}, m.peers[:idx]...), m.peers[idx+1:]...)
	if err := m.store.Save(next); err != nil {
		return Peer{}, fmt.Errorf("save peers: %w", err)
	}
	m.peers = next
	// The peer is gone from state either way now; a failure to tear down the
	// live entry is reported but must not resurrect it.
	if err := m.removeLocked(p); err != nil {
		m.log.Error("peer removed from state but not fully from the interface", "id", p.ID, "error", err)
		return p, fmt.Errorf("peer %s removed from state, but tearing it down failed: %w", p.ID, err)
	}
	m.log.Info("peer deleted", "id", p.ID, "name", p.Name)
	return p, nil
}

// Rotate mints a new keypair for an existing peer, keeping its id, address and
// ranges. The old key stops working the moment this returns.
func (m *Manager) Rotate(id string) (Peer, JoinCode, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := -1
	for i, p := range m.peers {
		if p.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return Peer{}, JoinCode{}, ErrNotFound
	}
	old := m.peers[idx]
	priv, pub, err := m.keygen()
	if err != nil {
		return Peer{}, JoinCode{}, fmt.Errorf("generate keypair: %w", err)
	}
	updated := old
	updated.PublicKey = pub

	// Drop the old key before adding the new one: wg keys peers by public
	// key, so leaving the old entry in place would keep the old client
	// connected with the same AllowedIPs.
	if err := m.wgm.RemovePeer(old.PublicKey); err != nil {
		return Peer{}, JoinCode{}, fmt.Errorf("remove old key: %w", err)
	}
	if err := m.applyLocked(updated); err != nil {
		return Peer{}, JoinCode{}, fmt.Errorf("apply rotated peer: %w", err)
	}
	next := append([]Peer{}, m.peers...)
	next[idx] = updated
	if err := m.store.Save(next); err != nil {
		return Peer{}, JoinCode{}, fmt.Errorf("save peers: %w", err)
	}
	m.peers = next
	m.log.Info("peer key rotated", "id", updated.ID, "name", updated.Name)
	return updated, m.joinCode(updated, priv), nil
}
