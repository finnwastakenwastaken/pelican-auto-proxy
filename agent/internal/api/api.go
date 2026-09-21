// Package api serves the agent's HTTPS control plane.
//
// It listens on the public interface over TLS 1.3 with the self-signed
// certificate generated at setup, and requires a bearer token on every route.
// There is no UI and no unauthenticated health endpoint on purpose: nothing on
// this listener should be reachable without the token.
//
// The Pelican panel may be anywhere -- a box on the admin's LAN, a VM at
// another provider -- so the listener cannot be restricted to the tunnel. What
// protects it instead: TLS with a certificate the plugin pins, a 32-byte
// bearer token, a constant-time compare, and a per-address lockout that stops
// somebody grinding through tokens.
package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/nft"
	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/peers"
	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/rules"
	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/state"
	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/wg"
)

// MaxBody caps request bodies. 4096 rules of JSON is well under this.
const MaxBody = 1 << 20

// HealthyHandshakeS is how old a peer's last handshake may be before the
// status summary stops counting it as healthy. WireGuard rehandshakes every
// two minutes when there is traffic, so three minutes means "we have not heard
// from this client for more than one missed handshake".
const HealthyHandshakeS = 180

// TokenEnvKey is the line in agent.env that holds the bearer token.
const TokenEnvKey = "AUTOPROXY_TOKEN"

func itoa(n int) string { return strconv.Itoa(n) }

// Config is everything the server needs that does not change at runtime.
type Config struct {
	Token       string
	PublicIface string
	WGIface     string
	Reserved    []int
	RulesFile   string
	EnvFile     string // agent.env; rewritten by POST /v1/token/rotate
	Version     string
}

// Server owns the applied rule set. Every apply is serialised by mu, so two
// concurrent pushes can never interleave a render with an nft run.
type Server struct {
	cfg     Config
	store   state.Store
	applier nft.Applier
	wg      wg.Reader
	wgm     wg.Manager
	peers   *peers.Manager
	log     *slog.Logger
	start   time.Time
	lock    *lockout

	mu        sync.Mutex
	token     string
	current   []rules.Rule
	counts    nft.Counts
	appliedAt time.Time
	lastError string
}

// New builds a server. Call Restore before serving.
func New(cfg Config, store state.Store, applier nft.Applier, wgr wg.Reader, wgm wg.Manager, pm *peers.Manager, log *slog.Logger) *Server {
	return &Server{
		cfg:     cfg,
		store:   store,
		applier: applier,
		wg:      wgr,
		wgm:     wgm,
		peers:   pm,
		log:     log,
		start:   time.Now(),
		lock:    newLockout(nil),
		token:   cfg.Token,
		current: []rules.Rule{},
	}
}

// SetClock replaces the lockout clock. Tests only.
func (s *Server) SetClock(now func() time.Time) { s.lock = newLockout(now) }

// peerLookup builds the id -> peer map the rules package validates against.
func (s *Server) peerLookup() rules.PeerLookup {
	out := rules.PeerLookup{}
	for _, p := range s.peers.List() {
		out[p.ID] = rules.PeerInfo{ID: p.ID, Mode: p.Mode, TunnelIP: p.TunnelIP, LANCIDRs: p.LANCIDRs}
	}
	return out
}

// directTargets lists the tunnel addresses of every real-IP peer. These are
// the addresses that must never be masqueraded.
func (s *Server) directTargets() []string {
	var out []string
	for _, p := range s.peers.List() {
		if p.Mode == peers.ModeReal && p.TunnelIP != "" {
			out = append(out, p.TunnelIP)
		}
	}
	return out
}

