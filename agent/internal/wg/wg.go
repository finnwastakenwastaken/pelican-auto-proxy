// Package wg reads WireGuard status by parsing "wg show <iface> dump".
//
// Parsing the dump format is deliberate: it is stable, tab separated and needs
// no cgo or netlink bindings.
package wg

import (
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Status is what /v1/status reports about the tunnel.
type Status struct {
	Iface        string `json:"iface"`
	Peers        int    `json:"peers"`
	HandshakeAge *int64 `json:"handshake_age_s"` // nil = never handshaked
	RX           int64  `json:"rx"`
	TX           int64  `json:"tx"`
	Error        string `json:"error,omitempty"`
}

// Reader runs the wg binary.
type Reader struct {
	Bin   string // default "wg"
	Iface string // default "wg0"
	Now   func() time.Time
}

func (r Reader) bin() string {
	if r.Bin == "" {
		return "wg"
	}
	return r.Bin
}

func (r Reader) iface() string {
	if r.Iface == "" {
		return "wg0"
	}
	return r.Iface
}

func (r Reader) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

// Read returns the tunnel status. Any failure is reported in Status.Error
// rather than as an error: a missing tunnel must not fail the status endpoint,
// because that endpoint is how the operator finds out the tunnel is missing.
func (r Reader) Read() Status {
	out, err := exec.Command(r.bin(), "show", r.iface(), "dump").Output()
	if err != nil {
		msg := err.Error()
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		return Status{Iface: r.iface(), Error: msg}
	}
	return Parse(string(out), r.iface(), r.now())
}

// Parse reads the output of "wg show <iface> dump". The first line describes
// the interface; every following line is a peer. Fields per peer:
// pubkey, psk, endpoint, allowed-ips, latest-handshake, rx, tx, keepalive.
func Parse(dump, iface string, now time.Time) Status {
	st := Status{Iface: iface}
	lines := strings.Split(strings.TrimRight(dump, "\n"), "\n")
	for i, line := range lines {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue // interface line
		}
		f := strings.Split(line, "\t")
		if len(f) < 8 {
			continue
		}
		st.Peers++
		hs, _ := strconv.ParseInt(f[4], 10, 64)
		rx, _ := strconv.ParseInt(f[5], 10, 64)
		tx, _ := strconv.ParseInt(f[6], 10, 64)
		st.RX += rx
		st.TX += tx
		if hs > 0 {
			age := now.Unix() - hs
			if age < 0 {
				age = 0
			}
			// Report the freshest handshake across peers.
			if st.HandshakeAge == nil || age < *st.HandshakeAge {
				a := age
				st.HandshakeAge = &a
			}
		}
	}
	return st
}
