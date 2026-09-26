package setup

import (
	"crypto/md5" //nolint:gosec // matching dpkg's recorded conffile digest
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/wg"
)

func writeOSRelease(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "os-release")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// The OS gate is a promise: we test on these two and nowhere else. A
// best-effort install on an untested platform is how a stranger ends up with a
// half-working firewall and no idea why. Ubuntu gets a distinct message
// pointing at a later release rather than a generic refusal.
func TestOSGate(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{"ID=debian\nVERSION_ID=\"12\"\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\n", true},
		{"ID=debian\nVERSION_ID=\"13\"\n", true},
		{"ID=ubuntu\nVERSION_ID=\"22.04\"\n", false},
		{"ID=ubuntu\nVERSION_ID=\"24.04\"\n", false},
		{"ID=debian\nVERSION_ID=\"11\"\n", false},
		{"ID=ubuntu\nVERSION_ID=\"20.04\"\n", false},
		{"ID=ubuntu\nVERSION_ID=\"25.04\"\n", false},
		{"ID=centos\nVERSION_ID=\"9\"\n", false},
		{"ID=arch\n", false},
		{"", false},
	}
	for _, tc := range cases {
		info, err := DetectOS(writeOSRelease(t, tc.body))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Supported(); got != tc.want {
			t.Errorf("%q: supported=%v, want %v", strings.ReplaceAll(tc.body, "\n", " "), got, tc.want)
		}
		if tc.want {
			continue
		}
		msg := info.UnsupportedMessage()
		if info.ID == "ubuntu" {
			for _, want := range []string{"Ubuntu is not supported in this release", "Debian 12 or 13", "tracked for a later release"} {
				if !strings.Contains(msg, want) {
					t.Errorf("Ubuntu must get the explicit follow-up message; %q missing from:\n%s", want, msg)
				}
			}
			continue
		}
		for _, want := range []string{"Debian 12", "Debian 13"} {
			if !strings.Contains(msg, want) {
				t.Errorf("the refusal must name what does work; %q missing from:\n%s", want, msg)
			}
		}
	}
}

func TestDetectOSMissingFile(t *testing.T) {
	if _, err := DetectOS(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected an error")
	}
}

func TestDetectNetParsesTheDefaultRoute(t *testing.T) {
	run := func(string, ...string) (string, error) {
		return "1.1.1.1 via 203.0.113.1 dev eth0 src 203.0.113.10 uid 0 \\    cache", nil
	}
	n, err := DetectNet(run)
	if err != nil {
		t.Fatal(err)
	}
	if n.Iface != "eth0" || n.PublicIP != "203.0.113.10" {
		t.Fatalf("unexpected %+v", n)
	}
}

func TestDetectNetUnparseable(t *testing.T) {
	run := func(string, ...string) (string, error) { return "nonsense", nil }
	if _, err := DetectNet(run); err == nil {
		t.Fatal("expected an error rather than an empty interface name")
	}
}

func TestVPSCodeRoundTrip(t *testing.T) {
	c := VPSCode{
		V: VPSCodeVersion, EndpointIP: "203.0.113.10", APIPort: 7443,
		APICAPEM: "cGVt", APISPKISHA256: "cGlu", Token: "t0ken",
		WGPubKey: "vps-pubkey", WGPort: 51820,
		TunnelSubnet: "10.66.66.0/24", VPSTunnelIP: "10.66.66.1", Version: "v1.0.0",
	}
	enc, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(enc, "+/=") {
		t.Fatalf("the VPS code is pasted into a web form and a shell; it must be base64url without padding: %s", enc)
	}
	back, err := DecodeVPSCode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if back != c {
		t.Fatalf("round trip lost data:\n got %+v\nwant %+v", back, c)
	}
}

func TestDecodeVPSCodeRejectsRubbish(t *testing.T) {
	if _, err := DecodeVPSCode("!!!"); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := DecodeVPSCode("eyJ2Ijo5OTl9"); err == nil || !strings.Contains(err.Error(), "version 999") {
		t.Fatalf("an unknown version must be named, got %v", err)
	}
}

