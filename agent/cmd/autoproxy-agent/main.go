// Command autoproxy-agent owns the VPS side of Pelican Auto Proxy.
//
// Subcommands:
//
//	setup        provision this VPS (packages, WireGuard, firewall, unit, code)
//	show-code    re-print the VPS code saved at setup
//	uninstall    remove everything setup created
//	run          the daemon (default when no subcommand is given)
//	-version     print the version and exit
//
// As a daemon it takes a full desired rule set over HTTPS, validates it,
// renders one nftables transaction and applies it, and manages the WireGuard
// peers that the rules point at.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/api"
	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/nft"
	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/peers"
	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/setup"
	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/state"
	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/wg"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// parsePorts turns "22,51820,7443" into a port list. Bad entries are reported
// rather than silently dropped: a reserved port that quietly disappears is how
// you lose SSH.
func parsePorts(s string) ([]int, error) {
	var out []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("invalid reserved port %q", part)
		}
		out = append(out, n)
	}
	return out, nil
}

func usage() {
	fmt.Fprint(os.Stderr, `autoproxy-agent -- the VPS side of Pelican Auto Proxy

  autoproxy-agent setup [flags]   provision this VPS and print the VPS code
  autoproxy-agent show-code       re-print the saved VPS code (root only)
  autoproxy-agent uninstall [-y]  remove everything setup created
  autoproxy-agent run             run the daemon (the default)
  autoproxy-agent -version        print the version

Setup flags:
  --dry-run            print every change that would be made and touch nothing
  --yes, -y            allow overwriting an /etc/nftables.conf that is not ours
  --api-port N         HTTPS control API port (default 7443)
  --wg-port N          WireGuard listen port (default 51820)
  --wg-subnet CIDR     tunnel subnet (default 10.66.66.0/24)
  --public-ip ADDR     this VPS's public IPv4 (default: auto-detect)
  --dns-name NAME      extra DNS name in the API certificate (optional)

Docs: https://github.com/finnwastakenwastaken/pelican-auto-proxy
`)
}

func main() {
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}

	switch cmd {
	case "setup":
		if err := runSetup(args); err != nil {
			fmt.Fprintln(os.Stderr, "ERROR: "+err.Error())
			os.Exit(1)
		}
	case "show-code":
		if err := setup.ShowCode(os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "ERROR: "+err.Error())
			os.Exit(1)
		}
	case "uninstall":
		if err := runUninstall(args); err != nil {
			fmt.Fprintln(os.Stderr, "ERROR: "+err.Error())
			os.Exit(1)
		}
	case "help", "-h", "--help":
		usage()
	case "", "run":
		runDaemon(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func runSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "print every change that would be made and touch nothing")
	yes := fs.Bool("yes", false, "allow overwriting an /etc/nftables.conf that is not ours")
	fs.BoolVar(yes, "y", false, "alias for --yes")
	apiPort := fs.Int("api-port", 7443, "HTTPS control API port")
	wgPort := fs.Int("wg-port", 51820, "WireGuard listen port")
	wgSubnet := fs.String("wg-subnet", "10.66.66.0/24", "tunnel subnet")
	publicIP := fs.String("public-ip", "auto", "this VPS's public IPv4 address")
	dnsName := fs.String("dns-name", "", "extra DNS name for the API certificate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return setup.Run(setup.Options{
		DryRun: *dryRun, Yes: *yes, APIPort: *apiPort, WGPort: *wgPort,
		WGSubnet: *wgSubnet, PublicIP: *publicIP, DNSName: *dnsName, Version: version,
	})
}

func runUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	fs.BoolVar(yes, "y", false, "alias for --yes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return setup.Uninstall(setup.UninstallOptions{Yes: *yes})
}

