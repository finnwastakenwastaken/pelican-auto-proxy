package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/clients"
)

// tunnelReq builds a request as the HTTP server would hand it over: the
// connection's local address in the context, the peer's address as
// RemoteAddr.
func tunnelReq(t *testing.T, h http.Handler, local, remote, method, path, body, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	la, err := net.ResolveTCPAddr("tcp", local)
	if err != nil {
		t.Fatal(err)
	}
	r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, la))
	r.RemoteAddr = remote
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func peerIP(t *testing.T, h http.Handler, id string) string {
	t.Helper()
	for _, p := range getPeers(t, h) {
		if p.ID == id {
			return p.TunnelIP
		}
	}
	t.Fatalf("peer %s not listed", id)
	return ""
}

func getPeers(t *testing.T, h http.Handler) []peerResponse {
	t.Helper()
	w := do(t, h, "GET", "/v1/peers", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /v1/peers: %d %s", w.Code, w.Body.String())
	}
	var pr peersResponse
	if err := json.Unmarshal(w.Body.Bytes(), &pr); err != nil {
		t.Fatal(err)
	}
	return pr.Peers
}

const checkin = `{"version":"0.3.0","flavour":"systemd","remote_updates":true}`

func TestTunnelCheckinFromAPeerNeedsNoToken(t *testing.T) {
	_, h, _ := newTest(t, "/bin/true")
	id := addPeer(t, h, "wings-1", "real", "")
	ip := peerIP(t, h, id)

	w := tunnelReq(t, h, "10.66.66.1:7443", ip+":40000", "POST", "/v1/tunnel/checkin", checkin, "")
	if w.Code != http.StatusOK {
		t.Fatalf("check-in from the peer's tunnel address: got %d: %s", w.Code, w.Body.String())
	}
	var ans clients.Answer
	if err := json.Unmarshal(w.Body.Bytes(), &ans); err != nil {
		t.Fatal(err)
	}
	if ans.DesiredVersion != "" || ans.PollSeconds != clients.PollSeconds {
		t.Fatalf("unexpected answer %+v", ans)
	}

	var got *peerResponse
	for _, p := range getPeers(t, h) {
		if p.ID == id {
			p := p
			got = &p
		}
	}
	if got == nil || got.Client.Version == nil || *got.Client.Version != "0.3.0" ||
		got.Client.Flavour == nil || *got.Client.Flavour != "systemd" || got.Client.ReportedAt == nil ||
		got.Client.RemoteUpdates == nil || !*got.Client.RemoteUpdates {
		t.Fatalf("GET /v1/peers does not show the report: %+v", got)
	}
}

// Everything that is not "addressed to the tunnel address, from a peer's own
// tunnel address" must take the token route. This is the whole security
// boundary of the tunnel endpoint.
func TestTunnelCheckinRefusedWithoutTunnelIdentity(t *testing.T) {
	_, h, _ := newTest(t, "/bin/true")
	id := addPeer(t, h, "wings-1", "real", "")
	ip := peerIP(t, h, id)

	for _, c := range []struct{ name, local, remote string }{
		{"public address, peer source", "203.0.113.10:7443", ip + ":40000"},
		{"public address, internet source", "203.0.113.10:7443", "198.51.100.7:40000"},
		{"tunnel address, unknown tunnel source", "10.66.66.1:7443", "10.66.66.200:40000"},
		{"tunnel address, LAN source", "10.66.66.1:7443", "192.168.1.5:40000"},
		{"loopback", "127.0.0.1:7443", "127.0.0.1:40000"},
	} {
		w := tunnelReq(t, h, c.local, c.remote, "POST", "/v1/tunnel/checkin", checkin, "")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: got %d, want 401", c.name, w.Code)
		}
	}
	// With the token the path is simply not an API route.
	if w := tunnelReq(t, h, "203.0.113.10:7443", "198.51.100.7:40000", "POST", "/v1/tunnel/checkin", checkin, token); w.Code != http.StatusNotFound {
		t.Fatalf("token route: got %d, want 404", w.Code)
	}
	for _, p := range getPeers(t, h) {
		if p.Client.Version != nil {
			t.Fatalf("a refused check-in was recorded: %+v", p.Client)
		}
	}
}

