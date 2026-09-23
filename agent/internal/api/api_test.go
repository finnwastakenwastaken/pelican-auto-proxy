package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/nft"
	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/peers"
	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/rules"
	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/state"
	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/wg"
)

const token = "s3cret"

// noopRun swallows every wg and ip command: these tests are about the API, not
// about the kernel, and rule 4 of this repository forbids touching the host's
// network stack.
func noopRun(string, ...string) (string, error) { return "", nil }

var keyCounter int

func stubKeygen() (string, string, error) {
	keyCounter++
	return fmt.Sprintf("privkey-%d", keyCounter), fmt.Sprintf("pubkey-%d", keyCounter), nil
}

// newPeers builds a peer manager backed by dir, with readable stub keys.
func newPeers(t *testing.T, dir string) *peers.Manager {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pm := peers.NewManager(peers.Config{
		Subnet:    netip.MustParsePrefix("10.66.66.0/24"),
		VPSIP:     netip.MustParseAddr("10.66.66.1"),
		Endpoint:  "203.0.113.10:51820",
		VPSPubKey: "vps-pubkey",
	}, peers.Store{Dir: dir}, wg.Manager{Run: noopRun}, log)
	pm.SetKeygen(stubKeygen)
	return pm
}

// newTest builds a server whose "nft" is /bin/true (or /bin/false to simulate
// a kernel that refuses the ruleset).
func newTest(t *testing.T, nftBin string) (*Server, http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	rulesFile := filepath.Join(dir, "rules.nft")
	envFile := filepath.Join(dir, "agent.env")
	if err := os.WriteFile(envFile, []byte("# comment\nAUTOPROXY_TOKEN="+token+"\nAUTOPROXY_LISTEN=0.0.0.0:7443\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(Config{
		Token:       token,
		PublicIface: "eth0",
		WGIface:     "wg0",
		Reserved:    []int{22, 51820, 7443},
		RulesFile:   rulesFile,
		EnvFile:     envFile,
		Version:     "test",
		TunnelIP:    netip.MustParseAddr("10.66.66.1"),
	},
		state.Store{Dir: dir},
		nft.Applier{Bin: nftBin},
		wg.Reader{Bin: "/bin/false", Iface: "wg0"},
		wg.Manager{Run: noopRun},
		newPeers(t, dir),
		log,
	)
	return s, s.Handler(), dir
}

// addPeer creates a peer through the API and returns its id.
func addPeer(t *testing.T, h http.Handler, name, mode string, cidrs string) string {
	t.Helper()
	body := fmt.Sprintf(`{"name":%q,"mode":%q`, name, mode)
	if cidrs != "" {
		body += `,"lan_cidrs":[` + cidrs + `]`
	}
	body += "}"
	w := do(t, h, "POST", "/v1/peers", body, token)
	if w.Code != http.StatusCreated {
		t.Fatalf("creating peer %s: got %d: %s", name, w.Code, w.Body.String())
	}
	var resp createPeerResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Peer.ID
}

func do(t *testing.T, h http.Handler, method, path, body, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAuthRequiredOnEveryRoute(t *testing.T) {
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/v1/status", ""},
		{"GET", "/v1/rules", ""},
		{"PUT", "/v1/rules", `{"rules":[]}`},
		{"GET", "/v1/peers", ""},
		{"POST", "/v1/peers", `{"name":"a","mode":"real"}`},
		{"GET", "/anything", ""},
	} {
		// A fresh server per route: after five failures the lockout takes
		// over and every later attempt is a 429, which is correct but is not
		// what this test is about.
		_, h, _ := newTest(t, "/bin/true")
		if w := do(t, h, c.method, c.path, c.body, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token: got %d, want 401", c.method, c.path, w.Code)
		}
		if w := do(t, h, c.method, c.path, c.body, "wrong"); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with a wrong token: got %d, want 401", c.method, c.path, w.Code)
		}
	}
}

func TestEmptyConfiguredTokenRejectsEverything(t *testing.T) {
	// Defence in depth: main refuses to start without a token, but if that
	// ever regressed, an empty token must not mean "no auth".
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	s := New(Config{RulesFile: filepath.Join(dir, "r.nft")}, state.Store{Dir: dir},
		nft.Applier{Bin: "/bin/true"}, wg.Reader{Bin: "/bin/false"}, wg.Manager{Run: noopRun}, newPeers(t, dir), log)
	if w := do(t, s.Handler(), "GET", "/v1/status", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", w.Code)
	}
}