// Restore loads the persisted rule set, re-applies the peers, and re-applies
// the rules that still resolve. A failure here is recorded as last_error and
// logged, but never fatal: an agent that refuses to start is an agent nobody
// can ask what went wrong.
func (s *Server) Restore() {
	if err := s.peers.Restore(); err != nil {
		s.setError("restore peers: " + err.Error())
		s.log.Error("peer restore failed", "error", err)
	}

	snap, err := s.store.Load()
	if err != nil {
		s.setError("load state: " + err.Error())
		s.log.Error("state load failed", "error", err)
		return
	}
	if snap == nil {
		s.log.Info("no stored rules, starting empty")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Rules whose peer has been deleted since the last push no longer resolve.
	// Drop those and apply the rest: refusing the whole set would close every
	// working game port because of one stale entry.
	keep, dropped := s.splitResolvable(snap.Rules)
	if len(dropped) > 0 {
		var ids []string
		for _, d := range dropped {
			ids = append(ids, d.ID)
		}
		s.log.Warn("dropping stored rules that no longer resolve", "rules", ids, "first_reason", dropped[0].Reason)
	}
	counts, err := s.applyLocked(keep)
	if err != nil {
		s.lastError = "restore: " + err.Error()
		s.log.Error("restore failed", "rules", len(keep), "error", err)
		return
	}
	if len(dropped) > 0 {
		s.lastError = fmt.Sprintf("%d stored rule(s) dropped on restore: %s", len(dropped), dropped[0].Reason)
	}
	s.log.Info("restored rules", "rules", counts.Rules, "tcp", counts.TCP, "udp", counts.UDP, "dropped", len(dropped))
}

// splitResolvable separates rules that still name a live peer from those that
// do not.
func (s *Server) splitResolvable(rs []rules.Rule) ([]rules.Rule, []rules.Rejection) {
	lookup := s.peerLookup()
	var keep []rules.Rule
	var dropped []rules.Rejection
	for _, r := range rs {
		_, bad := rules.Resolve([]rules.Rule{r}, lookup)
		if len(bad) > 0 {
			dropped = append(dropped, bad[0])
			continue
		}
		keep = append(keep, r)
	}
	return keep, dropped
}

func (s *Server) setError(msg string) {
	s.mu.Lock()
	s.lastError = msg
	s.mu.Unlock()
}

// applyLocked resolves, renders and applies rs. Caller holds mu.
func (s *Server) applyLocked(rs []rules.Rule) (nft.Counts, error) {
	resolved, bad := rules.Resolve(rs, s.peerLookup())
	if len(bad) > 0 {
		return nft.Counts{}, fmt.Errorf("rule %q: %s", bad[0].ID, bad[0].Reason)
	}
	text := nft.Render(resolved, s.cfg.PublicIface, s.cfg.WGIface, s.directTargets())
	if err := s.applier.Apply(s.cfg.RulesFile, text); err != nil {
		return nft.Counts{}, err
	}
	s.current = rs
	s.counts = nft.Count(resolved)
	s.appliedAt = time.Now().UTC()
	s.lastError = ""
	return s.counts, nil
}

// reapply re-renders the current rule set against the current peer list,
// dropping rules that no longer resolve. Called after a peer is created (the
// direct_targets set grows) or deleted (its forwards must close, and its
// tunnel address must leave direct_targets).
func (s *Server) reapply(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keep, dropped := s.splitResolvable(s.current)
	counts, err := s.applyLocked(keep)
	if err != nil {
		s.lastError = reason + ": " + err.Error()
		s.log.Error("re-apply after peer change failed", "reason", reason, "error", err)
		return
	}
	if len(dropped) > 0 {
		s.lastError = fmt.Sprintf("%d rule(s) dropped after %s: %s", len(dropped), reason, dropped[0].Reason)
		s.log.Warn("rules dropped after peer change", "reason", reason, "dropped", len(dropped))
	}
	if err := s.store.Save(state.Snapshot{Rules: keep, AppliedAt: s.appliedAt}); err != nil {
		s.log.Error("state save failed after peer change", "error", err)
	}
	s.log.Info("re-applied rules after peer change", "reason", reason, "rules", counts.Rules, "dropped", len(dropped))
}

// ---------------------------------------------------------------------------
// Response shapes
// ---------------------------------------------------------------------------

type peersSummary struct {
	Total   int `json:"total"`
	Healthy int `json:"healthy"`
}

type statusResponse struct {
	Version   string       `json:"version"`
	UptimeS   int64        `json:"uptime_s"`
	WG        wg.Status    `json:"wg"`
	Peers     peersSummary `json:"peers"`
	Applied   nft.Counts   `json:"applied"`
	AppliedAt *time.Time   `json:"applied_at"`
	LastError string       `json:"last_error"`
}

// peerResponse is one peer as the plugin sees it: the stored record plus the
// live counters from "wg show".
type peerResponse struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	PublicKey    string    `json:"public_key"`
	TunnelIP     string    `json:"tunnel_ip"`
	Mode         string    `json:"mode"`
	LANCIDRs     []string  `json:"lan_cidrs"`
	HandshakeAge *int64    `json:"handshake_age_s"`
	RX           int64     `json:"rx"`
	TX           int64     `json:"tx"`
	CreatedAt    time.Time `json:"created_at"`
}

