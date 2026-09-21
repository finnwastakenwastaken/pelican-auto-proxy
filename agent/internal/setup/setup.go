package setup

import (
	"crypto/md5" //nolint:gosec // dpkg records conffile checksums as md5; this is a change check, not a security boundary
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/state"
	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/tlsutil"
	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/wg"
)

// Runner executes a command and returns its combined output. Injectable so
// tests can watch what setup would do without a privileged host.
type Runner func(name string, args ...string) (string, error)

// ExecRunner is the default Runner.
func ExecRunner(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Options are the setup flags.
type Options struct {
	DryRun        bool
	Yes           bool
	APIPort       int
	WGPort        int
	WGSubnet      string
	PublicIP      string // "auto" or a literal address
	DNSName       string // optional extra SAN, for operators who have a hostname
	Version       string
	OSReleasePath string
	Out           io.Writer
	Run           Runner
}

// VPSCodeVersion is the version field of the VPS code.
const VPSCodeVersion = 1

// VPSCode is everything the Pelican plugin needs to reach and trust this
// agent. It is printed once and stored at /etc/autoproxy/vps-code (0600).
type VPSCode struct {
	V             int    `json:"v"`
	EndpointIP    string `json:"endpoint_ip"`
	APIPort       int    `json:"api_port"`
	APICAPEM      string `json:"api_ca_pem"` // base64 of the PEM
	APISPKISHA256 string `json:"api_spki_sha256"`
	Token         string `json:"token"`
	WGPubKey      string `json:"wg_pubkey"`
	WGPort        int    `json:"wg_port"`
	TunnelSubnet  string `json:"tunnel_subnet"`
	VPSTunnelIP   string `json:"vps_tunnel_ip"`
	Version       string `json:"version"`
}

// Encode renders the VPS code as one base64url token.
func (c VPSCode) Encode() (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// DecodeVPSCode is the inverse of Encode.
func DecodeVPSCode(s string) (VPSCode, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return VPSCode{}, fmt.Errorf("VPS code is not valid base64url: %w", err)
	}
	var c VPSCode
	if err := json.Unmarshal(b, &c); err != nil {
		return VPSCode{}, fmt.Errorf("VPS code is not valid JSON: %w", err)
	}
	if c.V != VPSCodeVersion {
		return VPSCode{}, fmt.Errorf("VPS code version %d is not supported (expected %d)", c.V, VPSCodeVersion)
	}
	return c, nil
}

type env struct {
	opts Options
	out  io.Writer
	run  Runner
}

func (e *env) log(format string, a ...any)  { fmt.Fprintf(e.out, ">> "+format+"\n", a...) }
func (e *env) warn(format string, a ...any) { fmt.Fprintf(e.out, "WARNING: "+format+"\n", a...) }
func (e *env) plan(format string, a ...any) { fmt.Fprintf(e.out, "   [dry-run] "+format+"\n", a...) }

// exec runs a command, or describes it under --dry-run.
func (e *env) exec(name string, args ...string) (string, error) {
	if e.opts.DryRun {
		e.plan("run: %s %s", name, strings.Join(args, " "))
		return "", nil
	}
	return e.run(name, args...)
}

// mustExec fails setup when the command fails.
func (e *env) mustExec(name string, args ...string) error {
	out, err := e.exec(name, args...)
	if err != nil {
		if out != "" {
			return fmt.Errorf("%s %s failed: %w: %s", name, strings.Join(args, " "), err, out)
		}
		return fmt.Errorf("%s %s failed: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// tryExec runs a command and only warns when it fails. For steps that are not
// worth aborting a half-finished install over.
func (e *env) tryExec(name string, args ...string) {
	if out, err := e.exec(name, args...); err != nil {
		e.warn("%s %s: %v %s", name, strings.Join(args, " "), err, out)
	}
}

func (e *env) ensureDir(path string, mode os.FileMode) error {
	if e.opts.DryRun {
		if _, err := os.Stat(path); err != nil {
			e.plan("CREATE  %s/ (mode %#o)", path, mode)
		}
		return nil
	}
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// writeFile writes content unless it is already exactly that. A re-run of
// setup stays quiet and genuinely idempotent.
func (e *env) writeFile(path string, mode os.FileMode, content string) error {
	if cur, err := os.ReadFile(path); err == nil && string(cur) == content {
		if fi, err := os.Stat(path); err == nil && fi.Mode().Perm() == mode {
			e.log("unchanged: %s", path)
			return nil
		}
	}
	if e.opts.DryRun {
		if _, err := os.Stat(path); err == nil {
			e.plan("REWRITE %s (mode %#o)", path, mode)
		} else {
			e.plan("CREATE  %s (mode %#o)", path, mode)
		}
		return nil
	}
	if err := state.WriteFileAtomic(path, []byte(content), mode); err != nil {
		return err
	}
	e.log("wrote %s (mode %#o)", path, mode)
	return nil
}

func defaulted(o Options) Options {
	if o.APIPort == 0 {
		o.APIPort = 7443
	}
	if o.WGPort == 0 {
		o.WGPort = 51820
	}
	if o.WGSubnet == "" {
		o.WGSubnet = "10.66.66.0/24"
	}
	if o.PublicIP == "" {
		o.PublicIP = "auto"
	}
	if o.OSReleasePath == "" {
		o.OSReleasePath = "/etc/os-release"
	}
	if o.Out == nil {
		o.Out = os.Stdout
	}
	if o.Run == nil {
		o.Run = ExecRunner
	}
	return o
}

// Run provisions the VPS. It is idempotent: re-running never regenerates the
// WireGuard key, the bearer token or the certificate, but it does refresh
// everything derived from them, so a new sshd port or a new interface takes
// effect.
func Run(opts Options) error {
	opts = defaulted(opts)
	e := &env{opts: opts, out: opts.Out, run: opts.Run}

	if opts.DryRun {
		fmt.Fprintln(e.out, "================= DRY RUN: nothing on this box will change =================")
	}

	// --- 1. Preconditions -------------------------------------------------
	if os.Geteuid() != 0 {
		return fmt.Errorf("autoproxy-agent setup must be run as root")
	}
	osInfo, err := DetectOS(opts.OSReleasePath)
	if err != nil {
		return err
	}
	if !osInfo.Supported() {
		return fmt.Errorf("%s", osInfo.UnsupportedMessage())
	}
	e.log("Platform: %s (supported)", strings.TrimSpace(firstNonEmpty(osInfo.Pretty, osInfo.ID+" "+osInfo.VersionID)))

	subnet, err := netip.ParsePrefix(opts.WGSubnet)
	if err != nil || !subnet.Addr().Is4() {
		return fmt.Errorf("--wg-subnet %q is not a valid IPv4 CIDR", opts.WGSubnet)
	}
	if subnet.Masked() != subnet {
		return fmt.Errorf("--wg-subnet %q has host bits set; write it as %s", opts.WGSubnet, subnet.Masked())
	}
	if bits := subnet.Bits(); bits > 24 {
		return fmt.Errorf("--wg-subnet %s is too small: a /24 or larger is needed for the peer pool", subnet)
	}
	vpsTunnelIP := subnet.Masked().Addr().Next() // .1

	if opts.APIPort < 1 || opts.APIPort > 65535 {
		return fmt.Errorf("--api-port %d is out of range", opts.APIPort)
	}
	if opts.WGPort < 1 || opts.WGPort > 65535 {
		return fmt.Errorf("--wg-port %d is out of range", opts.WGPort)
	}
	if opts.APIPort == opts.WGPort {
		return fmt.Errorf("--api-port and --wg-port cannot both be %d", opts.APIPort)
	}

	netInfo, err := DetectNet(e.run)
	if err != nil {
		return err
	}
	publicIP := opts.PublicIP
	if publicIP == "auto" || publicIP == "" {
		publicIP = netInfo.PublicIP
	}
	if net.ParseIP(publicIP) == nil {
		return fmt.Errorf("could not determine this VPS's public IPv4 address; pass --public-ip")
	}
	e.log("Public interface: %s, public address: %s", netInfo.Iface, publicIP)

	sshPort, sshDetected := DetectSSHPort()
	if !sshDetected {
		e.warn("could not ask sshd which port it listens on; assuming %d.", sshPort)
		e.warn("If sshd listens elsewhere, the base firewall would lock you out. Abort now and")
		e.warn("set AUTOPROXY_SSH_PORT if that is the case.")
	}
	if v := os.Getenv("AUTOPROXY_SSH_PORT"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &sshPort); err != nil {
			return fmt.Errorf("AUTOPROXY_SSH_PORT=%q is not a port number", v)
		}
	}
	e.log("sshd port: %d (allowed by the base firewall, and reserved from forwarding)", sshPort)

	// --- 2. Packages ------------------------------------------------------
	e.log("Installing wireguard-tools and nftables")
	os.Setenv("DEBIAN_FRONTEND", "noninteractive")
	if err := e.mustExec("apt-get", "update"); err != nil {
		return err
	}
	if err := e.mustExec("apt-get", "install", "-y", "wireguard-tools", "nftables"); err != nil {
		return err
	}

	// --- 3. Forwarding ----------------------------------------------------
	e.log("Enabling net.ipv4.ip_forward persistently")
	if err := e.writeFile(SysctlConf, 0o644, sysctlFile); err != nil {
		return err
	}
	// Not fatal if the kernel refuses (a container, a locked-down provider
	// kernel): aborting would leave the box half-provisioned. The summary
	// re-reads the live value, so a box that cannot forward says so.
	if _, err := e.exec("sysctl", "-q", "-w", "net.ipv4.ip_forward=1"); err != nil {
		e.warn("could not set net.ipv4.ip_forward live; nothing will be forwarded until it is 1.")
	}

	// --- 4. Directories ---------------------------------------------------
	for _, d := range []struct {
		path string
		mode os.FileMode
	}{{EtcDir, 0o750}, {TLSDir, 0o750}, {StateDir, 0o750}, {WGDir, 0o700}} {
		if err := e.ensureDir(d.path, d.mode); err != nil {
			return err
		}
	}

	// --- 5. WireGuard identity and wg0.conf -------------------------------
	wgPub, err := e.ensureServerKey()
	if err != nil {
		return err
	}
	// wg0.conf carries NO peers. Peers are dynamic: the agent adds them with
	// "wg set" and re-applies them from peers.json on start. Two writers on
	// this file is how peers silently disappear after a reboot.
	wg0 := WG0Conf(vpsTunnelIP.String(), subnet.Bits(), opts.WGPort, e.readOrPlaceholder(WGKey))
	if err := e.writeFile(WGConf, 0o600, wg0); err != nil {
		return err
	}

	// --- 6. rules.nft placeholder ----------------------------------------
	if _, err := os.Stat(RulesPath); err == nil {
		e.log("%s already present, leaving it to the agent", RulesPath)
	} else if err := e.writeFile(RulesPath, 0o640, emptyRules); err != nil {
		return err
	}

	// --- 7. Orphaned Docker tables ---------------------------------------
	e.removeOrphanDockerTables()

	// --- 8. Base firewall -------------------------------------------------
	if err := e.installBaseFirewall(sshPort, opts.WGPort, opts.APIPort); err != nil {
		return err
	}

	// --- 9. Token, TLS, agent.env ----------------------------------------
	token, err := e.ensureToken()
	if err != nil {
		return err
	}
	certPEM, err := e.ensureCert(publicIP, opts.DNSName)
	if err != nil {
		return err
	}

	agentEnv := fmt.Sprintf(`# autoproxy-managed: rewritten by "autoproxy-agent setup".
# Mode 0600 -- this holds the bearer token.
AUTOPROXY_TOKEN=%s
AUTOPROXY_LISTEN=0.0.0.0:%d
AUTOPROXY_PUBLIC_IFACE=%s
AUTOPROXY_RESERVED_PORTS=%d,%d,%d
AUTOPROXY_STATE_DIR=%s
AUTOPROXY_RULES_FILE=%s
AUTOPROXY_ENV_FILE=%s
AUTOPROXY_TLS_CERT=%s
AUTOPROXY_TLS_KEY=%s
AUTOPROXY_NFT_BIN=/usr/sbin/nft
AUTOPROXY_WG_IFACE=%s
AUTOPROXY_WG_BIN=/usr/bin/wg
AUTOPROXY_IP_BIN=/usr/sbin/ip
AUTOPROXY_WG_SUBNET=%s
AUTOPROXY_WG_PORT=%d
AUTOPROXY_PUBLIC_IP=%s
`, token, opts.APIPort, netInfo.Iface, sshPort, opts.WGPort, opts.APIPort,
		StateDir, RulesPath, EnvPath, CertPath, KeyPath, WGIface, subnet, opts.WGPort, publicIP)
	if err := e.writeFile(EnvPath, 0o600, agentEnv); err != nil {
		return err
	}

	// --- 10. Binary and unit ---------------------------------------------
	if err := e.installSelf(); err != nil {
		return err
	}
	if err := e.writeFile(UnitPath, 0o644, unitFile); err != nil {
		return err
	}
	if err := e.mustExec("systemctl", "daemon-reload"); err != nil {
		return err
	}

	// --- 11. Bring everything up -----------------------------------------
	e.tryExec("systemctl", "enable", "nftables")
	e.tryExec("systemctl", "enable", "wg-quick@wg0")
	e.tryExec("systemctl", "restart", "wg-quick@wg0")

	// The peer-routing rule and the interface mark, immediately rather than
	// only when the agent next starts. wg0.conf already carries FwMark so a
	// reboot is covered, and the agent re-ensures both on every start so an
	// upgraded install is covered; this call is what covers the window
	// between "setup finished" and "the agent got there", and it is also the
	// only one of the three that runs while the operator is still watching
	// the output, so a kernel that refuses ip rules says so here.
	//
	// EnsureFWMark is not strictly needed after a wg-quick restart that just
	// read the new FwMark line, but it costs one declarative call and it
	// repairs the upgrade case where wg-quick was NOT restarted because the
	// interface was already up with the old config.
	wgm := wg.Manager{WGBin: "wg", IPBin: "ip", Iface: WGIface, Run: e.exec}
	if err := wgm.EnsureFWMark(); err != nil {
		e.warn("could not set the wg0 fwmark: %v", err)
	}
	if err := wgm.EnsureRoutingRule(); err != nil {
		e.warn("could not add the peer-routing ip rule (priority %s, table %s): %v", wg.DefaultRulePriority, wg.DefaultTable, err)
		e.warn("without it, a site client whose LAN range covers its own address will never complete a handshake.")
	}

	e.tryExec("systemctl", "enable", "autoproxy-agent")
	e.tryExec("systemctl", "restart", "autoproxy-agent")

	// --- 12. The VPS code -------------------------------------------------
	spki := "<generated on the real run>"
	pemB64 := spki
	if len(certPEM) > 0 {
		spki, err = tlsutil.SPKISHA256(certPEM)
		if err != nil {
			return fmt.Errorf("compute the certificate fingerprint: %w", err)
		}
		pemB64 = base64.StdEncoding.EncodeToString(certPEM)
	}
	code := VPSCode{
		V: VPSCodeVersion, EndpointIP: publicIP, APIPort: opts.APIPort,
		APICAPEM: pemB64, APISPKISHA256: spki, Token: token,
		WGPubKey: wgPub, WGPort: opts.WGPort,
		TunnelSubnet: subnet.String(), VPSTunnelIP: vpsTunnelIP.String(),
		Version: opts.Version,
	}
	encoded, err := code.Encode()
	if err != nil {
		return fmt.Errorf("encode the VPS code: %w", err)
	}

	if opts.DryRun {
		fmt.Fprintln(e.out, "\n================= DRY RUN complete: nothing was changed =================")
		fmt.Fprintln(e.out, "Re-run without --dry-run to apply. Secrets are only shown on a real run.")
		return nil
	}

	if err := e.writeFile(CodePath, 0o600, encoded+"\n"); err != nil {
		return err
	}
	e.verificationSummary(opts.APIPort)
	PrintCode(e.out, encoded, publicIP, opts.APIPort)
	return nil
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// PrintCode shows the VPS code with the warning that goes with a secret
// printed to a terminal.
func PrintCode(out io.Writer, code, publicIP string, apiPort int) {
	fmt.Fprintln(out, "\n==================== Your VPS code ====================")
	fmt.Fprintln(out, "This code contains the API bearer token. Anyone who has it can change")
	fmt.Fprintln(out, "the forwarding rules on this VPS. Paste it into the Pelican plugin's")
	fmt.Fprintf(out, "Setup page and then clear your terminal. It is also saved at %s (mode 0600).\n", CodePath)
	fmt.Fprintln(out, "Lost it? Run: sudo autoproxy-agent show-code")
	fmt.Fprintln(out)
	fmt.Fprintln(out, code)
	fmt.Fprintln(out)
	fmt.Fprintf(out, "The panel will connect to https://%s:%d/v1/status\n", publicIP, apiPort)
	fmt.Fprintln(out, "=======================================================")
}

// ShowCode re-prints the saved code. Root only, because the file is 0600 and
// holds the token.
func ShowCode(out io.Writer) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("autoproxy-agent show-code must be run as root: the code contains the API token")
	}
	b, err := os.ReadFile(CodePath)
	if err != nil {
		return fmt.Errorf("no saved VPS code at %s: run 'autoproxy-agent setup' first (%w)", CodePath, err)
	}
	encoded := strings.TrimSpace(string(b))
	code, err := DecodeVPSCode(encoded)
	if err != nil {
		return fmt.Errorf("the saved VPS code is unreadable: %w", err)
	}
	PrintCode(out, encoded, code.EndpointIP, code.APIPort)
	return nil
}

func (e *env) readOrPlaceholder(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "<generated on the real run>"
	}
	return strings.TrimSpace(string(b))
}

// ensureServerKey generates the VPS's WireGuard identity once and never again.
// Regenerating it silently breaks every client that already joined.
func (e *env) ensureServerKey() (string, error) {
	if b, err := os.ReadFile(WGPub); err == nil {
		e.log("WireGuard server keypair already present, keeping it")
		return strings.TrimSpace(string(b)), nil
	}
	if e.opts.DryRun {
		e.plan("GENERATE the WireGuard server keypair -> %s and %s", WGKey, WGPub)
		return "<generated on the real run>", nil
	}
	priv, err := e.run("wg", "genkey")
	if err != nil {
		return "", fmt.Errorf("wg genkey: %w", err)
	}
	priv = strings.TrimSpace(priv)
	cmd := exec.Command("wg", "pubkey")
	cmd.Stdin = strings.NewReader(priv + "\n")
	pubOut, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("wg pubkey: %w", err)
	}
	pub := strings.TrimSpace(string(pubOut))
	if err := state.WriteFileAtomic(WGKey, []byte(priv+"\n"), 0o600); err != nil {
		return "", err
	}
	if err := state.WriteFileAtomic(WGPub, []byte(pub+"\n"), 0o644); err != nil {
		return "", err
	}
	e.log("generated the WireGuard server keypair")
	return pub, nil
}

// ensureToken keeps an existing bearer token so a re-run does not silently
// invalidate the one already pasted into the panel.
func (e *env) ensureToken() (string, error) {
	if b, err := os.ReadFile(EnvPath); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "AUTOPROXY_TOKEN="); ok && v != "" {
				e.log("Keeping the existing agent token, refreshing the rest")
				return v, nil
			}
		}
	}
	if e.opts.DryRun {
		e.plan("GENERATE a 32-byte bearer token")
		return "<generated on the real run>", nil
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	e.log("Generated a new agent token")
	return hex.EncodeToString(raw[:]), nil
}