func TestStatusAndEmptyRules(t *testing.T) {
	_, h, _ := newTest(t, "/bin/true")
	w := do(t, h, "GET", "/v1/status", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	var st statusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Version != "test" {
		t.Fatalf("version missing: %+v", st)
	}
	if st.AppliedAt != nil {
		t.Fatalf("nothing applied yet, applied_at should be null: %+v", st)
	}
	if st.WG.Error == "" {
		t.Fatalf("wg error should be surfaced when wg cannot be read: %+v", st.WG)
	}

	w = do(t, h, "GET", "/v1/rules", "", token)
	var rr rulesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &rr); err != nil {
		t.Fatal(err)
	}
	if rr.Rules == nil || len(rr.Rules) != 0 {
		t.Fatalf("expected an empty array, got %v", rr.Rules)
	}
}

func TestPutAppliesPersistsAndShows(t *testing.T) {
	s, h, dir := newTest(t, "/bin/true")
	id := addPeer(t, h, "lan-box", "site", `"10.0.0.0/24"`)
	body := fmt.Sprintf(`{"rules":[{"id":"alloc-1","proto":"both","public_port":9445,"public_port_end":null,"target_ip":"10.0.0.10","via_peer":%q,"target_port":null,"note":"Palworld"}]}`, id)
	w := do(t, h, "PUT", "/v1/rules", body, token)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	var pr putResponse
	if err := json.Unmarshal(w.Body.Bytes(), &pr); err != nil {
		t.Fatal(err)
	}
	if pr.Applied.TCP != 1 || pr.Applied.UDP != 1 || pr.Applied.Rules != 1 {
		t.Fatalf("unexpected counts %+v", pr.Applied)
	}

	// The rendered file landed where nft would read it.
	b, err := os.ReadFile(filepath.Join(dir, "rules.nft"))
	if err != nil || !strings.Contains(string(b), "9445 : 10.0.0.10") {
		t.Fatalf("rendered file wrong: %v %s", err, string(b))
	}
	// And the state survives a restart.
	snap, err := (state.Store{Dir: dir}).Load()
	if err != nil || snap == nil || len(snap.Rules) != 1 {
		t.Fatalf("state not persisted: %+v %v", snap, err)
	}

	w = do(t, h, "GET", "/v1/rules", "", token)
	if !strings.Contains(w.Body.String(), "alloc-1") {
		t.Fatalf("GET /v1/rules did not return the applied set: %s", w.Body.String())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastError != "" {
		t.Fatalf("unexpected last_error %q", s.lastError)
	}
}

func TestPutRejectedRulesReturn422AndChangeNothing(t *testing.T) {
	_, h, dir := newTest(t, "/bin/true")
	id := addPeer(t, h, "lan-box", "site", `"10.0.0.0/24"`)
	body := fmt.Sprintf(`{"rules":[{"id":"bad","proto":"tcp","public_port":22,"target_ip":"10.0.0.10","via_peer":%q}]}`, id)
	w := do(t, h, "PUT", "/v1/rules", body, token)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	var rr rejectResponse
	if err := json.Unmarshal(w.Body.Bytes(), &rr); err != nil {
		t.Fatal(err)
	}
	if len(rr.Rejected) != 1 || rr.Rejected[0].ID != "bad" || !strings.Contains(rr.Rejected[0].Reason, "reserved") {
		t.Fatalf("unexpected rejection payload %+v", rr.Rejected)
	}
	if _, err := os.Stat(filepath.Join(dir, "rules.nft")); err == nil {
		t.Fatal("a rejected rule set must not write the nft file")
	}
}

func TestPutNftFailureReturns500AndSetsLastError(t *testing.T) {
	s, h, _ := newTest(t, "/bin/false")
	id := addPeer(t, h, "wings-1", "real", "")
	body := fmt.Sprintf(`{"rules":[{"id":"a","proto":"tcp","public_port":9000,"target_peer":%q}]}`, id)
	w := do(t, h, "PUT", "/v1/rules", body, token)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "nft") {
		t.Fatalf("the nft failure should be reported to the caller: %s", w.Body.String())
	}
	w = do(t, h, "GET", "/v1/status", "", token)
	var st statusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.LastError == "" {
		t.Fatal("last_error must be set after a failed apply")
	}
	_ = s
}