func runDaemon(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	listen := fs.String("listen", envOr("AUTOPROXY_LISTEN", "0.0.0.0:7443"), "address to listen on (AUTOPROXY_LISTEN)")
	publicIface := fs.String("public-iface", envOr("AUTOPROXY_PUBLIC_IFACE", "eth0"), "public network interface (AUTOPROXY_PUBLIC_IFACE)")
	reservedStr := fs.String("reserved-ports", envOr("AUTOPROXY_RESERVED_PORTS", "22,51820,7443"), "comma separated public ports that may never be forwarded (AUTOPROXY_RESERVED_PORTS)")
	stateDir := fs.String("state-dir", envOr("AUTOPROXY_STATE_DIR", "/var/lib/autoproxy"), "directory for rules.json and peers.json (AUTOPROXY_STATE_DIR)")
	rulesFile := fs.String("rules-file", envOr("AUTOPROXY_RULES_FILE", "/etc/autoproxy/rules.nft"), "nftables file owned by the agent (AUTOPROXY_RULES_FILE)")
	envFile := fs.String("env-file", envOr("AUTOPROXY_ENV_FILE", "/etc/autoproxy/agent.env"), "environment file rewritten by token rotate (AUTOPROXY_ENV_FILE)")
	certFile := fs.String("tls-cert", envOr("AUTOPROXY_TLS_CERT", "/etc/autoproxy/tls/agent.crt"), "TLS certificate (AUTOPROXY_TLS_CERT)")
	keyFile := fs.String("tls-key", envOr("AUTOPROXY_TLS_KEY", "/etc/autoproxy/tls/agent.key"), "TLS private key (AUTOPROXY_TLS_KEY)")
	nftBin := fs.String("nft-bin", envOr("AUTOPROXY_NFT_BIN", "nft"), "path to the nft binary (AUTOPROXY_NFT_BIN)")
	wgIface := fs.String("wg-iface", envOr("AUTOPROXY_WG_IFACE", "wg0"), "WireGuard interface (AUTOPROXY_WG_IFACE)")
	wgBin := fs.String("wg-bin", envOr("AUTOPROXY_WG_BIN", "wg"), "path to the wg binary (AUTOPROXY_WG_BIN)")
	ipBin := fs.String("ip-bin", envOr("AUTOPROXY_IP_BIN", "ip"), "path to the ip binary (AUTOPROXY_IP_BIN)")
	wgSubnet := fs.String("wg-subnet", envOr("AUTOPROXY_WG_SUBNET", "10.66.66.0/24"), "tunnel subnet (AUTOPROXY_WG_SUBNET)")
	wgPort := fs.Int("wg-port", atoiOr(envOr("AUTOPROXY_WG_PORT", "51820"), 51820), "WireGuard listen port (AUTOPROXY_WG_PORT)")
	publicIP := fs.String("public-ip", envOr("AUTOPROXY_PUBLIC_IP", ""), "this VPS's public IPv4, handed to clients in join codes (AUTOPROXY_PUBLIC_IP)")
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	if *showVersion {
		fmt.Println(version)
		return
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// The token is env-only on purpose: a command line flag would put it in
	// /proc/<pid>/cmdline and in every ps listing.
	token := os.Getenv("AUTOPROXY_TOKEN")
	if token == "" {
		log.Error("AUTOPROXY_TOKEN is not set; refusing to start without authentication")
		os.Exit(2)
	}

	reserved, err := parsePorts(*reservedStr)
	if err != nil {
		log.Error("bad AUTOPROXY_RESERVED_PORTS", "error", err)
		os.Exit(2)
	}

	subnet, err := netip.ParsePrefix(*wgSubnet)
	if err != nil || !subnet.Addr().Is4() {
		log.Error("bad AUTOPROXY_WG_SUBNET", "value", *wgSubnet)
		os.Exit(2)
	}
	vpsTunnelIP := subnet.Masked().Addr().Next()

	if *publicIP == "" {
		log.Error("AUTOPROXY_PUBLIC_IP is not set; join codes would have no endpoint for clients to dial")
		os.Exit(2)
	}

	wgPubKey := ""
	if b, err := os.ReadFile("/etc/wireguard/autoproxy-server.pub"); err == nil {
		wgPubKey = strings.TrimSpace(string(b))
	} else {
		log.Warn("cannot read the WireGuard public key; join codes will be unusable", "error", err)
	}

	wgm := wg.Manager{WGBin: *wgBin, IPBin: *ipBin, Iface: *wgIface}
	// publicAddr is only used to refuse a site peer's lan_cidrs that would
	// swallow the address every client dials. A value that does not parse
	// (a hostname, say) leaves it zero, and that check is skipped rather
	// than the agent refusing to start over something advisory.
	publicAddr, err := netip.ParseAddr(*publicIP)
	if err != nil {
		log.Warn("AUTOPROXY_PUBLIC_IP is not a plain IP address; lan_cidrs will not be checked against it", "value", *publicIP)
	}
	// The API port goes into every join code so a client knows where to check
	// in over the tunnel. A listen address without a parseable port leaves it
	// out, and the client falls back to the default 7443.
	apiPort := 0
	if _, p, err := net.SplitHostPort(*listen); err == nil {
		apiPort = atoiOr(p, 0)
	}
	pm := peers.NewManager(peers.Config{
		Subnet:    subnet,
		VPSIP:     vpsTunnelIP,
		Endpoint:  net.JoinHostPort(*publicIP, strconv.Itoa(*wgPort)),
		PublicIP:  publicAddr,
		VPSPubKey: wgPubKey,
		APIPort:   apiPort,
	}, peers.Store{Dir: *stateDir}, wgm, log)

	srv := api.New(api.Config{
		Token:       token,
		PublicIface: *publicIface,
		WGIface:     *wgIface,
		Reserved:    reserved,
		RulesFile:   *rulesFile,
		EnvFile:     *envFile,
		Version:     version,
		TunnelIP:    vpsTunnelIP,
	},
		state.Store{Dir: *stateDir},
		nft.Applier{Bin: *nftBin},
		wg.Reader{Bin: *wgBin, Iface: *wgIface},
		wgm, pm, log,
	)

	log.Info("starting",
		"version", version,
		"listen", *listen,
		"public_iface", *publicIface,
		"wg_iface", *wgIface,
		"wg_subnet", subnet.String(),
		"reserved_ports", *reservedStr,
		"rules_file", *rulesFile,
		"state_dir", *stateDir,
		"tunnel_checkin", "https://"+net.JoinHostPort(vpsTunnelIP.String(), strconv.Itoa(apiPort))+"/v1/tunnel/checkin")

	srv.Restore()

	cert, err := tls.LoadX509KeyPair(*certFile, *keyFile)
	if err != nil {
		log.Error("cannot load the TLS certificate; run 'autoproxy-agent setup' first",
			"cert", *certFile, "key", *keyFile, "error", err)
		os.Exit(1)
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Error("cannot listen", "listen", *listen, "error", err)
		os.Exit(1)
	}

	httpSrv := &http.Server{
		Handler: srv.Handler(),
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			// TLS 1.3 only. Every client that talks to this API is software we
			// ship or a current PHP/curl; there is no legacy to carry.
			MinVersion: tls.VersionTLS13,
		},
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ServeTLS(ln, "", "") }()

	log.Info("listening", "addr", ln.Addr().String(), "tls", "1.3+")

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server stopped", "error", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		// Rules and peers stay in place: players keep playing across an agent
		// restart, and a reboot re-applies them from state.
		log.Info("shutting down; nftables rules and WireGuard peers stay in place")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}
}

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}