type peersResponse struct {
	Peers []peerResponse `json:"peers"`
}

type createPeerRequest struct {
	Name     string   `json:"name"`
	Mode     string   `json:"mode"`
	LANCIDRs []string `json:"lan_cidrs,omitempty"`
}

type createPeerResponse struct {
	Peer     peerResponse `json:"peer"`
	JoinCode string       `json:"join_code"`
	Warning  string       `json:"warning"`
}

type rulesResponse struct {
	Rules []rules.Rule `json:"rules"`
}

type putRequest struct {
	Rules []rules.Rule `json:"rules"`
}

type putResponse struct {
	Applied   nft.Counts `json:"applied"`
	AppliedAt time.Time  `json:"applied_at"`
}

type rejectResponse struct {
	Rejected []rules.Rejection `json:"rejected"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type tokenRotateResponse struct {
	Token   string `json:"token"`
	Warning string `json:"warning"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func (s *Server) peerResponses() []peerResponse {
	stats := s.wgm.PeerStats()
	list := s.peers.List()
	out := make([]peerResponse, 0, len(list))
	for _, p := range list {
		r := peerResponse{
			ID: p.ID, Name: p.Name, PublicKey: p.PublicKey, TunnelIP: p.TunnelIP,
			Mode: p.Mode, LANCIDRs: p.LANCIDRs, CreatedAt: p.CreatedAt,
		}
		if r.LANCIDRs == nil {
			r.LANCIDRs = []string{}
		}
		if st, ok := stats[p.PublicKey]; ok {
			r.HandshakeAge, r.RX, r.TX = st.HandshakeAge, st.RX, st.TX
		}
		out = append(out, r)
	}
	return out
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	counts, appliedAt, lastErr := s.counts, s.appliedAt, s.lastError
	s.mu.Unlock()

	ps := s.peerResponses()
	summary := peersSummary{Total: len(ps)}
	for _, p := range ps {
		if p.HandshakeAge != nil && *p.HandshakeAge < HealthyHandshakeS {
			summary.Healthy++
		}
	}

	resp := statusResponse{
		Version:   s.cfg.Version,
		UptimeS:   int64(time.Since(s.start).Seconds()),
		WG:        s.wg.Read(),
		Peers:     summary,
		Applied:   counts,
		LastError: lastErr,
	}
	if !appliedAt.IsZero() {
		t := appliedAt
		resp.AppliedAt = &t
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGetPeers(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, peersResponse{Peers: s.peerResponses()})
}

func (s *Server) handleCreatePeer(w http.ResponseWriter, r *http.Request) {
	var req createPeerRequest
	if !decodeBody(w, r, &req) {
		return
	}
	p, code, err := s.peers.Create(req.Name, req.Mode, req.LANCIDRs)
	if err != nil {
		s.writePeerError(w, err)
		return
	}
	encoded, err := code.Encode()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "encode join code: " + err.Error()})
		return
	}
	// A new real-IP peer changes direct_targets, so the rendered ruleset is
	// now out of date even though no rule changed.
	if p.Mode == peers.ModeReal {
		s.reapply("peer " + p.ID + " created")
	}
	resp := createPeerResponse{
		Peer: peerResponse{
			ID: p.ID, Name: p.Name, PublicKey: p.PublicKey, TunnelIP: p.TunnelIP,
			Mode: p.Mode, LANCIDRs: p.LANCIDRs, CreatedAt: p.CreatedAt,
		},
		JoinCode: encoded,
		Warning:  "This join code contains the client's private key and is shown exactly once. If it is lost, rotate the peer.",
	}
	if resp.Peer.LANCIDRs == nil {
		resp.Peer.LANCIDRs = []string{}
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleDeletePeer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := s.peers.Delete(id)
	if errors.Is(err, peers.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "no peer with id " + id})
		return
	}
	if err != nil {
		// The peer is out of state; the teardown is what failed. Say both.
		s.log.Error("peer delete incomplete", "id", id, "error", err)
		s.reapply("peer " + id + " deleted")
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	// Close the forwards that pointed at this peer straight away rather than
	// leaving public ports open on to an address nothing answers on.
	s.reapply("peer " + p.ID + " deleted")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRotatePeer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, code, err := s.peers.Rotate(id)
	if errors.Is(err, peers.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "no peer with id " + id})
		return
	}
	if err != nil {
		s.writePeerError(w, err)
		return
	}
	encoded, err := code.Encode()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "encode join code: " + err.Error()})
		return
	}
	resp := createPeerResponse{
		Peer: peerResponse{
			ID: p.ID, Name: p.Name, PublicKey: p.PublicKey, TunnelIP: p.TunnelIP,
			Mode: p.Mode, LANCIDRs: p.LANCIDRs, CreatedAt: p.CreatedAt,
		},
		JoinCode: encoded,
		Warning:  "The previous key stopped working. Re-run the client installer with this join code; it is shown exactly once.",
	}
	if resp.Peer.LANCIDRs == nil {
		resp.Peer.LANCIDRs = []string{}
	}
	writeJSON(w, http.StatusOK, resp)
}

