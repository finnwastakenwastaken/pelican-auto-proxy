package setup

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/wg"
)

// UninstallOptions are the uninstall flags.
type UninstallOptions struct {
	Yes bool
	Out io.Writer
	In  io.Reader
	Run Runner
}

// Uninstall removes everything setup created, listing it first.
//
// Packages (wireguard-tools, nftables) are deliberately left installed: the
// operator may have been using them before, and removing them is not ours to
// decide. Everything else -- unit, binary, config, state, both nft tables and
// the tunnel interface -- goes.
func Uninstall(opts UninstallOptions) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.In == nil {
		opts.In = os.Stdin
	}
	if opts.Run == nil {
		opts.Run = ExecRunner
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("autoproxy-agent uninstall must be run as root")
	}
	e := &env{opts: Options{Out: opts.Out, Run: func(n string, a ...string) (string, error) { return opts.Run(n, a...) }}, out: opts.Out, run: opts.Run}

	files := []string{UnitPath, BinPath, SysctlConf, WGConf, WGKey, WGPub}
	dirs := []string{EtcDir, StateDir}

	fmt.Fprintln(opts.Out, "This will remove Pelican Auto Proxy from this machine:")
	fmt.Fprintln(opts.Out, "  - stop and disable autoproxy-agent and wg-quick@wg0")
	fmt.Fprintln(opts.Out, "  - delete the nftables tables inet autoproxy_rules and inet autoproxy_base")
	fmt.Fprintln(opts.Out, "  - bring down and delete the wg0 interface (this also drops its peer routes)")
	fmt.Fprintf(opts.Out, "  - remove the peer-routing ip rule (fwmark %s, table %s) -- it outlives the\n", wg.DefaultFWMark, wg.DefaultTable)
	fmt.Fprintln(opts.Out, "    interface, since it names a table and a mark, not a device")
	for _, f := range files {
		fmt.Fprintf(opts.Out, "  - delete %s\n", f)
	}
	for _, d := range dirs {
		fmt.Fprintf(opts.Out, "  - delete %s/ and everything in it (peers, rules, token, certificate)\n", d)
	}
	if _, err := os.Stat(NftBackup); err == nil {
		fmt.Fprintf(opts.Out, "  - restore %s from %s\n", NftConf, NftBackup)
	} else if b, err := os.ReadFile(NftConf); err == nil && strings.Contains(string(b), NftMarker) {
		fmt.Fprintf(opts.Out, "  - delete %s (we wrote it, and there is no earlier copy to restore)\n", NftConf)
		fmt.Fprintln(opts.Out, "    NOTE: this machine will have NO firewall afterwards. Install your own.")
	}
	fmt.Fprintln(opts.Out, "The wireguard-tools and nftables packages are left installed.")
	fmt.Fprintln(opts.Out, "Forwarded game ports stop working the moment this runs.")

	if !opts.Yes {
		fmt.Fprint(opts.Out, "\nType 'yes' to continue: ")
		sc := bufio.NewScanner(opts.In)
		if !sc.Scan() || strings.TrimSpace(strings.ToLower(sc.Text())) != "yes" {
			fmt.Fprintln(opts.Out, "Aborted. Nothing was changed.")
			return nil
		}
	}

	// Services first: stop writing before deleting what is being written.
	for _, unit := range []string{"autoproxy-agent", "wg-quick@wg0"} {
		e.tryExec("systemctl", "disable", "--now", unit)
	}
	// Tables by exact name. Never "flush ruleset": that would take out
	// whatever firewall the operator puts back.
	for _, t := range []string{"autoproxy_rules", "autoproxy_base"} {
		if _, err := opts.Run("nft", "list", "table", "inet", t); err == nil {
			e.tryExec("nft", "delete", "table", "inet", t)
			fmt.Fprintf(opts.Out, ">> deleted table inet %s\n", t)
		}
	}
	if _, err := opts.Run("ip", "link", "show", WGIface); err == nil {
		e.tryExec("ip", "link", "delete", WGIface)
	}

	// The ip rule names a fwmark and a table, not a device, so deleting wg0
	// above does not take it with it -- unlike the peer routes in that table,
	// which the kernel drops along with the device they point at. Leaving the
	// rule behind would mean every packet on this machine consults an empty
	// table forever after an uninstall: harmless in practice, but it is our
	// litter and "uninstall leaves no trace" is a gate we test.
	//
	// The flush afterwards is belt and braces for the case where something
	// other than a wg0 route ended up in our table.
	wgm := wg.Manager{WGBin: "wg", IPBin: "ip", Iface: WGIface, Run: func(n string, a ...string) (string, error) { return opts.Run(n, a...) }}
	if err := wgm.RemoveRoutingRule(); err != nil {
		e.warn("could not remove the peer-routing ip rule: %v", err)
	} else {
		fmt.Fprintf(opts.Out, ">> removed the peer-routing ip rule (priority %s)\n", wg.DefaultRulePriority)
	}
	// Only if there is something there: an empty routing table does not
	// exist as far as the kernel is concerned, and flushing it prints
	// "FIB table does not exist", which would be a warning in the operator's
	// uninstall output for the entirely normal case of no peers.
	if out, err := opts.Run("ip", "route", "show", "table", wg.DefaultTable); err == nil && strings.TrimSpace(out) != "" {
		e.tryExec("ip", "route", "flush", "table", wg.DefaultTable)
	}

	if b, err := os.ReadFile(NftBackup); err == nil {
		if err := os.WriteFile(NftConf, b, 0o644); err != nil {
			e.warn("could not restore %s: %v", NftConf, err)
		} else {
			fmt.Fprintf(opts.Out, ">> restored %s from %s\n", NftConf, NftBackup)
			_ = os.Remove(NftBackup)
		}
	} else if b, err := os.ReadFile(NftConf); err == nil && strings.Contains(string(b), NftMarker) {
		_ = os.Remove(NftConf)
		fmt.Fprintf(opts.Out, ">> deleted %s (this machine now has no firewall)\n", NftConf)
	}

	for _, f := range files {
		if err := os.Remove(f); err == nil {
			fmt.Fprintf(opts.Out, ">> deleted %s\n", f)
		}
	}
	for _, d := range dirs {
		if err := os.RemoveAll(d); err == nil {
			fmt.Fprintf(opts.Out, ">> deleted %s/\n", d)
		}
	}
	e.tryExec("systemctl", "daemon-reload")

	fmt.Fprintln(opts.Out, "\nDone. Pelican Auto Proxy is removed.")
	fmt.Fprintln(opts.Out, "Remove the VPS from the Pelican plugin's Setup page too, and uninstall")
	fmt.Fprintln(opts.Out, "autoproxy-client on each node host with: autoproxy-client uninstall")
	return nil
}
