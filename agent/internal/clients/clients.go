// Package clients keeps what each tunnel client last said about itself, and
// the client version the panel would like it to run.
//
// Two writers, two channels, never mixed:
//
//   - The client reports over the tunnel (POST /v1/tunnel/checkin on the
//     VPS's tunnel address). Its identity is the tunnel source address, which
//     WireGuard's cryptokey routing ties to that peer's key; there is no token
//     on that side. A report can only ever describe the peer it came from.
//   - The panel sets a desired version with the bearer token
//     (PUT /v1/peers/{id}/client). That is the only way a desired version is
//     ever set.
//
// What the agent hands back to a client is a version string and a request id,
// nothing else. The client downloads that version from the project's own
// GitHub release and verifies it against the release's SHA256SUMS itself; the
// agent never serves, names or points at code. See docs/security.md.
//
// The records are kept apart from peers.json on purpose: peers.json is the
// tunnel topology, written only when a peer is created, rotated or deleted;
// this file changes whenever a client reports something new.
package clients

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/state"
)

// FileName is the client state file inside the state directory.
const FileName = "clients.json"

// SaveEvery bounds how stale reported_at can be on disk after an agent
// restart. A check-in that changes nothing but the timestamp is only written
// when the last write is older than this, so N clients polling every two
// minutes do not turn into N writes every two minutes.
const SaveEvery = 10 * time.Minute

// MaxErrorLen caps the failure reason a client may report. It is shown to the
// admin verbatim, so it must be short, and it must never be a way to park
// kilobytes of text in the agent's state.
const MaxErrorLen = 300

// Update states a client reports.
const (
	StateUpdating = "updating"
	StateFailed   = "failed"
	StateUpdated  = "updated"
)

// Flavours.
const (
	FlavourSystemd = "systemd"
	FlavourDocker  = "docker"
	FlavourUnknown = "unknown"
)

// Update is the last thing a client said about an update attempt, or what the
// agent concluded when the client reached its desired version.
type Update struct {
	State     string    `json:"state"`
	Version   string    `json:"version"`
	Error     string    `json:"error"`
	RequestID string    `json:"request_id"`
	At        time.Time `json:"at"`
}

// Record is everything known about one peer's client.
type Record struct {
	Version        string     `json:"version,omitempty"`
	Flavour        string     `json:"flavour,omitempty"`
	RemoteUpdates  *bool      `json:"remote_updates,omitempty"`
	ReportedAt     *time.Time `json:"reported_at,omitempty"`
	DesiredVersion string     `json:"desired_version,omitempty"`
	RequestID      string     `json:"request_id,omitempty"`
	RequestedAt    *time.Time `json:"requested_at,omitempty"`
	Update         *Update    `json:"update,omitempty"`
}

// Report is the body of a check-in.
//
// Decoded leniently on purpose, unlike every token-authenticated request:
// clients and the agent are updated independently and a newer client may
// report a field this agent has never heard of. Rejecting it would turn an
// unknown field into "the version is never reported", silently. A check-in can
// only describe its own peer and can never create a forward, so the strictness
// that protects PUT /v1/rules buys nothing here.
type Report struct {
	Version       string        `json:"version"`
	Flavour       string        `json:"flavour"`
	RemoteUpdates *bool         `json:"remote_updates"`
	Update        *UpdateReport `json:"update"`
}

// UpdateReport is the client's account of its last update attempt.
type UpdateReport struct {
	State     string `json:"state"`
	Version   string `json:"version"`
	Error     string `json:"error"`
	RequestID string `json:"request_id"`
}

// Answer is what a check-in gets back.
type Answer struct {
	DesiredVersion string `json:"desired_version"`
	RequestID      string `json:"request_id"`
	PollSeconds    int    `json:"poll_s"`
}

// PollSeconds is how often a client should check in. Sent in every answer so
// it can change later without a client release.
const PollSeconds = 120

// ValidationError is a bad request, reported as 422 (token API) or 400
// (tunnel).
type ValidationError struct{ Reason string }

func (e ValidationError) Error() string { return e.Reason }

func invalid(format string, a ...any) error {
	return ValidationError{Reason: fmt.Sprintf(format, a...)}
}