// writePeerError maps a peers error to a status code: a caller mistake is 422,
// anything else is ours and is a 500.
func (s *Server) writePeerError(w http.ResponseWriter, err error) {
	var ve peers.ValidationError
	if errors.As(err, &ve) {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: ve.Reason})
		return
	}
	s.log.Error("peer operation failed", "error", err)
	writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
}

func (s *Server) handleGetRules(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	cur := make([]rules.Rule, len(s.current))
	copy(cur, s.current)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, rulesResponse{Rules: cur})
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid JSON body: " + err.Error()})
		return false
	}
	return true
}

func (s *Server) handlePutRules(w http.ResponseWriter, r *http.Request) {
	var req putRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Rules == nil {
		req.Rules = []rules.Rule{}
	}

	if rejected := rules.Validate(req.Rules, s.cfg.Reserved, s.peerLookup()); len(rejected) > 0 {
		s.log.Warn("rule set rejected", "rules", len(req.Rules), "rejected", len(rejected), "first", rejected[0].Reason)
		writeJSON(w, http.StatusUnprocessableEntity, rejectResponse{Rejected: rejected})
		return
	}

	s.mu.Lock()
	counts, err := s.applyLocked(req.Rules)
	appliedAt := s.appliedAt
	if err != nil {
		s.lastError = err.Error()
	}
	s.mu.Unlock()

	if err != nil {
		s.log.Error("apply failed", "rules", len(req.Rules), "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}

	if err := s.store.Save(state.Snapshot{Rules: req.Rules, AppliedAt: appliedAt}); err != nil {
		// The rules are live; only the restart-safety net failed. Say so, but
		// do not pretend the apply failed.
		s.setError("rules applied but state not persisted: " + err.Error())
		s.log.Error("state save failed", "error", err)
	}

	s.log.Info("applied rules", "rules", counts.Rules, "tcp", counts.TCP, "udp", counts.UDP)
	writeJSON(w, http.StatusOK, putResponse{Applied: counts, AppliedAt: appliedAt})
}

// handleTokenRotate mints a new bearer token, writes it to agent.env and
// returns it once. The old token stops working immediately, so the caller has
// to store the new one before its next request.
func (s *Server) handleTokenRotate(w http.ResponseWriter, _ *http.Request) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not generate a token: " + err.Error()})
		return
	}
	next := hex.EncodeToString(raw[:])

	if err := UpdateEnvFile(s.cfg.EnvFile, TokenEnvKey, next); err != nil {
		// Do not switch the live token when it could not be persisted: a
		// restart would fall back to the old one and nobody would know which
		// token is current.
		s.log.Error("token rotate: could not persist", "file", s.cfg.EnvFile, "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "token not rotated: " + err.Error()})
		return
	}

	s.mu.Lock()
	s.token = next
	s.mu.Unlock()
	s.log.Warn("bearer token rotated")
	writeJSON(w, http.StatusOK, tokenRotateResponse{
		Token:   next,
		Warning: "The previous token stopped working. This value is shown once; it is also in " + s.cfg.EnvFile + " (mode 0600).",
	})
}

// UpdateEnvFile rewrites one KEY=value line in a systemd EnvironmentFile,
// preserving every other line, and writes the result atomically at 0600.
func UpdateEnvFile(path, key, value string) error {
	if path == "" {
		return fmt.Errorf("no env file configured")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	lines := strings.Split(string(b), "\n")
	replaced := false
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), key+"=") {
			lines[i] = key + "=" + value
			replaced = true
		}
	}
	if !replaced {
		return fmt.Errorf("%s has no %s= line", path, key)
	}
	return state.WriteFileAtomic(path, []byte(strings.Join(lines, "\n")), 0o600)
}