// The tunnel identity opens the tunnel routes and nothing else: a peer must
// not get at the token API by calling it over the tunnel.
func TestTunnelIdentityDoesNotOpenTheTokenAPI(t *testing.T) {
	_, h, _ := newTest(t, "/bin/true")
	id := addPeer(t, h, "wings-1", "real", "")
	ip := peerIP(t, h, id)
	for _, path := range []string{"/v1/peers", "/v1/status", "/v1/rules"} {
		if w := tunnelReq(t, h, "10.66.66.1:7443", ip+":40000", "GET", path, "", ""); w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s over the tunnel without a token: got %d, want 401", path, w.Code)
		}
	}
	if w := tunnelReq(t, h, "10.66.66.1:7443", ip+":40000", "GET", "/v1/tunnel/other", "", ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown tunnel route: got %d, want 404", w.Code)
	}
}

// Without a tunnel address configured there is no tunnel identity at all.
func TestTunnelCheckinOffWithoutTunnelIP(t *testing.T) {
	s, h, _ := newTest(t, "/bin/true")
	id := addPeer(t, h, "wings-1", "real", "")
	ip := peerIP(t, h, id)
	s.cfg.TunnelIP = netip.Addr{}
	if w := tunnelReq(t, h, "10.66.66.1:7443", ip+":40000", "POST", "/v1/tunnel/checkin", checkin, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", w.Code)
	}
	if s.guardAddr() != "" {
		t.Fatalf("no tunnel address must mean no guard")
	}
}

func TestSetDesiredVersionAndProgress(t *testing.T) {
	_, h, dir := newTest(t, "/bin/true")
	id := addPeer(t, h, "wings-1", "real", "")
	ip := peerIP(t, h, id)

	// Strict body, like every other token route.
	if w := do(t, h, "PUT", "/v1/peers/"+id+"/client", `{"desired_version":"0.3.1","extra":1}`, token); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: got %d, want 400", w.Code)
	}
	for _, bad := range []string{`"latest"`, `"0.3"`, `"0.3.1; rm -rf /"`, `"../../x"`, `"0.3.1-rc1"`} {
		if w := do(t, h, "PUT", "/v1/peers/"+id+"/client", `{"desired_version":`+bad+`}`, token); w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("desired_version %s: got %d, want 422", bad, w.Code)
		}
	}
	if w := do(t, h, "PUT", "/v1/peers/nope/client", `{"desired_version":"0.3.1"}`, token); w.Code != http.StatusNotFound {
		t.Fatalf("unknown peer: got %d, want 404", w.Code)
	}
	if w := do(t, h, "PUT", "/v1/peers/"+id+"/client", `{"desired_version":"0.3.1"}`, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: got %d, want 401", w.Code)
	}

	w := do(t, h, "PUT", "/v1/peers/"+id+"/client", `{"desired_version":"v0.3.1"}`, token)
	if w.Code != http.StatusOK {
		t.Fatalf("set desired: %d %s", w.Code, w.Body.String())
	}
	var sc setClientResponse
	_ = json.Unmarshal(w.Body.Bytes(), &sc)
	if sc.Client.DesiredVersion == nil || *sc.Client.DesiredVersion != "0.3.1" || sc.Client.RequestID == nil || sc.Client.RequestedAt == nil {
		t.Fatalf("desired not stored normalised with a request id: %+v", sc.Client)
	}
	rid := *sc.Client.RequestID

	// Survives a restart: it is on disk.
	b, err := os.ReadFile(filepath.Join(dir, clients.FileName))
	if err != nil || !strings.Contains(string(b), `"desired_version": "0.3.1"`) {
		t.Fatalf("clients.json does not hold the request: %v %s", err, b)
	}

	// The client checks in on the old version and is told what to do.
	w = tunnelReq(t, h, "10.66.66.1:7443", ip+":40000", "POST", "/v1/tunnel/checkin", `{"version":"0.3.0","flavour":"systemd"}`, "")
	var ans clients.Answer
	_ = json.Unmarshal(w.Body.Bytes(), &ans)
	if ans.DesiredVersion != "0.3.1" || ans.RequestID != rid {
		t.Fatalf("check-in answer does not carry the request: %+v", ans)
	}

	// It fails, and says why; the panel sees the reason.
	time.Sleep(tunnelMinInterval)
	w = tunnelReq(t, h, "10.66.66.1:7443", ip+":40000", "POST", "/v1/tunnel/checkin",
		`{"version":"0.3.0","flavour":"systemd","update":{"state":"failed","version":"0.3.1","request_id":"`+rid+`","error":"checksum mismatch\u0007"}}`, "")
	if w.Code != http.StatusOK {
		t.Fatalf("failed report: %d %s", w.Code, w.Body.String())
	}
	p := getPeers(t, h)[0]
	if p.Client.Update == nil || p.Client.Update.State != "failed" || p.Client.Update.Error != "checksum mismatch" || p.Client.Update.RequestID != rid {
		t.Fatalf("failure not visible: %+v", p.Client.Update)
	}
	if p.Client.DesiredVersion == nil {
		t.Fatalf("a failure must not withdraw the request")
	}

	// It gets there: the request is done and withdrawn.
	time.Sleep(tunnelMinInterval)
	w = tunnelReq(t, h, "10.66.66.1:7443", ip+":40000", "POST", "/v1/tunnel/checkin", `{"version":"0.3.1","flavour":"systemd","future_field":{"x":1}}`, "")
	_ = json.Unmarshal(w.Body.Bytes(), &ans)
	if w.Code != http.StatusOK || ans.DesiredVersion != "" {
		t.Fatalf("after reaching the version the request should be gone: %d %+v", w.Code, ans)
	}
	p = getPeers(t, h)[0]
	if p.Client.Update == nil || p.Client.Update.State != "updated" || p.Client.Update.Version != "0.3.1" || p.Client.DesiredVersion != nil {
		t.Fatalf("progress should read updated: %+v", p.Client)
	}

	// Withdrawing works with null.
	if w := do(t, h, "PUT", "/v1/peers/"+id+"/client", `{"desired_version":null}`, token); w.Code != http.StatusOK {
		t.Fatalf("clear: %d", w.Code)
	}
}

