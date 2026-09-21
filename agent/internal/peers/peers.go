// Package peers owns the dynamic WireGuard peer list.
//
// One peer is one machine that runs autoproxy-client: usually a Wings host
// (mode "real", so players' own IPs survive the forward), sometimes a LAN
// machine that forwards on to other hosts (mode "site", shared IP).
//
// The peer list is dynamic on purpose. wg0.conf is written once at setup with
// no peers at all and is never rewritten: peers are added and removed live
// with "wg set", and re-applied from peers.json on start. Two writers on one
// config file is how peers silently disappear after a reboot.
//
// Private keys are generated here, handed to exactly one client inside a join
// code, and never stored. A lost join code is replaced by a rotate, not by
// reading anything back.
package peers

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

// Modes. Real-IP mode DNATs straight to the peer's tunnel address and does not
// masquerade, so the game server sees the player's address. Site mode targets
// an address on the peer's LAN and does masquerade, because that host has no
// route back into the tunnel.
const (
	ModeReal = "real"
	ModeSite = "site"
)

// JoinCodeVersion is the version field of a join code. Bump it when the
// meaning of a field changes; the client refuses versions it does not know.
const JoinCodeVersion = 1

// Keepalive is the client's persistent keepalive in seconds. The client always
// dials out, so this is what holds the NAT mapping open on the home router.
const Keepalive = 25

// MaxPeers caps the list. A /24 tunnel has 253 usable addresses anyway; this
// is here so a runaway plugin cannot grow the file without bound.
const MaxPeers = 253

// Peer is one client machine. The private key is deliberately absent: it is
// returned once in a join code and then forgotten.
type Peer struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	PublicKey string    `json:"public_key"`
	TunnelIP  string    `json:"tunnel_ip"`
	Mode      string    `json:"mode"`
	LANCIDRs  []string  `json:"lan_cidrs,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// AllowedIPs is what this peer may send from and what we route to it: its own
// tunnel address, plus the LAN ranges behind it in site mode.
//
// A peer is never given 0.0.0.0/0. That would let one compromised client claim
// every address on the tunnel, including the other peers' game servers.
func (p Peer) AllowedIPs() []string {
	out := []string{p.TunnelIP + "/32"}
	out = append(out, p.LANCIDRs...)
	return out
}

// JoinCode is what the admin pastes into the client installer. It carries the
// only copy of the client's private key.
type JoinCode struct {
	V            int      `json:"v"`
	Endpoint     string   `json:"endpoint"` // "ip:port"
	VPSWGPubKey  string   `json:"vps_wg_pubkey"`
	ClientPriv   string   `json:"client_privkey"`
	ClientAddr   string   `json:"client_address"` // "10.66.66.5/32"
	TunnelSubnet string   `json:"tunnel_subnet"`
	VPSTunnelIP  string   `json:"vps_tunnel_ip"`
	Mode         string   `json:"mode"`
	LANCIDRs     []string `json:"lan_cidrs,omitempty"`
	Keepalive    int      `json:"keepalive"`
}

// Encode renders the join code as one base64url token, unpadded so it survives
// being pasted into a shell command without quoting.
func (j JoinCode) Encode() (string, error) {
	b, err := json.Marshal(j)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// DecodeJoinCode is the inverse of Encode. The client is bash and decodes this
// itself; this exists so our tests check the round trip.
func DecodeJoinCode(s string) (JoinCode, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return JoinCode{}, fmt.Errorf("join code is not valid base64url: %w", err)
	}
	var j JoinCode
	if err := json.Unmarshal(b, &j); err != nil {
		return JoinCode{}, fmt.Errorf("join code is not valid JSON: %w", err)
	}
	if j.V != JoinCodeVersion {
		return JoinCode{}, fmt.Errorf("join code version %d is not supported (expected %d)", j.V, JoinCodeVersion)
	}
	return j, nil
}

// ErrNotFound is returned for an unknown peer id.
var ErrNotFound = errors.New("peer not found")

// ValidationError is a bad request from the caller, not a failure of ours: the
// API turns it into a 422 rather than a 500.
type ValidationError struct{ Reason string }

func (e ValidationError) Error() string { return e.Reason }

// trimSpace is strings.TrimSpace, named here so manager.go does not need the
// import for one call.
func trimSpace(s string) string { return strings.TrimSpace(s) }

func invalid(format string, args ...any) error {
	return ValidationError{Reason: fmt.Sprintf(format, args...)}
}

// newID returns a short random identifier. Short on purpose: it appears in
// URLs, log lines and rule targets that a human has to match up by eye.
func newID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is not a condition we can paper over.
		panic("peers: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// isRFC1918Prefix reports whether the whole prefix lies inside 10/8,
// 172.16/12 or 192.168/16. Anything else is refused: the tunnel reaches a home
// LAN, and routing a public range into it would black-hole that range for the
// VPS itself.
func isRFC1918Prefix(p netip.Prefix) bool {
	private := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
	}
	for _, q := range private {
		// p lies wholly inside q when its base address is in q and p is at
		// least as specific. Contains() alone would accept 10.0.0.0/4.
		if p.Bits() >= q.Bits() && q.Contains(p.Addr()) {
			return true
		}
	}
	return false
}

// parseCIDR validates one LAN range and returns it in canonical form.
func parseCIDR(s string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(strings.TrimSpace(s))
	if err != nil {
		return netip.Prefix{}, invalid("lan_cidrs: %q is not a valid IPv4 CIDR", s)
	}
	if !p.Addr().Is4() {
		return netip.Prefix{}, invalid("lan_cidrs: %q is not IPv4; IPv6 is not supported yet", s)
	}
	if p.Masked() != p {
		return netip.Prefix{}, invalid("lan_cidrs: %q has host bits set; write it as %s", s, p.Masked())
	}
	if !isRFC1918Prefix(p) {
		return netip.Prefix{}, invalid("lan_cidrs: %q is not a private (RFC1918) range", s)
	}
	return p, nil
}
