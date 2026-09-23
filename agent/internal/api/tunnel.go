package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/clients"
)

// TunnelPrefix is the path prefix of the routes a tunnel client may call
// without the bearer token.
const TunnelPrefix = "/v1/tunnel/"

// MaxTunnelBody caps a check-in. A real one is a few hundred bytes.
const MaxTunnelBody = 4 << 10

// tunnelMinInterval is the least time between two check-ins from one peer
// that the agent will act on. A client checks in every PollSeconds; anything
// faster is a bug or a peer misbehaving, and it gets a quiet 429.
const tunnelMinInterval = 2 * time.Second

// tunnelWarnEvery bounds how often a misbehaving peer can make the agent log.
const tunnelWarnEvery = 10 * time.Minute

type peerKey struct{}

func withPeer(r *http.Request, id string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), peerKey{}, id))
}

func peerFrom(r *http.Request) string {
	id, _ := r.Context().Value(peerKey{}).(string)
	return id
}

// tunnelPeer decides whether r came through the tunnel from a known peer, and
// if so which one. Both ends of the connection are checked:
//
//   - the local address must be the VPS's own tunnel address. The listener is
//     bound to every address (the panel reaches it on the public one), so this
//     is what separates a tunnel request from a public one.
//   - the remote address must be exactly one peer's tunnel address. WireGuard
//     only accepts a packet from a peer when its source is inside that peer's
//     AllowedIPs, and a peer's AllowedIPs hold its own /32 plus, in site mode,
//     private LAN ranges that may never overlap the tunnel subnet (refused at
//     peer creation). So a tunnel source address names one key.
//
// What stops the same packet arriving from the internet, with the tunnel
// address as its destination and a peer's address as its source? Nothing on
// the internet routes 10.66.66.1 to this VPS, the TCP handshake's reply goes
// into the tunnel (so a spoofer never sees it), and the agent's own nftables
// table drops anything addressed to the tunnel address that did not come in
// on the tunnel interface (nft.Render, chain tunnel_guard).
func (s *Server) tunnelPeer(r *http.Request) (string, bool) {
	if !s.cfg.TunnelIP.IsValid() {
		return "", false
	}
	la, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok || la == nil {
		return "", false
	}
	local, err := netip.ParseAddrPort(la.String())
	if err != nil || local.Addr().Unmap() != s.cfg.TunnelIP {
		return "", false
	}
	remote, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return "", false
	}
	src := remote.Addr().Unmap().String()
	for _, p := range s.peers.List() {
		if p.TunnelIP == src {
			return p.ID, true
		}
	}
	return "", false
}

// tunnelLimiter throttles check-ins and the log lines a bad one can cause.
type tunnelLimiter struct {
	mu     sync.Mutex
	last   map[string]time.Time
	warned map[string]time.Time
}

func (l *tunnelLimiter) allow(id string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == nil {
		l.last = map[string]time.Time{}
	}
	if t, ok := l.last[id]; ok && now.Sub(t) < tunnelMinInterval {
		return false
	}
	l.last[id] = now
	return true
}

func (l *tunnelLimiter) shouldWarn(id string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.warned == nil {
		l.warned = map[string]time.Time{}
	}
	if t, ok := l.warned[id]; ok && now.Sub(t) < tunnelWarnEvery {
		return false
	}
	l.warned[id] = now
	return true
}

// tunnelHandler serves the routes a tunnel client calls. Deliberately not
// wrapped in the per-request logger: every client checks in every couple of
// minutes, and a line per check-in would bury everything else in the journal.
// It logs changes instead (a new version, an update that started or failed).
func (s *Server) tunnelHandler() http.Handler {
	lim := &tunnelLimiter{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/tunnel/checkin", func(w http.ResponseWriter, r *http.Request) {
		id := peerFrom(r)
		now := time.Now()
		if !lim.allow(id, now) {
			writeJSON(w, http.StatusTooManyRequests, errorResponse{Error: "checking in too often"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxTunnelBody)
		var rep clients.Report
		if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
			if lim.shouldWarn(id, now) {
				s.log.Warn("tunnel check-in with an unreadable body", "peer", id, "error", err)
			}
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid JSON body: " + err.Error()})
			return
		}
		ans, rec, changed, err := s.clients.Checkin(id, rep)
		var ve clients.ValidationError
		if errors.As(err, &ve) {
			if lim.shouldWarn(id, now) {
				s.log.Warn("tunnel check-in refused", "peer", id, "reason", ve.Reason)
			}
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: ve.Reason})
			return
		}
		if err != nil {
			// The report is applied in memory; only the restart-safety net
			// failed. Answer normally so the client is not punished for it.
			if lim.shouldWarn(id, now) {
				s.log.Error("could not save client state", "error", err)
			}
		}
		if changed {
			args := []any{"peer", id, "version", rec.Version, "flavour", rec.Flavour}
			if rec.Update != nil {
				args = append(args, "update", rec.Update.State, "update_version", rec.Update.Version)
				if rec.Update.Error != "" {
					args = append(args, "update_error", rec.Update.Error)
				}
			}
			s.log.Info("tunnel client reported", args...)
		}
		writeJSON(w, http.StatusOK, ans)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not found"})
	})
	return mux
}