func TestPutBadJSON(t *testing.T) {
	_, h, _ := newTest(t, "/bin/true")
	if w := do(t, h, "PUT", "/v1/rules", `{"rules":`, token); w.Code != http.StatusBadRequest {
		t.Fatalf("got %d", w.Code)
	}
	if w := do(t, h, "PUT", "/v1/rules", `{"rulez":[]}`, token); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown fields should be refused, got %d", w.Code)
	}
}

func TestPutEmptySetClosesEveryPort(t *testing.T) {
	_, h, dir := newTest(t, "/bin/true")
	id := addPeer(t, h, "wings-1", "real", "")
	body := fmt.Sprintf(`{"rules":[{"id":"a","proto":"tcp","public_port":9000,"target_peer":%q}]}`, id)
	if w := do(t, h, "PUT", "/v1/rules", body, token); w.Code != 200 {
		t.Fatalf("setup failed: %s", w.Body.String())
	}
	w := do(t, h, "PUT", "/v1/rules", `{"rules":[]}`, token)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	b, _ := os.ReadFile(filepath.Join(dir, "rules.nft"))
	if strings.Contains(string(b), "9000") {
		t.Fatalf("the port was not removed:\n%s", string(b))
	}
}

func TestBodyLimit(t *testing.T) {
	_, h, _ := newTest(t, "/bin/true")
	big := `{"rules":[` + strings.Repeat(`{"id":"a","proto":"tcp","public_port":9000,"target_peer":"x","note":"`+strings.Repeat("x", 500)+`"},`, 3000)
	big = strings.TrimSuffix(big, ",") + `]}`
	if len(big) <= MaxBody {
		t.Fatalf("test body is only %d bytes, needs to exceed %d", len(big), MaxBody)
	}
	w := do(t, h, "PUT", "/v1/rules", big, token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized body should be refused, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRestoreReappliesStoredRules(t *testing.T) {
	dir := t.TempDir()
	rulesFile := filepath.Join(dir, "rules.nft")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Create the peer first: the stored rule names it, and peers.json is what
	// makes that name resolvable after a reboot.
	pm := newPeers(t, dir)
	p, _, err := pm.Create("wings-1", peers.ModeReal, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := (state.Store{Dir: dir}).Save(state.Snapshot{Rules: []rules.Rule{
		{ID: "alloc-1", Proto: "tcp", PublicPort: 9445, TargetPeer: p.ID},
	}}); err != nil {
		t.Fatal(err)
	}

	s := New(Config{Token: token, PublicIface: "eth0", WGIface: "wg0", RulesFile: rulesFile},
		state.Store{Dir: dir}, nft.Applier{Bin: "/bin/true"}, wg.Reader{Bin: "/bin/false"},
		wg.Manager{Run: noopRun}, newPeers(t, dir), log)
	s.Restore()

	b, err := os.ReadFile(rulesFile)
	if err != nil || !strings.Contains(string(b), "9445") {
		t.Fatalf("stored rules were not re-applied: %v %s", err, string(b))
	}
	if !strings.Contains(string(b), "10.66.66.2") {
		t.Fatalf("the rule should resolve to the peer's tunnel address:\n%s", string(b))
	}
	w := do(t, s.Handler(), "GET", "/v1/rules", "", token)
	if !strings.Contains(w.Body.String(), "alloc-1") {
		t.Fatalf("restored rules not visible: %s", w.Body.String())
	}
}

// A rule whose peer was deleted while the agent was down must not take the
// working forwards down with it. Refusing the whole stored set would close
// every game port because of one stale entry.
func TestRestoreDropsRulesForPeersThatNoLongerExist(t *testing.T) {
	dir := t.TempDir()
	rulesFile := filepath.Join(dir, "rules.nft")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	pm := newPeers(t, dir)
	p, _, err := pm.Create("wings-1", peers.ModeReal, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := (state.Store{Dir: dir}).Save(state.Snapshot{Rules: []rules.Rule{
		{ID: "stale", Proto: "tcp", PublicPort: 9000, TargetPeer: "deleted-peer"},
		{ID: "good", Proto: "tcp", PublicPort: 9445, TargetPeer: p.ID},
	}}); err != nil {
		t.Fatal(err)
	}

	s := New(Config{Token: token, PublicIface: "eth0", WGIface: "wg0", RulesFile: rulesFile},
		state.Store{Dir: dir}, nft.Applier{Bin: "/bin/true"}, wg.Reader{Bin: "/bin/false"},
		wg.Manager{Run: noopRun}, newPeers(t, dir), log)
	s.Restore()

	b, _ := os.ReadFile(rulesFile)
	if !strings.Contains(string(b), "9445") {
		t.Fatalf("the healthy rule must still be applied:\n%s", string(b))
	}
	if strings.Contains(string(b), "9000") {
		t.Fatalf("the stale rule must not be applied:\n%s", string(b))
	}
	w := do(t, s.Handler(), "GET", "/v1/status", "", token)
	if !strings.Contains(w.Body.String(), "dropped on restore") {
		t.Fatalf("last_error must say a rule was dropped, or the admin never finds out: %s", w.Body.String())
	}
}

func TestRestoreFailureIsNotFatal(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pm := newPeers(t, dir)
	p, _, err := pm.Create("wings-1", peers.ModeReal, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := (state.Store{Dir: dir}).Save(state.Snapshot{Rules: []rules.Rule{
		{ID: "alloc-1", Proto: "tcp", PublicPort: 9445, TargetPeer: p.ID},
	}}); err != nil {
		t.Fatal(err)
	}
	s := New(Config{Token: token, RulesFile: filepath.Join(dir, "rules.nft")},
		state.Store{Dir: dir}, nft.Applier{Bin: "/bin/false"}, wg.Reader{Bin: "/bin/false"},
		wg.Manager{Run: noopRun}, newPeers(t, dir), log)
	s.Restore() // must not panic or exit

	w := do(t, s.Handler(), "GET", "/v1/status", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("the agent must keep serving after a failed restore, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "restore") {
		t.Fatalf("last_error should explain the failed restore: %s", w.Body.String())
	}
}

func TestUnknownPathIs404(t *testing.T) {
	_, h, _ := newTest(t, "/bin/true")
	if w := do(t, h, "GET", "/v1/nope", "", token); w.Code != http.StatusNotFound {
		t.Fatalf("got %d", w.Code)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	_, h, _ := newTest(t, "/bin/true")
	if w := do(t, h, "POST", "/v1/rules", `{"rules":[]}`, token); w.Code == http.StatusOK {
		t.Fatal("POST /v1/rules must not apply anything")
	}
}

// --- peers -----------------------------------------------------------------

func TestCreatePeerReturnsAJoinCodeExactlyOnce(t *testing.T) {
	_, h, _ := newTest(t, "/bin/true")
	w := do(t, h, "POST", "/v1/peers", `{"name":"wings-1","mode":"real"}`, token)
	if w.Code != http.StatusCreated {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	var resp createPeerResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.JoinCode == "" || resp.Warning == "" {
		t.Fatalf("a join code must come with the warning that it is shown once: %+v", resp)
	}
	code, err := peers.DecodeJoinCode(resp.JoinCode)
	if err != nil {
		t.Fatal(err)
	}
	if code.ClientPriv == "" || code.ClientAddr != "10.66.66.2/32" || code.Mode != "real" {
		t.Fatalf("join code is not usable: %+v", code)
	}

	// The private key must not come back from any later read.
	w = do(t, h, "GET", "/v1/peers", "", token)
	if strings.Contains(w.Body.String(), code.ClientPriv) {
		t.Fatalf("GET /v1/peers leaked the private key: %s", w.Body.String())
	}
}

func TestListPeersReportsHandshakeAndCounters(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pm := newPeers(t, dir)
	pm.SetKeygen(func() (string, string, error) { return "privkey-x", "pubkey-x", nil })
	p, _, err := pm.Create("wings-1", peers.ModeReal, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A "wg show wg0 dump" line for that peer: pubkey, psk, endpoint,
	// allowed-ips, handshake, rx, tx, keepalive.
	hs := time.Now().Unix() - 30
	dump := fmt.Sprintf("iface\tline\there\tnow\npubkey-x\t(none)\t198.51.100.7:51820\t10.66.66.2/32\t%d\t1024\t2048\t25\n", hs)
	s := New(Config{Token: token, RulesFile: filepath.Join(dir, "rules.nft"), Version: "test"},
		state.Store{Dir: dir}, nft.Applier{Bin: "/bin/true"}, wg.Reader{Bin: "/bin/false"},
		wg.Manager{Run: func(string, ...string) (string, error) { return dump, nil }}, pm, log)

	w := do(t, s.Handler(), "GET", "/v1/peers", "", token)
	var resp peersResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Peers) != 1 {
		t.Fatalf("expected one peer, got %+v", resp.Peers)
	}
	got := resp.Peers[0]
	if got.ID != p.ID || got.TunnelIP != "10.66.66.2" || got.Mode != "real" {
		t.Fatalf("peer record wrong: %+v", got)
	}
	if got.HandshakeAge == nil || *got.HandshakeAge < 29 || *got.HandshakeAge > 35 {
		t.Fatalf("handshake age wrong: %+v", got.HandshakeAge)
	}
	if got.RX != 1024 || got.TX != 2048 {
		t.Fatalf("counters wrong: rx=%d tx=%d", got.RX, got.TX)
	}

	// The status summary counts the same peer as healthy.
	w = do(t, s.Handler(), "GET", "/v1/status", "", token)
	var st statusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Peers.Total != 1 || st.Peers.Healthy != 1 {
		t.Fatalf("expected 1 total / 1 healthy, got %+v", st.Peers)
	}
}

// A peer that has not handshaked in over three minutes is not healthy. Without
// this, a node whose client died reads as fine and nobody looks at it.
func TestStalePeerIsNotHealthy(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pm := newPeers(t, dir)
	pm.SetKeygen(func() (string, string, error) { return "privkey-x", "pubkey-x", nil })
	if _, _, err := pm.Create("wings-1", peers.ModeReal, nil); err != nil {
		t.Fatal(err)
	}
	dump := fmt.Sprintf("iface\npubkey-x\t(none)\t198.51.100.7:51820\t10.66.66.2/32\t%d\t0\t0\t25\n", time.Now().Unix()-600)
	s := New(Config{Token: token, RulesFile: filepath.Join(dir, "rules.nft")},
		state.Store{Dir: dir}, nft.Applier{Bin: "/bin/true"}, wg.Reader{Bin: "/bin/false"},
		wg.Manager{Run: func(string, ...string) (string, error) { return dump, nil }}, pm, log)
	w := do(t, s.Handler(), "GET", "/v1/status", "", token)
	var st statusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Peers.Total != 1 || st.Peers.Healthy != 0 {
		t.Fatalf("a 10-minute-old handshake is not healthy, got %+v", st.Peers)
	}
}

func TestCreatePeerValidationIs422(t *testing.T) {
	_, h, _ := newTest(t, "/bin/true")
	for _, body := range []string{
		`{"name":"","mode":"real"}`,
		`{"name":"a","mode":"sideways"}`,
		`{"name":"a","mode":"site"}`,
		`{"name":"a","mode":"site","lan_cidrs":["203.0.113.0/24"]}`,
		`{"name":"a","mode":"real","lan_cidrs":["10.0.0.0/24"]}`,
	} {
		if w := do(t, h, "POST", "/v1/peers", body, token); w.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: got %d, want 422 (%s)", body, w.Code, w.Body.String())
		}
	}
}

// Deleting a peer has to close its public ports in the same breath. A forward
// left pointing at a tunnel address nothing answers on is a port open to the
// internet that looks closed.
func TestDeletePeerClosesItsForwards(t *testing.T) {
	_, h, dir := newTest(t, "/bin/true")
	a := addPeer(t, h, "wings-1", "real", "")
	b := addPeer(t, h, "wings-2", "real", "")
	body := fmt.Sprintf(`{"rules":[{"id":"ra","proto":"tcp","public_port":9000,"target_peer":%q},{"id":"rb","proto":"tcp","public_port":9001,"target_peer":%q}]}`, a, b)
	if w := do(t, h, "PUT", "/v1/rules", body, token); w.Code != http.StatusOK {
		t.Fatalf("push failed: %s", w.Body.String())
	}

	if w := do(t, h, "DELETE", "/v1/peers/"+a, "", token); w.Code != http.StatusNoContent {
		t.Fatalf("delete failed: %d %s", w.Code, w.Body.String())
	}
	rendered, _ := os.ReadFile(filepath.Join(dir, "rules.nft"))
	if strings.Contains(string(rendered), "9000") {
		t.Fatalf("the deleted peer's port is still open:\n%s", string(rendered))
	}
	if !strings.Contains(string(rendered), "9001") {
		t.Fatalf("the surviving peer's port was closed too:\n%s", string(rendered))
	}
	// And its tunnel address is out of direct_targets, so nothing that reuses
	// that address later inherits "do not masquerade".
	if strings.Contains(string(rendered), "10.66.66.2,") || strings.Contains(string(rendered), "{ 10.66.66.2 }") {
		t.Fatalf("the deleted peer is still in direct_targets:\n%s", string(rendered))
	}

	// A later push that still names the deleted peer is refused outright.
	if w := do(t, h, "PUT", "/v1/rules", body, token); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a push naming a deleted peer must be rejected, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeleteUnknownPeerIs404(t *testing.T) {
	_, h, _ := newTest(t, "/bin/true")
	if w := do(t, h, "DELETE", "/v1/peers/nope", "", token); w.Code != http.StatusNotFound {
		t.Fatalf("got %d", w.Code)
	}
	if w := do(t, h, "POST", "/v1/peers/nope/rotate", "", token); w.Code != http.StatusNotFound {
		t.Fatalf("rotate: got %d", w.Code)
	}
}

func TestRotatePeerIssuesANewCode(t *testing.T) {
	_, h, _ := newTest(t, "/bin/true")
	id := addPeer(t, h, "wings-1", "real", "")
	w := do(t, h, "GET", "/v1/peers", "", token)
	var before peersResponse
	_ = json.Unmarshal(w.Body.Bytes(), &before)

	w = do(t, h, "POST", "/v1/peers/"+id+"/rotate", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	var resp createPeerResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Peer.ID != id || resp.Peer.TunnelIP != before.Peers[0].TunnelIP {
		t.Fatalf("rotate must keep the id and address: %+v", resp.Peer)
	}
	if resp.Peer.PublicKey == before.Peers[0].PublicKey {
		t.Fatal("rotate must change the public key")
	}
	if resp.JoinCode == "" {
		t.Fatal("rotate must return a new join code")
	}
}

// Adding a real-IP peer changes @direct_targets even though no rule changed.
// Missing this means the first player on that node is silently masqueraded.
func TestCreatingARealPeerRefreshesDirectTargets(t *testing.T) {
	_, h, dir := newTest(t, "/bin/true")
	a := addPeer(t, h, "wings-1", "real", "")
	body := fmt.Sprintf(`{"rules":[{"id":"ra","proto":"tcp","public_port":9000,"target_peer":%q}]}`, a)
	if w := do(t, h, "PUT", "/v1/rules", body, token); w.Code != http.StatusOK {
		t.Fatalf("push failed: %s", w.Body.String())
	}
	addPeer(t, h, "wings-2", "real", "")
	rendered, _ := os.ReadFile(filepath.Join(dir, "rules.nft"))
	if !strings.Contains(string(rendered), "10.66.66.3") {
		t.Fatalf("the new peer is not in direct_targets:\n%s", string(rendered))
	}
	if !strings.Contains(string(rendered), "9000") {
		t.Fatalf("the existing forward was lost:\n%s", string(rendered))
	}
}

// --- lockout ---------------------------------------------------------------

func TestLockoutAfterFiveFailures(t *testing.T) {
	s, _, _ := newTest(t, "/bin/true")
	now := time.Unix(1700000000, 0)
	s.SetClock(func() time.Time { return now })
	h := s.Handler()

	for i := 0; i < MaxFailures; i++ {
		if w := do(t, h, "GET", "/v1/status", "", "wrong"); w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, want 401", i+1, w.Code)
		}
	}
	w := do(t, h, "GET", "/v1/status", "", "wrong")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("the sixth attempt must be locked out, got %d", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != "900" {
		t.Fatalf("Retry-After should be 900, got %q", got)
	}
	// The correct token is refused too while the lockout stands: otherwise an
	// attacker who guesses on attempt six is not slowed down at all.
	if w := do(t, h, "GET", "/v1/status", "", token); w.Code != http.StatusTooManyRequests {
		t.Fatalf("a locked-out address must be refused even with the right token, got %d", w.Code)
	}

	now = now.Add(LockoutFor + time.Second)
	if w := do(t, h, "GET", "/v1/status", "", token); w.Code != http.StatusOK {
		t.Fatalf("the lockout must expire, got %d", w.Code)
	}
}

func TestSuccessfulAuthResetsTheCounter(t *testing.T) {
	s, _, _ := newTest(t, "/bin/true")
	h := s.Handler()
	for i := 0; i < MaxFailures-1; i++ {
		do(t, h, "GET", "/v1/status", "", "wrong")
	}
	if w := do(t, h, "GET", "/v1/status", "", token); w.Code != http.StatusOK {
		t.Fatalf("got %d", w.Code)
	}
	// Four more failures must not trip the lockout now that the count reset.
	for i := 0; i < MaxFailures-1; i++ {
		if w := do(t, h, "GET", "/v1/status", "", "wrong"); w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, want 401", i+1, w.Code)
		}
	}
}

func TestLockoutIsPerAddress(t *testing.T) {
	s, _, _ := newTest(t, "/bin/true")
	h := s.Handler()
	for i := 0; i < MaxFailures+1; i++ {
		r := httptest.NewRequest("GET", "/v1/status", nil)
		r.RemoteAddr = "198.51.100.5:40000"
		r.Header.Set("Authorization", "Bearer wrong")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
	}
	// A different address is unaffected: one noisy source must not be able to
	// lock the real panel out.
	r := httptest.NewRequest("GET", "/v1/status", nil)
	r.RemoteAddr = "203.0.113.9:40000"
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("another address must not be locked out, got %d", w.Code)
	}
}

func TestLockoutTableIsPurgedAndCapped(t *testing.T) {
	now := time.Unix(1700000000, 0)
	l := newLockout(func() time.Time { return now })
	for i := 0; i < 5; i++ {
		l.fail(fmt.Sprintf("192.0.2.%d", i))
	}
	if l.size() != 5 {
		t.Fatalf("expected 5 tracked addresses, got %d", l.size())
	}
	now = now.Add(IdlePurge + time.Minute)
	l.fail("198.51.100.1")
	if l.size() != 1 {
		t.Fatalf("idle entries should be purged, got %d", l.size())
	}
}

// --- token rotate ----------------------------------------------------------

func TestTokenRotate(t *testing.T) {
	_, h, dir := newTest(t, "/bin/true")
	w := do(t, h, "POST", "/v1/token/rotate", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	var resp tokenRotateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Token) != 64 || resp.Token == token {
		t.Fatalf("expected a fresh 32-byte hex token, got %q", resp.Token)
	}
	if resp.Warning == "" {
		t.Fatal("rotating a token that is shown once needs a warning")
	}

	// The old token stops working and the new one works.
	if w := do(t, h, "GET", "/v1/status", "", token); w.Code != http.StatusUnauthorized {
		t.Fatalf("the old token must stop working, got %d", w.Code)
	}
	if w := do(t, h, "GET", "/v1/status", "", resp.Token); w.Code != http.StatusOK {
		t.Fatalf("the new token must work, got %d", w.Code)
	}

	// And it survives a restart, with the rest of agent.env intact.
	b, err := os.ReadFile(filepath.Join(dir, "agent.env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "AUTOPROXY_TOKEN="+resp.Token) {
		t.Fatalf("the new token was not persisted:\n%s", string(b))
	}
	if !strings.Contains(string(b), "AUTOPROXY_LISTEN=0.0.0.0:7443") || !strings.Contains(string(b), "# comment") {
		t.Fatalf("rewriting the token clobbered the rest of agent.env:\n%s", string(b))
	}
	fi, _ := os.Stat(filepath.Join(dir, "agent.env"))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("agent.env holds the token; mode must stay 0600, got %v", fi.Mode().Perm())
	}
}

// If the new token cannot be written down, it must not become the live token:
// a restart would silently fall back to the old one.
func TestTokenRotateDoesNotSwitchWhenItCannotPersist(t *testing.T) {
	s, h, _ := newTest(t, "/bin/true")
	s.cfg.EnvFile = filepath.Join(t.TempDir(), "does-not-exist.env")
	if w := do(t, h, "POST", "/v1/token/rotate", "", token); w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	if w := do(t, h, "GET", "/v1/status", "", token); w.Code != http.StatusOK {
		t.Fatalf("the old token must still work after a failed rotate, got %d", w.Code)
	}
}

func TestUpdateEnvFileRefusesAMissingKey(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "agent.env")
	if err := os.WriteFile(p, []byte("AUTOPROXY_LISTEN=0.0.0.0:7443\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UpdateEnvFile(p, TokenEnvKey, "x"); err == nil {
		t.Fatal("silently appending a key would leave two sources of truth; expected an error")
	}
}