func TestTunnelCheckinThrottledAndValidated(t *testing.T) {
	_, h, _ := newTest(t, "/bin/true")
	id := addPeer(t, h, "wings-1", "real", "")
	ip := peerIP(t, h, id)
	if w := tunnelReq(t, h, "10.66.66.1:7443", ip+":40000", "POST", "/v1/tunnel/checkin", checkin, ""); w.Code != http.StatusOK {
		t.Fatalf("first: %d", w.Code)
	}
	if w := tunnelReq(t, h, "10.66.66.1:7443", ip+":40000", "POST", "/v1/tunnel/checkin", checkin, ""); w.Code != http.StatusTooManyRequests {
		t.Fatalf("immediate second check-in: got %d, want 429", w.Code)
	}
	time.Sleep(tunnelMinInterval)
	if w := tunnelReq(t, h, "10.66.66.1:7443", ip+":40000", "POST", "/v1/tunnel/checkin", `{"version":"$(reboot)"}`, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("bad version: got %d, want 400", w.Code)
	}
	time.Sleep(tunnelMinInterval)
	big := `{"version":"0.3.0","pad":"` + strings.Repeat("x", MaxTunnelBody) + `"}`
	if w := tunnelReq(t, h, "10.66.66.1:7443", ip+":40000", "POST", "/v1/tunnel/checkin", big, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("oversized body: got %d, want 400", w.Code)
	}
}

func TestDeletedPeerForgetsItsClientState(t *testing.T) {
	_, h, dir := newTest(t, "/bin/true")
	id := addPeer(t, h, "wings-1", "real", "")
	ip := peerIP(t, h, id)
	tunnelReq(t, h, "10.66.66.1:7443", ip+":40000", "POST", "/v1/tunnel/checkin", checkin, "")
	if w := do(t, h, "DELETE", "/v1/peers/"+id, "", token); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", w.Code)
	}
	b, _ := os.ReadFile(filepath.Join(dir, clients.FileName))
	if strings.Contains(string(b), id) {
		t.Fatalf("deleted peer still in clients.json: %s", b)
	}
}

func TestRestoreRendersTheGuardWithNoStoredRules(t *testing.T) {
	s, _, dir := newTest(t, "/bin/true")
	s.Restore()
	b, err := os.ReadFile(filepath.Join(dir, "rules.nft"))
	if err != nil || !strings.Contains(string(b), "chain tunnel_guard") {
		t.Fatalf("a fresh agent must render its table (with the guard) at start: %v %s", err, b)
	}
}