// ensureCert generates the API certificate once. Regenerating it would break
// the plugin's pinned trust until the admin pastes a new VPS code.
func (e *env) ensureCert(publicIP, dnsName string) ([]byte, error) {
	if b, err := os.ReadFile(CertPath); err == nil {
		if _, err := tlsutil.SPKISHA256(b); err == nil {
			e.log("API certificate already present, keeping it")
			return b, nil
		}
		e.warn("%s is not a usable certificate; generating a new one", CertPath)
	}
	if e.opts.DryRun {
		e.plan("GENERATE a self-signed ECDSA P-256 certificate for %s -> %s, %s", publicIP, CertPath, KeyPath)
		return nil, nil
	}
	var dns []string
	if strings.TrimSpace(dnsName) != "" {
		dns = []string{strings.TrimSpace(dnsName)}
	}
	m, err := tlsutil.Generate([]net.IP{net.ParseIP(publicIP)}, dns, time.Now())
	if err != nil {
		return nil, err
	}
	if err := m.Write(CertPath, KeyPath); err != nil {
		return nil, err
	}
	e.log("generated the API certificate for %s (valid 10 years)", publicIP)
	return m.CertPEM, nil
}

// installSelf copies the running binary to /usr/local/bin when it is not
// already there, so the systemd unit's ExecStart always resolves.
func (e *env) installSelf() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not locate the running binary: %w", err)
	}
	if self == BinPath {
		return nil
	}
	if e.opts.DryRun {
		e.plan("INSTALL %s -> %s (mode 0755)", self, BinPath)
		return nil
	}
	b, err := os.ReadFile(self)
	if err != nil {
		return fmt.Errorf("read %s: %w", self, err)
	}
	if err := state.WriteFileAtomic(BinPath, b, 0o755); err != nil {
		return fmt.Errorf("install %s: %w", BinPath, err)
	}
	e.log("installed %s", BinPath)
	return nil
}