// reportedVersion is what a client may call its own version: a release
// ("0.3.0"), a pre-release ("0.3.0-rc1") or a development build ("dev").
var reportedVersion = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+-]{0,31}$`)

// releaseVersion is what the panel may ask for: a plain release number.
// Nothing else can be downloaded from a release page anyway, and refusing
// anything fancier keeps the string a client turns into a URL boring.
var releaseVersion = regexp.MustCompile(`^v?([0-9]{1,4})\.([0-9]{1,4})\.([0-9]{1,4})$`)

// NormalizeRelease validates a desired version and strips a leading "v".
func NormalizeRelease(v string) (string, error) {
	v = strings.TrimSpace(v)
	m := releaseVersion.FindStringSubmatch(v)
	if m == nil {
		return "", invalid("desired_version %q is not a release number like 1.2.3", v)
	}
	return m[1] + "." + m[2] + "." + m[3], nil
}

// Compare compares two versions of the form X.Y.Z with an optional
// "-prerelease" suffix and an optional leading "v". ok is false when either
// side does not parse, and the caller must then not draw any conclusion.
// A pre-release sorts before its release (0.3.0-rc1 < 0.3.0).
func Compare(a, b string) (cmp int, ok bool) {
	pa, oka := parse(a)
	pb, okb := parse(b)
	if !oka || !okb {
		return 0, false
	}
	for i := 0; i < 3; i++ {
		if pa.n[i] != pb.n[i] {
			if pa.n[i] < pb.n[i] {
				return -1, true
			}
			return 1, true
		}
	}
	switch {
	case pa.pre == pb.pre:
		return 0, true
	case pa.pre == "":
		return 1, true
	case pb.pre == "":
		return -1, true
	case pa.pre < pb.pre:
		return -1, true
	default:
		return 1, true
	}
}

type parsed struct {
	n   [3]int
	pre string
}

func parse(v string) (parsed, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	var p parsed
	core := v
	if i := strings.IndexByte(v, '-'); i >= 0 {
		core, p.pre = v[:i], v[i+1:]
		if p.pre == "" {
			return parsed{}, false
		}
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return parsed{}, false
	}
	for i, s := range parts {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || s == "" {
			return parsed{}, false
		}
		p.n[i] = n
	}
	return p, true
}

// sanitize keeps a client-supplied string printable and short.
func sanitize(s string, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= max {
			b.WriteString("...")
			break
		}
		if unicode.IsControl(r) {
			r = ' '
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// Store reads and writes clients.json.
type Store struct{ Dir string }

func (s Store) path() string { return filepath.Join(s.Dir, FileName) }

type snapshot struct {
	Clients map[string]Record `json:"clients"`
}

// Load returns the stored records, or an empty map on a fresh install or an
// agent upgraded from a version without this file.
func (s Store) Load() (map[string]Record, error) {
	b, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Record{}, nil
	}
	if err != nil {
		return nil, err
	}
	var snap snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path(), err)
	}
	if snap.Clients == nil {
		snap.Clients = map[string]Record{}
	}
	return snap.Clients, nil
}

// Save writes the records atomically. 0640 like peers.json: nothing secret,
// but it describes every node.
func (s Store) Save(recs map[string]Record) error {
	b, err := json.MarshalIndent(snapshot{Clients: recs}, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return state.WriteFileAtomic(s.path(), b, 0o640)
}

// Registry is the in-memory view, safe for concurrent use.
type Registry struct {
	store Store
	now   func() time.Time

	mu       sync.Mutex
	recs     map[string]Record
	lastSave time.Time
}

// NewRegistry builds an empty registry. Call Load before serving.
func NewRegistry(store Store) *Registry {
	return &Registry{store: store, now: time.Now, recs: map[string]Record{}}
}

// SetClock replaces the clock. Tests only.
func (r *Registry) SetClock(f func() time.Time) { r.now = f }

// Load reads clients.json. A damaged file is reported and the registry starts
// empty: losing "which version did this client report" is harmless, a
// refusal to start is not.
func (r *Registry) Load() error {
	recs, err := r.store.Load()
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.recs = map[string]Record{}
		return err
	}
	r.recs = recs
	return nil
}

// Get returns a copy of one peer's record (the zero Record when there is none).
func (r *Registry) Get(id string) Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recs[id]
}

// Forget drops a deleted peer's record.
func (r *Registry) Forget(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.recs[id]; !ok {
		return nil
	}
	delete(r.recs, id)
	return r.saveLocked()
}

// Prune drops every record whose peer no longer exists, for state left behind
// by a delete that happened while this file could not be written.
func (r *Registry) Prune(live map[string]bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	changed := false
	for id := range r.recs {
		if !live[id] {
			delete(r.recs, id)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return r.saveLocked()
}

// SetDesired records the version the panel wants this peer's client to run,
// or clears it when version is empty. Every set mints a new request id, which
// is how a client tells "try again" apart from "the same request it already
// gave up on".
func (r *Registry) SetDesired(id, version string) (Record, error) {
	norm := ""
	if strings.TrimSpace(version) != "" {
		v, err := NormalizeRelease(version)
		if err != nil {
			return Record{}, err
		}
		norm = v
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.recs[id]
	if norm == "" {
		rec.DesiredVersion, rec.RequestID, rec.RequestedAt = "", "", nil
	} else {
		now := r.now().UTC()
		rec.DesiredVersion = norm
		rec.RequestID = newRequestID()
		rec.RequestedAt = &now
	}
	r.recs[id] = rec
	if err := r.saveLocked(); err != nil {
		return rec, err
	}
	return rec, nil
}

// Checkin applies a client's report for peer id and returns what it should do.
// changed says whether anything besides the timestamp moved, for logging.
func (r *Registry) Checkin(id string, rep Report) (Answer, Record, bool, error) {
	version := strings.TrimSpace(rep.Version)
	if !reportedVersion.MatchString(version) {
		return Answer{}, Record{}, false, invalid("version %q is not a version string", version)
	}
	flavour := strings.TrimSpace(rep.Flavour)
	switch flavour {
	case FlavourSystemd, FlavourDocker:
	case "":
		flavour = FlavourUnknown
	default:
		flavour = FlavourUnknown
	}

	var upd *Update
	if rep.Update != nil && strings.TrimSpace(rep.Update.State) != "" {
		st := strings.TrimSpace(rep.Update.State)
		switch st {
		case StateUpdating, StateFailed, StateUpdated:
		default:
			return Answer{}, Record{}, false, invalid("update.state %q is not one of updating, failed, updated", st)
		}
		uv := strings.TrimSpace(rep.Update.Version)
		if uv != "" && !reportedVersion.MatchString(uv) {
			return Answer{}, Record{}, false, invalid("update.version %q is not a version string", uv)
		}
		upd = &Update{
			State:     st,
			Version:   uv,
			Error:     sanitize(rep.Update.Error, MaxErrorLen),
			RequestID: sanitize(rep.Update.RequestID, 64),
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	rec := r.recs[id]
	changed := rec.Version != version || rec.Flavour != flavour || !sameBool(rec.RemoteUpdates, rep.RemoteUpdates)

	rec.Version = version
	rec.Flavour = flavour
	rec.RemoteUpdates = copyBool(rep.RemoteUpdates)
	rec.ReportedAt = &now

	if upd != nil && (rec.Update == nil || rec.Update.State != upd.State || rec.Update.Version != upd.Version ||
		rec.Update.Error != upd.Error || rec.Update.RequestID != upd.RequestID) {
		upd.At = now
		rec.Update = upd
		changed = true
	}

	// The client got there: the request is done. Clearing it here rather than
	// leaving it set means a node that is later rolled back by hand (update
	// --version ... --force) is not silently pulled forward again by a request
	// the admin made weeks ago.
	if rec.DesiredVersion != "" {
		if c, ok := Compare(version, rec.DesiredVersion); ok && c >= 0 {
			rec.Update = &Update{State: StateUpdated, Version: version, RequestID: rec.RequestID, At: now}
			rec.DesiredVersion, rec.RequestID, rec.RequestedAt = "", "", nil
			changed = true
		}
	}

	r.recs[id] = rec
	var err error
	if changed || now.Sub(r.lastSave) >= SaveEvery {
		err = r.saveLocked()
	}
	return Answer{DesiredVersion: rec.DesiredVersion, RequestID: rec.RequestID, PollSeconds: PollSeconds}, rec, changed, err
}

func (r *Registry) saveLocked() error {
	cp := make(map[string]Record, len(r.recs))
	for k, v := range r.recs {
		cp[k] = v
	}
	if err := r.store.Save(cp); err != nil {
		return err
	}
	r.lastSave = r.now()
	return nil
}

func sameBool(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func copyBool(b *bool) *bool {
	if b == nil {
		return nil
	}
	v := *b
	return &v
}

func newRequestID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("clients: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