// ---------------------------------------------------------------------------
// Authentication
// ---------------------------------------------------------------------------

// tokenOK compares the presented bearer token in constant time. Both sides are
// hashed first so the comparison does not leak the token length.
func tokenOK(presented, want string) bool {
	p := sha256.Sum256([]byte(presented))
	w := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(p[:], w[:]) == 1 && want != ""
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r.RemoteAddr)

		// Checked before any hashing: a locked-out address must cost us
		// nothing beyond a map lookup.
		if locked, retry := s.lock.locked(ip); locked {
			writeLockout(w, retry)
			return
		}

		h := r.Header.Get("Authorization")
		presented := ""
		if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
			presented = strings.TrimSpace(h[7:])
		}
		s.mu.Lock()
		want := s.token
		s.mu.Unlock()

		if !tokenOK(presented, want) {
			if s.lock.fail(ip) {
				s.log.Warn("address locked out after repeated authentication failures",
					"remote", ip, "failures", MaxFailures, "minutes", int(LockoutFor.Minutes()))
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="autoproxy"`)
			writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "unauthorized"})
			s.log.Warn("unauthorized request", "path", r.URL.Path, "remote", ip)
			return
		}
		s.lock.success(ip)
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.code,
			"remote", clientIP(r.RemoteAddr),
			"ms", time.Since(started).Milliseconds())
	})
}

// Handler returns the routed, authenticated, logged HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", s.handleStatus)
	mux.HandleFunc("GET /v1/rules", s.handleGetRules)
	mux.HandleFunc("PUT /v1/rules", s.handlePutRules)
	mux.HandleFunc("GET /v1/peers", s.handleGetPeers)
	mux.HandleFunc("POST /v1/peers", s.handleCreatePeer)
	mux.HandleFunc("DELETE /v1/peers/{id}", s.handleDeletePeer)
	mux.HandleFunc("POST /v1/peers/{id}/rotate", s.handleRotatePeer)
	mux.HandleFunc("POST /v1/token/rotate", s.handleTokenRotate)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not found"})
	})
	return s.logging(s.auth(mux))
}