// removeOrphanDockerTables deletes "ip nat" and "ip filter" left behind by a
// removed Docker, by EXACT name and never with "flush ruleset".
//
// An orphaned Docker filter table's FORWARD chain has policy drop. nftables
// requires every base chain at a hook to accept, so that orphan silently
// discards every packet we forward while our own table still looks perfectly
// healthy: a failure that looks like a normal empty state.
func (e *env) removeOrphanDockerTables() {
	e.log("Checking for orphaned Docker nftables tables")
	if HasDocker() {
		e.warn("docker is installed. Leaving 'ip nat' and 'ip filter' alone.")
		e.warn("Docker's FORWARD chain has policy drop and WILL block forwarding.")
		e.warn("Add accepts for the tunnel to DOCKER-USER, or run the panel's node elsewhere.")
		return
	}
	for _, t := range []string{"nat", "filter"} {
		if _, err := e.run("nft", "list", "table", "ip", t); err != nil {
			e.log("no orphaned 'ip %s' table (nothing to do)", t)
			continue
		}
		if e.opts.DryRun {
			e.plan("DELETE  nft table 'ip %s' (orphaned Docker leftover)", t)
			continue
		}
		if out, err := e.run("nft", "delete", "table", "ip", t); err != nil {
			e.warn("could not delete orphaned table 'ip %s': %v %s", t, err, out)
		} else {
			e.log("deleted orphaned table 'ip %s'", t)
		}
	}
}

