// Package rules holds the desired forwarding rule set and validates it.
//
// Validation is pure: no I/O, no globals. The agent is the last line of
// defence (the Pelican plugin validates too), so every check lives here.
package rules

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// MaxRules caps the rule set so a runaway plugin cannot generate a ruleset
// that takes minutes to apply.
const MaxRules = 4096

// Rule is one public port (or port range) forwarded to a private target.
//
// A rule names its target in exactly one of two ways:
//
//	{"target_peer": "<peer id>"}                 real-IP mode: DNAT straight to
//	                                             that peer's tunnel address, no
//	                                             masquerade, so the game server
//	                                             sees the player's own IP.
//	{"target_ip": "10.0.0.10", "via_peer": "x"}  site mode: an address on that
//	                                             peer's LAN, masqueraded,
//	                                             because that host has no route
//	                                             back into the tunnel.
type Rule struct {
	ID            string `json:"id"`
	Proto         string `json:"proto"`                     // tcp | udp | both
	PublicPort    int    `json:"public_port"`               // start of the span
	PublicPortEnd *int   `json:"public_port_end,omitempty"` // nil or >= PublicPort
	TargetPeer    string `json:"target_peer,omitempty"`     // real-IP mode
	TargetIP      string `json:"target_ip,omitempty"`       // site mode, RFC1918 IPv4
	ViaPeer       string `json:"via_peer,omitempty"`        // site mode, the peer that reaches TargetIP
	TargetPort    *int   `json:"target_port,omitempty"`     // nil = keep the public port
	Note          string `json:"note,omitempty"`
}

// PeerInfo is the slice of a peer this package needs. The rules package must
// not import peers: peers already depends on state, and state depends on
// rules.
type PeerInfo struct {
	ID       string
	Mode     string // "real" | "site"
	TunnelIP string
	LANCIDRs []string
}

// PeerLookup maps peer id to peer.
type PeerLookup map[string]PeerInfo

// Resolved is a rule with its target turned into a concrete address, plus
// whether that address is a peer's own tunnel IP (Direct: never masqueraded).
type Resolved struct {
	Rule
	IP     string
	Direct bool
}

// Rejection explains why one rule was refused. An empty ID means the whole
// request was refused (for example: too many rules).
type Rejection struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// End returns the last port of the span.
func (r Rule) End() int {
	if r.PublicPortEnd != nil {
		return *r.PublicPortEnd
	}
	return r.PublicPort
}

// IsRange reports whether the rule covers more than one public port.
func (r Rule) IsRange() bool { return r.End() != r.PublicPort }

// Protos expands "both" into the concrete protocols.
func (r Rule) Protos() []string {
	switch r.Proto {
	case "both":
		return []string{"tcp", "udp"}
	case "tcp", "udp":
		return []string{r.Proto}
	default:
		return nil
	}
}

// target returns a comparable description of where the rule sends traffic.
// Two overlapping rules with the same target signature are compatible only if
// their spans are identical; nftables interval maps reject partial overlaps.
func (r Resolved) target() string {
	kind := "site"
	if r.Direct {
		kind = "direct"
	}
	if r.TargetPort == nil {
		return fmt.Sprintf("%s/%s:keep", kind, r.IP)
	}
	return fmt.Sprintf("%s/%s:%d", kind, r.IP, *r.TargetPort)
}

func (r Rule) span() string {
	if r.IsRange() {
		return fmt.Sprintf("%d-%d", r.PublicPort, r.End())
	}
	return fmt.Sprintf("%d", r.PublicPort)
}

// sameDefinition reports whether two rules are byte-for-byte the same forward
// (the note is ignored: it is cosmetic).
func sameDefinition(a, b Resolved) bool {
	return a.Proto == b.Proto &&
		a.PublicPort == b.PublicPort &&
		a.End() == b.End() &&
		a.target() == b.target()
}

func validPort(p int) bool { return p >= 1 && p <= 65535 }

// isRFC1918 reports whether ip is a private IPv4 address (10/8, 172.16/12,
// 192.168/16). Anything else - public, loopback, link-local, IPv6 - is refused:
// the tunnel only reaches the home LAN.
func isRFC1918(s string) bool {
	addr, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil || !addr.Is4() {
		return false
	}
	b := addr.As4()
	switch {
	case b[0] == 10:
		return true
	case b[0] == 172 && b[1] >= 16 && b[1] <= 31:
		return true
	case b[0] == 192 && b[1] == 168:
		return true
	}
	return false
}

