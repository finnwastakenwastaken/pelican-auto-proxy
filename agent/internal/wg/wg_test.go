package wg

import (
	"testing"
	"time"
)

// Real output shape of "wg show wg0 dump": one interface line, then one line
// per peer, tab separated.
const dump = "PRIVKEY=\tSRVPUB=\t51820\toff\n" +
	"PEERPUB=\t(none)\t203.0.113.9:51820\t10.66.66.2/32\t1700000000\t12345\t67890\t25\n"

func TestParsePeer(t *testing.T) {
	now := time.Unix(1700000030, 0)
	st := Parse(dump, "wg0", now)
	if st.Peers != 1 {
		t.Fatalf("expected 1 peer, got %d", st.Peers)
	}
	if st.HandshakeAge == nil || *st.HandshakeAge != 30 {
		t.Fatalf("expected handshake age 30, got %v", st.HandshakeAge)
	}
	if st.RX != 12345 || st.TX != 67890 {
		t.Fatalf("unexpected counters %d/%d", st.RX, st.TX)
	}
}

func TestParseNeverHandshaked(t *testing.T) {
	d := "PRIV\tPUB\t51820\toff\n" +
		"PEERPUB\t(none)\t(none)\t10.66.66.2/32\t0\t0\t0\t25\n"
	st := Parse(d, "wg0", time.Unix(1700000000, 0))
	if st.HandshakeAge != nil {
		t.Fatalf("a peer that never handshaked must report null, got %v", *st.HandshakeAge)
	}
	if st.Peers != 1 {
		t.Fatalf("expected 1 peer, got %d", st.Peers)
	}
}

func TestParseNoPeers(t *testing.T) {
	st := Parse("PRIV\tPUB\t51820\toff\n", "wg0", time.Now())
	if st.Peers != 0 || st.HandshakeAge != nil {
		t.Fatalf("expected an empty status, got %+v", st)
	}
}

func TestReadReportsMissingInterfaceInsteadOfFailing(t *testing.T) {
	st := Reader{Bin: "/bin/false", Iface: "wg0"}.Read()
	if st.Error == "" {
		t.Fatal("a missing tunnel must show up as an error field, not a crash")
	}
}