// installBaseFirewall renders, checks and installs /etc/nftables.conf.
func (e *env) installBaseFirewall(sshPort, wgPort, apiPort int) error {
	e.log("Preparing %s (base ruleset; ssh %d, wireguard %d, api %d)", NftConf, sshPort, wgPort, apiPort)
	r := strings.NewReplacer(
		"@SSH_PORT@", fmt.Sprint(sshPort),
		"@WG_PORT@", fmt.Sprint(wgPort),
		"@API_PORT@", fmt.Sprint(apiPort),
	)
	text := r.Replace(baseNft)
	if !strings.Contains(text, NftMarker) {
		return fmt.Errorf("the base ruleset template lost its %q marker comment", NftMarker)
	}

	// Syntax-check before going near the live ruleset. The template ends in an
	// include, and nft fails the whole file when the target is missing, so on
	// a dry run against a fresh box we check against a temporary stand-in.
	work, err := os.MkdirTemp("", "autoproxy-nft-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	check := text
	if _, err := os.Stat(RulesPath); err != nil {
		standIn := work + "/rules.nft"
		if err := os.WriteFile(standIn, []byte(emptyRules), 0o600); err != nil {
			return err
		}
		check = strings.Replace(text, `include "`+RulesPath+`"`, `include "`+standIn+`"`, 1)
		e.log("%s does not exist yet; checking against a temporary stand-in", RulesPath)
	}
	checkPath := work + "/nftables.conf"
	if err := os.WriteFile(checkPath, []byte(check), 0o600); err != nil {
		return err
	}
	if out, err := e.run("nft", "-c", "-f", checkPath); err != nil {
		return fmt.Errorf("nft -c rejected the generated base ruleset; nothing was changed: %s", out)
	}
	e.log("nft -c accepted the generated base ruleset")

	if b, err := os.ReadFile(NftConf); err == nil && !strings.Contains(string(b), NftMarker) {
		// Debian and Ubuntu both ship an /etc/nftables.conf in the nftables
		// package: a skeleton with "flush ruleset" and three empty chains.
		// Treating that as "somebody else's firewall" would make setup refuse
		// on every fresh VPS -- which is every VPS this is installed on, so
		// the one-command install would never work. dpkg records the checksum
		// of the file it shipped, so an untouched default can be told apart
		// from a firewall a human actually wrote.
		distroDefault := e.isDistroDefault(b)
		if !distroDefault && !e.opts.Yes {
			e.warn("%s exists and does not carry the %q marker.", NftConf, NftMarker)
			e.warn("Refusing to overwrite somebody else's firewall. Re-run with --yes to replace it.")
			e.warn("(The generated ruleset passed nft -c; only the write is being skipped.)")
			e.warn("The agent's own table is unaffected, but inbound traffic will not reach it")
			e.warn("unless the existing firewall already allows port %d and the game ports.", e.opts.APIPort)
			return nil
		}
		if distroDefault {
			e.log("%s is the distribution's unmodified default; replacing it", NftConf)
		} else {
			e.warn("%s is not ours; overwriting because --yes was given.", NftConf)
		}
		e.warn("Keeping a copy at %s", NftBackup)
		if !e.opts.DryRun {
			if _, err := os.Stat(NftBackup); err != nil {
				if err := state.WriteFileAtomic(NftBackup, b, 0o644); err != nil {
					return fmt.Errorf("could not keep a copy of the previous firewall: %w", err)
				}
			}
		} else {
			e.plan("COPY    %s -> %s", NftConf, NftBackup)
		}
	}

	if err := e.writeFile(NftConf, 0o644, text); err != nil {
		return err
	}
	// "systemctl start" is a no-op when the unit already runs, which would
	// leave a re-run's changes unapplied. Load the file explicitly too.
	e.tryExec("systemctl", "enable", "nftables")
	e.tryExec("systemctl", "start", "nftables")
	if err := e.mustExec("nft", "-f", NftConf); err != nil {
		return err
	}
	return nil
}

// isDistroDefault reports whether /etc/nftables.conf is byte for byte the one
// the nftables package shipped. dpkg stores the checksum of every conffile it
// installed, so this is the package manager's own answer to "has a human
// edited this?" rather than a guess of ours.
//
// md5 because that is the digest dpkg records; this is a "did it change"
// comparison against a local database, not a security boundary (anyone who can
// write this file is already root).
//
// Any doubt -- dpkg missing, output we cannot parse -- returns false and falls
// back to refusing without --yes. The worst case is then an operator who has to
// pass a flag, not an operator whose firewall we silently replaced.
func (e *env) isDistroDefault(current []byte) bool {
	out, err := e.run("dpkg-query", "-W", "-f=${Conffiles}\n", "nftables")
	if err != nil {
		return false
	}
	sum := fmt.Sprintf("%x", md5.Sum(current))
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == NftConf {
			return f[1] == sum
		}
	}
	return false
}