// The base ruleset is the one file that can lock an operator out of their own
// VPS. Every port it opens is checked here.
func TestBaseFirewallOpensExactlyTheRightPorts(t *testing.T) {
	r := strings.NewReplacer("@SSH_PORT@", "2222", "@WG_PORT@", "51820", "@API_PORT@", "7443")
	out := r.Replace(baseNft)
	for _, want := range []string{
		"tcp dport 2222 accept",
		"udp dport 51820 accept",
		"tcp dport 7443 accept",
		"policy drop",
		"iif lo accept",
		"ct state established,related accept",
		NftMarker,
		`include "/etc/autoproxy/rules.nft"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("base ruleset is missing %q", want)
		}
	}
	if strings.Contains(out, "@SSH_PORT@") || strings.Contains(out, "@WG_PORT@") || strings.Contains(out, "@API_PORT@") {
		t.Error("a placeholder was left unsubstituted; the ruleset would not load")
	}
	// A forward chain here would split ownership of the forward hook with the
	// agent's table, and nftables requires both to accept.
	if strings.Contains(out, "hook forward") {
		t.Error("the base table must not own the forward hook: the agent's table does")
	}
	// "flush ruleset" would wipe the agent's table and close every game port.
	// Comment lines are skipped: the template explains why it does not do it.
	for _, line := range strings.Split(out, "\n") {
		if t2 := strings.TrimSpace(line); !strings.HasPrefix(t2, "#") && strings.Contains(t2, "flush ruleset") {
			t.Errorf("never flush ruleset: it takes the agent's table with it (%q)", line)
		}
	}
}

func TestUnitFileKeepsTheHardening(t *testing.T) {
	for _, want := range []string{
		"ExecStart=/usr/local/bin/autoproxy-agent run",
		"EnvironmentFile=/etc/autoproxy/agent.env",
		"CapabilityBoundingSet=CAP_NET_ADMIN",
		"AmbientCapabilities=CAP_NET_ADMIN",
		"NoNewPrivileges=yes",
		"ProtectSystem=strict",
		"ReadWritePaths=/etc/autoproxy /var/lib/autoproxy",
		"StartLimitIntervalSec=0",
		"Restart=always",
	} {
		if !strings.Contains(unitFile, want) {
			t.Errorf("unit file is missing %q", want)
		}
	}
}

// wg-quick treats a failing PostUp as fatal and deletes the interface it just
// created. Found the hard way in a container where the kernel refused the
// rp_filter sysctl: wg0 came up, the hook failed, wg-quick tore it down, and
// every peer operation afterwards failed with "No such device".
func TestWG0ConfHasNoHooksAndNoPeers(t *testing.T) {
	conf := WG0Conf("10.66.66.1", 24, 51820, "server-private-key")
	for _, hook := range []string{"PostUp", "PreUp", "PostDown", "PreDown"} {
		if strings.Contains(conf, hook) {
			t.Errorf("wg0.conf must not use %s: wg-quick deletes the interface when a hook fails", hook)
		}
	}
	if strings.Contains(conf, "[Peer]") {
		t.Error("wg0.conf must contain no peers: the agent manages them live")
	}
	for _, want := range []string{"Address = 10.66.66.1/24", "ListenPort = 51820", "PrivateKey = server-private-key"} {
		if !strings.Contains(conf, want) {
			t.Errorf("wg0.conf is missing %q:\n%s", want, conf)
		}
	}
}

// The fwmark has to survive a reboot, and on a reboot nothing but wg-quick
// reads wg0.conf. If it were only applied by the agent at start, there would
// be a window after every boot in which the interface is up and unmarked, and
// during that window the VPS's own handshakes to a peer whose lan_cidrs cover
// something the VPS must reach directly are routed into the tunnel.
func TestWG0ConfCarriesTheFwMark(t *testing.T) {
	conf := WG0Conf("10.66.66.1", 24, 51820, "server-private-key")
	want := "FwMark = " + wg.DefaultFWMark
	if !strings.Contains(conf, want) {
		t.Errorf("wg0.conf must contain %q so the mark survives a reboot:\n%s", want, conf)
	}
	// FwMark is a wg setconf key, not a wg-quick hook -- the test above
	// refuses hooks, and this must not be read as a licence to add one.
	if strings.Contains(conf, "PostUp") {
		t.Error("the fwmark must not be applied through a wg-quick hook")
	}
}

// Both Debian and Ubuntu ship an /etc/nftables.conf with the nftables package.
// If setup treated that as "somebody else's firewall", it would refuse on every
// fresh VPS and the one-command install would never work for anyone.
func TestDistroDefaultNftablesConfIsRecognised(t *testing.T) {
	shipped := []byte("#!/usr/sbin/nft -f\nflush ruleset\n")
	// The checksum dpkg would have recorded for that content.
	sum := "e5bf4df1d0b46b8e2e5c92e3e08c23ab"
	e := &env{opts: Options{}, out: io.Discard, run: func(string, ...string) (string, error) {
		return " " + NftConf + " " + sum, nil
	}}
	if e.isDistroDefault([]byte("something a human wrote\n")) {
		t.Fatal("an edited file must not be treated as the distribution default")
	}

	// Now feed back the real digest of the shipped bytes.
	real := &env{opts: Options{}, out: io.Discard, run: func(string, ...string) (string, error) {
		return " " + NftConf + " " + md5hex(shipped), nil
	}}
	if !real.isDistroDefault(shipped) {
		t.Fatal("the unmodified shipped file must be recognised, or setup refuses on every fresh VPS")
	}

	// dpkg missing, or nothing about this path: refuse rather than guess.
	for _, out := range []string{"", " /etc/other.conf abc", "garbage"} {
		e2 := &env{opts: Options{}, out: io.Discard, run: func(string, ...string) (string, error) { return out, nil }}
		if e2.isDistroDefault(shipped) {
			t.Errorf("unparseable dpkg output %q must not be read as 'safe to replace'", out)
		}
	}
	broken := &env{opts: Options{}, out: io.Discard, run: func(string, ...string) (string, error) {
		return "", errors.New("dpkg-query: not found")
	}}
	if broken.isDistroDefault(shipped) {
		t.Error("a missing dpkg must not be read as 'safe to replace'")
	}
}

func md5hex(b []byte) string { return fmt.Sprintf("%x", md5.Sum(b)) }

// ipRuleShow is "ip rule show" on a VPS where setup has run, byte for byte as
// iproute2 prints it: the rule is added as "not fwmark 0x2b lookup 201" but
// printed with an implied "from all" in the middle.
const ipRuleShow = "0:\tfrom all lookup local\n" +
	"90:\tnot from all fwmark 0x2b lookup 201\n" +
	"32766:\tfrom all lookup main\n" +
	"32767:\tfrom all lookup default"

// ruleRunner answers "ip rule show" with the rule present from the given look
// onwards (0 = never), and everything else with an empty success.
func ruleRunner(presentFrom int, looks *int) Runner {
	return func(name string, args ...string) (string, error) {
		if name == "ip" && strings.Join(args, " ") == "rule show" {
			*looks++
			if presentFrom > 0 && *looks >= presentFrom {
				return ipRuleShow, nil
			}
			return "0:\tfrom all lookup local\n32766:\tfrom all lookup main", nil
		}
		return "", nil
	}
}

// Seen on a real install: the verification block said "MISSING peer-routing
// ip rule" while "ip rule show" listed it. The summary matched the rule as it
// is typed, not as it is printed. It must use the same matcher as the code that
// installs the rule.
func TestVerificationFindsTheRuleAsIPRuleShowPrintsIt(t *testing.T) {
	var looks, sleeps int
	var out strings.Builder
	e := &env{out: &out, run: ruleRunner(1, &looks), sleep: func(time.Duration) { sleeps++ }}
	e.verificationSummary(7443)
	if !strings.Contains(out.String(), "OK      peer-routing ip rule is installed") {
		t.Fatalf("the rule is installed but the summary did not say so:\n%s", out.String())
	}
	if strings.Contains(out.String(), "MISSING peer-routing") {
		t.Fatalf("false alarm for an installed rule:\n%s", out.String())
	}
	if sleeps != 0 {
		t.Fatalf("waited %d times for a rule that was already there", sleeps)
	}
}

// A rule that shows up a moment later (the agent re-ensures it when it starts)
// is not a problem, so the summary looks again before it warns.
func TestVerificationWaitsBrieflyForTheRule(t *testing.T) {
	var looks, sleeps int
	e := &env{out: io.Discard, run: ruleRunner(3, &looks), sleep: func(time.Duration) { sleeps++ }}
	if !e.routingRuleInstalled() {
		t.Fatal("a rule that appears on the third look was reported missing")
	}
	if looks != 3 || sleeps != 2 {
		t.Fatalf("looks=%d sleeps=%d, want 3 and 2", looks, sleeps)
	}
}

// A rule that never appears is still reported, after a bounded wait.
func TestVerificationStillWarnsWhenTheRuleNeverAppears(t *testing.T) {
	var looks, sleeps int
	var out strings.Builder
	e := &env{out: &out, run: ruleRunner(0, &looks), sleep: func(time.Duration) { sleeps++ }}
	e.verificationSummary(7443)
	if !strings.Contains(out.String(), "MISSING peer-routing ip rule") {
		t.Fatalf("a missing rule was not reported:\n%s", out.String())
	}
	if looks != ruleCheckAttempts || sleeps != ruleCheckAttempts-1 {
		t.Fatalf("looks=%d sleeps=%d, want %d and %d", looks, sleeps, ruleCheckAttempts, ruleCheckAttempts-1)
	}
}
