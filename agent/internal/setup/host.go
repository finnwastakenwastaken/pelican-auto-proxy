// Package setup provisions and removes the VPS side of Pelican Auto Proxy.
//
// It replaces what used to be a shell script. Putting it in the binary means
// the installer is one download with a checksum, the OS gate and the
// provisioning logic cannot drift apart, and "uninstall" is written by the
// same code that knows every path it created.
//
// Nothing here touches sshd. Silently hardening SSH from a provisioning script
// is how people get locked out of a box they still had a session on.
package setup

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// supported lists the platforms the installers promise. Anything else is
// refused with the list named, rather than half-working. Adding a platform is
// one entry here (and in the installers' matching case statements).
var supported = map[string][]string{
	"debian": {"12", "13"},
}

// OSInfo is the parsed /etc/os-release.
type OSInfo struct {
	ID        string
	VersionID string
	Pretty    string
}

// DetectOS reads /etc/os-release.
func DetectOS(path string) (OSInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return OSInfo{}, fmt.Errorf("cannot read %s: %w", path, err)
	}
	defer f.Close()
	info := OSInfo{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "ID":
			info.ID = strings.ToLower(v)
		case "VERSION_ID":
			info.VersionID = v
		case "PRETTY_NAME":
			info.Pretty = v
		}
	}
	return info, sc.Err()
}

// Supported reports whether this platform is one we test on.
func (o OSInfo) Supported() bool {
	for _, v := range supported[o.ID] {
		if o.VersionID == v {
			return true
		}
	}
	return false
}

// UnsupportedMessage is what a stranger on the wrong OS reads. It names what
// does work, so the next step is obvious.
func (o OSInfo) UnsupportedMessage() string {
	name := o.Pretty
	if name == "" {
		name = o.ID + " " + o.VersionID
	}
	if o.ID == "ubuntu" {
		return "Ubuntu is not supported in this release; Debian 12 or 13 only. " +
			"Ubuntu support is tracked for a later release."
	}
	return fmt.Sprintf("%s is not supported.\n"+
		"Pelican Auto Proxy is tested on Debian 12 and Debian 13.\n"+
		"Install one of those on this VPS and run the installer again.", strings.TrimSpace(name))
}

// Net describes what we detected about the machine's connection to the world.
type Net struct {
	Iface    string
	PublicIP string
}

// DetectNet finds the interface and source address the default route uses.
// "ip route get" is asked rather than reading addresses off an interface,
// because that answers the question we actually have: which address do packets
// to the internet leave from.
func DetectNet(run Runner) (Net, error) {
	out, err := run("ip", "-o", "route", "get", "1.1.1.1")
	if err != nil {
		return Net{}, fmt.Errorf("could not determine the default route: %w", err)
	}
	fields := strings.Fields(out)
	var n Net
	for i := 0; i < len(fields)-1; i++ {
		switch fields[i] {
		case "dev":
			n.Iface = fields[i+1]
		case "src":
			n.PublicIP = fields[i+1]
		}
	}
	if n.Iface == "" {
		return Net{}, fmt.Errorf("could not parse the outgoing interface from: %s", out)
	}
	return n, nil
}

// DetectSSHPort asks sshd itself. Guessing 22 when sshd listens elsewhere
// would install a base firewall that locks the operator out of their own VPS,
// so a failure here is reported loudly rather than defaulted away silently.
func DetectSSHPort() (port int, detected bool) {
	out, err := exec.Command("sshd", "-T").Output()
	if err != nil {
		return 22, false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "port ") {
			var p int
			if _, err := fmt.Sscanf(line, "port %d", &p); err == nil && p > 0 && p < 65536 {
				return p, true
			}
		}
	}
	return 22, false
}

// HasDocker reports whether Docker is installed. Orphaned "ip nat" and
// "ip filter" tables are only safe to delete when it is not: with Docker
// present they are its live firewall.
func HasDocker() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}