// checkOne runs the checks that need no peer list and returns the first
// failure reason, or "".
func checkOne(r Rule, reserved map[int]bool) string {
	if strings.TrimSpace(r.ID) == "" {
		return "id is required"
	}
	if r.Protos() == nil {
		return fmt.Sprintf("proto %q must be tcp, udp or both", r.Proto)
	}
	if !validPort(r.PublicPort) {
		return fmt.Sprintf("public_port %d out of range 1-65535", r.PublicPort)
	}
	if r.PublicPortEnd != nil {
		end := *r.PublicPortEnd
		if !validPort(end) {
			return fmt.Sprintf("public_port_end %d out of range 1-65535", end)
		}
		if end < r.PublicPort {
			return fmt.Sprintf("public_port_end %d is below public_port %d", end, r.PublicPort)
		}
	}
	if r.IsRange() && r.TargetPort != nil {
		return "a port range cannot be remapped: target_port must be null"
	}
	if r.TargetPort != nil && !validPort(*r.TargetPort) {
		return fmt.Sprintf("target_port %d out of range 1-65535", *r.TargetPort)
	}
	for p := r.PublicPort; p <= r.End(); p++ {
		if reserved[p] {
			return fmt.Sprintf("public port %d is reserved on the VPS", p)
		}
	}
	return ""
}

// cidrContains reports whether ip falls inside any of the CIDRs.
func cidrContains(cidrs []string, ip string) bool {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return false
	}
	for _, c := range cidrs {
		pfx, err := netip.ParsePrefix(strings.TrimSpace(c))
		if err != nil {
			continue
		}
		if pfx.Contains(addr) {
			return true
		}
	}
	return false
}

// resolveOne turns a rule's target into a concrete address. The returned
// string is the rejection reason, or "" on success.
//
// This is where a rule that names a peer the admin has since deleted is
// caught: the id no longer resolves, so the forward is refused rather than
// silently sending players into a tunnel address nothing answers on.
func resolveOne(r Rule, peers PeerLookup) (Resolved, string) {
	hasPeer := strings.TrimSpace(r.TargetPeer) != ""
	hasIP := strings.TrimSpace(r.TargetIP) != ""
	hasVia := strings.TrimSpace(r.ViaPeer) != ""

	switch {
	case hasPeer && hasIP:
		return Resolved{}, "set either target_peer (real-IP mode) or target_ip with via_peer (site mode), not both"
	case !hasPeer && !hasIP:
		return Resolved{}, "no target: set target_peer (real-IP mode) or target_ip with via_peer (site mode)"
	}

	if hasPeer {
		if hasVia {
			return Resolved{}, "via_peer is only used with target_ip; a target_peer rule already names its peer"
		}
		p, ok := peers[r.TargetPeer]
		if !ok {
			return Resolved{}, fmt.Sprintf("target_peer %q is not a known peer", r.TargetPeer)
		}
		if p.Mode != "real" {
			return Resolved{}, fmt.Sprintf("peer %q is in site mode: forward to an address on its LAN with target_ip and via_peer", r.TargetPeer)
		}
		if p.TunnelIP == "" {
			return Resolved{}, fmt.Sprintf("peer %q has no tunnel address", r.TargetPeer)
		}
		return Resolved{Rule: r, IP: p.TunnelIP, Direct: true}, ""
	}

	if !hasVia {
		return Resolved{}, "target_ip needs via_peer: the agent has to know which tunnel reaches that address"
	}
	p, ok := peers[r.ViaPeer]
	if !ok {
		return Resolved{}, fmt.Sprintf("via_peer %q is not a known peer", r.ViaPeer)
	}
	if p.Mode != "site" {
		return Resolved{}, fmt.Sprintf("peer %q is in real-IP mode: forward to it with target_peer, not target_ip", r.ViaPeer)
	}
	if !isRFC1918(r.TargetIP) {
		return Resolved{}, fmt.Sprintf("target_ip %q is not a private (RFC1918) IPv4 address", r.TargetIP)
	}
	if !cidrContains(p.LANCIDRs, r.TargetIP) {
		return Resolved{}, fmt.Sprintf("target_ip %s is outside the ranges peer %q covers (%s)", r.TargetIP, p.ID, strings.Join(p.LANCIDRs, ", "))
	}
	return Resolved{Rule: r, IP: strings.TrimSpace(r.TargetIP), Direct: false}, ""
}

type entry struct {
	idx   int
	start int
	end   int
}