func (e *env) verificationSummary(apiPort int) {
	fmt.Fprintln(e.out, "\n==================== Verification ====================")
	if b, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward"); err == nil && strings.TrimSpace(string(b)) == "1" {
		fmt.Fprintln(e.out, "  OK      net.ipv4.ip_forward = 1")
	} else {
		e.warn("  ip_forward is not 1: nothing will be forwarded, however healthy the rest looks.")
	}
	ruleset, _ := e.run("nft", "list", "ruleset")
	for _, want := range []string{"autoproxy_base", "autoproxy_rules"} {
		if strings.Contains(ruleset, "table inet "+want) {
			fmt.Fprintf(e.out, "  OK      table inet %s is loaded\n", want)
		} else {
			e.warn("  MISSING table inet %s is NOT loaded", want)
		}
	}
	if out, err := e.run("wg", "show", WGIface); err == nil {
		fmt.Fprintf(e.out, "  OK      %s is up (no peers yet; the plugin adds them)\n", WGIface)
		_ = out
	} else {
		e.warn("  %s is not up: clients cannot connect until it is", WGIface)
	}
	// autoproxy-agent installs the fwmark and the peer-routing ip rule itself
	// on every start (the same "state is truth, reapply on boot" pattern
	// peers already use), right before it re-applies any stored peer. By the
	// time this summary runs, "systemctl restart autoproxy-agent" above has
	// already had a moment to do that -- see docs/dev/decisions.md for why a
	// missing rule here means a site peer whose lan_cidrs cover its own
	// endpoint would never complete a handshake.
	if out, _ := e.run("ip", "rule", "show"); strings.Contains(out, fmt.Sprintf("not fwmark %s lookup %s", wg.DefaultFWMark, wg.DefaultTable)) {
		fmt.Fprintf(e.out, "  OK      peer-routing ip rule is installed (table %s)\n", wg.DefaultTable)
	} else {
		e.warn("  MISSING peer-routing ip rule: check 'journalctl -u autoproxy-agent' -- a site peer whose")
		e.warn("          lan_cidrs cover its own endpoint would never complete a handshake without it")
	}
	if out, _ := e.run("systemctl", "is-active", "autoproxy-agent"); strings.TrimSpace(out) == "active" {
		fmt.Fprintf(e.out, "  OK      autoproxy-agent is running and listening on :%d\n", apiPort)
	} else {
		e.warn("  autoproxy-agent is not active; see 'journalctl -u autoproxy-agent -n 50'")
	}
}