// Validate checks a full desired rule set against the current peer list and
// returns one rejection per bad rule, in input order. An empty result means
// the set is safe to render.
//
// Rules that are exact duplicates of each other are accepted (the renderer
// deduplicates). Rules whose public port spans overlap for the same protocol
// are both rejected: nftables cannot express the ambiguity and silently
// picking one would send players to the wrong machine.
func Validate(rs []Rule, reserved []int, peers PeerLookup) []Rejection {
	if len(rs) > MaxRules {
		return []Rejection{{Reason: fmt.Sprintf("too many rules: %d (max %d)", len(rs), MaxRules)}}
	}

	res := make(map[int]bool, len(reserved))
	for _, p := range reserved {
		res[p] = true
	}

	reasons := make([]string, len(rs))
	resolved := make([]Resolved, len(rs))
	for i, r := range rs {
		if reasons[i] = checkOne(r, res); reasons[i] != "" {
			continue
		}
		resolved[i], reasons[i] = resolveOne(r, peers)
	}

	// Same id used for two different forwards: the plugin lost track of state.
	byID := map[string]int{}
	for i, r := range rs {
		if reasons[i] != "" {
			continue
		}
		if j, ok := byID[r.ID]; ok {
			if !sameDefinition(resolved[i], resolved[j]) {
				reasons[i] = fmt.Sprintf("id %q is used twice with different definitions", r.ID)
				reasons[j] = reasons[i]
			}
			continue
		}
		byID[r.ID] = i
	}

	// Overlap detection, per concrete protocol.
	perProto := map[string][]entry{}
	for i, r := range rs {
		if reasons[i] != "" {
			continue
		}
		for _, p := range r.Protos() {
			perProto[p] = append(perProto[p], entry{idx: i, start: r.PublicPort, end: r.End()})
		}
	}
	protos := make([]string, 0, len(perProto))
	for p := range perProto {
		protos = append(protos, p)
	}
	sort.Strings(protos)

	for _, p := range protos {
		es := perProto[p]
		sort.Slice(es, func(a, b int) bool {
			if es[a].start != es[b].start {
				return es[a].start < es[b].start
			}
			return es[a].idx < es[b].idx
		})
		for a := 0; a < len(es); a++ {
			for b := a + 1; b < len(es); b++ {
				if es[b].start > es[a].end {
					break // sorted by start: nothing further can overlap
				}
				ra, rb := resolved[es[a].idx], resolved[es[b].idx]
				identicalSpan := ra.PublicPort == rb.PublicPort && ra.End() == rb.End()
				if identicalSpan && ra.target() == rb.target() {
					continue // exact duplicate forward, harmless
				}
				if reasons[es[a].idx] == "" {
					reasons[es[a].idx] = conflictReason(p, ra, rb)
				}
				if reasons[es[b].idx] == "" {
					reasons[es[b].idx] = conflictReason(p, rb, ra)
				}
			}
		}
	}

	var out []Rejection
	for i, reason := range reasons {
		if reason != "" {
			out = append(out, Rejection{ID: rs[i].ID, Reason: reason})
		}
	}
	return out
}

// conflictReason explains, from a's point of view, why a and b cannot both be
// applied.
func conflictReason(proto string, a, b Resolved) string {
	if a.target() == b.target() {
		return fmt.Sprintf("%s public ports %s overlap rule %q on %s (ranges must be identical or disjoint)", proto, a.span(), b.ID, b.span())
	}
	return fmt.Sprintf("%s public ports %s conflict with rule %q on %s: different target", proto, a.span(), b.ID, b.span())
}

// Resolve turns a validated rule set into rules with concrete addresses. It
// returns an error if any rule no longer resolves, which is how a stored rule
// set that mentions a deleted peer is detected on restart.
func Resolve(rs []Rule, peers PeerLookup) ([]Resolved, []Rejection) {
	out := make([]Resolved, 0, len(rs))
	var bad []Rejection
	for _, r := range rs {
		res, reason := resolveOne(r, peers)
		if reason != "" {
			bad = append(bad, Rejection{ID: r.ID, Reason: reason})
			continue
		}
		out = append(out, res)
	}
	return out, bad
}

// Dedupe drops exact duplicate forwards, preserving input order.
func Dedupe(rs []Resolved) []Resolved {
	seen := map[string]bool{}
	out := make([]Resolved, 0, len(rs))
	for _, r := range rs {
		key := fmt.Sprintf("%s|%d|%d|%s", r.Proto, r.PublicPort, r.End(), r.target())
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}
