#!/usr/bin/env bash
# Unit tests for the pure render/decode functions in client/autoproxy-client.
# Sources the script with AUTOPROXY_RENDER_ONLY=1 so main() never runs and
# no network/root access is required; this only exercises string rendering
# and the join-code decoder.
#
# Usage: bash client/tests/test-render.sh

set -euo pipefail

# HAVE_PYTHON3 is read by decode_method/decode_join_code/json_get, sourced
# below from autoproxy-client with `source=/dev/null` (so shellcheck cannot
# see the cross-file use and reports SC2034 on every assignment here).
# shellcheck disable=SC2034

here="$(cd "$(dirname "$0")" && pwd)"
script="$here/../autoproxy-client"
expected_dir="$here/expected"

fail=0

check() {
    local name="$1" got="$2" want_file="$3"
    if diff -u "$want_file" <(printf '%s\n' "$got") >/tmp/autoproxy-test-diff.$$ 2>&1; then
        echo "ok:   $name"
    else
        echo "FAIL: $name"
        cat /tmp/autoproxy-test-diff.$$
        fail=1
    fi
    rm -f /tmp/autoproxy-test-diff.$$
}

check_eq() {
    local name="$1" got="$2" want="$3"
    if [[ "$got" == "$want" ]]; then
        echo "ok:   $name"
    else
        echo "FAIL: $name: got '${got}', want '${want}'"
        fail=1
    fi
}

export AUTOPROXY_RENDER_ONLY=1
# shellcheck source=/dev/null
source "$script"

# --- nft rendering: the client-side ruleset, iface autoproxy0 ------------

check "real.nft" \
    "$(render_nft "10.66.66.0/24" "10.66.66.1" "")" \
    "$expected_dir/real.nft"

check "custom-subnet.nft (variable substitution)" \
    "$(render_nft "192.0.2.0/24" "192.0.2.1" "")" \
    "$expected_dir/custom-subnet.nft"

check "host-ip.nft (--host-ip fallback chain)" \
    "$(render_nft "10.66.66.0/24" "10.66.66.1" "198.51.100.20" "" "10.66.66.2")" \
    "$expected_dir/host-ip.nft"

# Site mode renders the masquerade chain, scoped to this peer's LAN ranges.
check "site.nft (masquerade, scoped to the peer's lan_cidrs)" \
    "$(render_nft "10.66.66.0/24" "10.66.66.1" "" "192.0.2.0/24,198.51.100.0/24")" \
    "$expected_dir/site.nft"

# Real mode must render NO masquerade at all: a Wings host forwards the game
# port into a container across a bridge, so any masquerade here would replace
# the player's address with the bridge gateway's. This is the regression that
# broke real-IP mode on the first live test.
real_out="$(render_nft "10.66.66.0/24" "10.66.66.1" "")"
if grep -q 'masquerade' <<<"$real_out"; then
    echo "FAIL: real mode rendered a masquerade rule (real player IPs would be lost)"
    fail=1
else
    echo "ok:   real mode renders no masquerade rule"
fi

# --- wg conf rendering: AllowedIPs 0.0.0.0/0 in both modes, key masking --

wg_out="$(render_wg_conf "PRIVKEYVALUE" "10.66.66.7/32" "PUBKEYVALUE" "203.0.113.10:51820" "25" "0")"
if grep -q '^AllowedIPs = 0.0.0.0/0$' <<<"$wg_out"; then
    echo "ok:   wg conf AllowedIPs is 0.0.0.0/0"
else
    echo "FAIL: wg conf AllowedIPs is not 0.0.0.0/0"
    fail=1
fi
if grep -q '^PrivateKey = PRIVKEYVALUE$' <<<"$wg_out"; then
    echo "ok:   wg conf unmasked key present when mask=0"
else
    echo "FAIL: wg conf unmasked key missing"
    fail=1
fi

wg_masked="$(render_wg_conf "PRIVKEYVALUE" "10.66.66.7/32" "PUBKEYVALUE" "203.0.113.10:51820" "25" "1")"
if grep -q 'PRIVKEYVALUE' <<<"$wg_masked"; then
    echo "FAIL: wg conf leaked the private key with mask=1"
    fail=1
else
    echo "ok:   wg conf masks the private key when mask=1"
fi

# --- join-code decoder: python3 path and bash fallback must agree --------
# (races the fast path (python3) against the slow one (pure bash) and
# compares the actual decoded values, not which one ran faster.)

# Exactly the fields agent/internal/peers.JoinCode marshals, in its order.
# No "name": the agent does not put one in a join code, and a fixture that
# invents a field is how the client grows a dependency on something that will
# never arrive.
sample_json='{"v":1,"endpoint":"203.0.113.10:51820","vps_wg_pubkey":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","client_privkey":"BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=","client_address":"10.66.66.7/32","tunnel_subnet":"10.66.66.0/24","vps_tunnel_ip":"10.66.66.1","mode":"real","lan_cidrs":["192.0.2.0/24","198.51.100.0/24"],"keepalive":25}'

if ! command -v python3 >/dev/null 2>&1; then
    echo "SKIP: decoder agreement test (python3 not installed on this host)"
else
    code="$(python3 -c 'import base64,sys; print(base64.urlsafe_b64encode(sys.stdin.buffer.read()).decode().rstrip("="))' <<<"$sample_json")"

    fields="v endpoint vps_wg_pubkey client_privkey client_address tunnel_subnet vps_tunnel_ip mode keepalive lan_cidrs"

    HAVE_PYTHON3=1
    check_eq "decode_method: python3 label" "$(decode_method)" "python3"
    json_py="$(decode_join_code "$code")"

    HAVE_PYTHON3=0
    check_eq "decode_method: bash label" "$(decode_method)" "bash"
    json_bash="$(decode_join_code "$code")"

    check_eq "decoder agreement: raw JSON text (python3 vs bash base64url)" "$json_bash" "$json_py"

    for key in $fields; do
        HAVE_PYTHON3=1
        val_py="$(json_get "$json_py" "$key")"
        # shellcheck disable=SC2034  # read by json_get, sourced from autoproxy-client
        HAVE_PYTHON3=0
        val_bash="$(json_get "$json_bash" "$key")"
        check_eq "json_get('${key}'): python3 vs bash agree" "$val_bash" "$val_py"
    done

    check_eq "json_get('endpoint') value" "$(json_get "$json_py" endpoint)" "203.0.113.10:51820"
    check_eq "json_get('mode') value" "$(json_get "$json_py" mode)" "real"
    check_eq "json_get('lan_cidrs') value (comma-joined)" "$(json_get "$json_py" lan_cidrs)" "192.0.2.0/24,198.51.100.0/24"
fi

# --- validate_join_code: accepts good, rejects bad ------------------------

if (
    validate_join_code "1" "203.0.113.10:51820" "PUB" "PRIV" "10.66.66.7/32" \
        "10.66.66.0/24" "10.66.66.1" "real" "25"
); then
    echo "ok:   validate_join_code accepts a well-formed join code"
else
    echo "FAIL: validate_join_code rejected a well-formed join code"
    fail=1
fi

if (
    validate_join_code "2" "not-an-endpoint" "" "" "bad" "bad" "bad" "wrong-mode" "abc" 2>/dev/null
); then
    echo "FAIL: validate_join_code accepted a malformed join code"
    fail=1
else
    echo "ok:   validate_join_code rejects a malformed join code"
fi

# --- render_client_json: round-trips through json_get ---------------------

client_json="$(render_client_json "1" "203.0.113.10:51820" "PUB" "PRIV" "10.66.66.7/32" \
    "10.66.66.0/24" "10.66.66.1" "site" "25" "192.0.2.0/24,198.51.100.0/24" "192.0.2.50")"
check_eq "render_client_json: mode round-trips" "$(json_get "$client_json" mode)" "site"
check_eq "render_client_json: host_ip round-trips" "$(json_get "$client_json" host_ip)" "192.0.2.50"
check_eq "render_client_json: lan_cidrs round-trips" "$(json_get "$client_json" lan_cidrs)" "192.0.2.0/24,198.51.100.0/24"

# --- repath_due: when the run loop moves the tunnel to a new local port ----

repath_case() {
    local name="$1" age="$2" since_last="$3" want="$4" got=no
    repath_due "$age" "$since_last" && got=yes
    check_eq "repath_due: $name" "$got" "$want"
}
repath_case "healthy tunnel between rekeys (150s) is left alone" 150 -1 no
repath_case "just under the limit is left alone" 179 -1 no
repath_case "first move once the session is dead (180s)" 180 -1 yes
repath_case "no second move 30s after the first" 210 30 no
repath_case "second move a full interval after the first" 360 180 yes
repath_case "long outage keeps moving" 900 200 yes

# --- checkin_repath_due: timed-out check-ins with a fresh handshake ---------

checkin_case() {
    local name="$1" timeouts="$2" want="$3" got=no
    checkin_repath_due "$timeouts" && got=yes
    check_eq "checkin_repath_due: $name" "$got" "$want"
}
checkin_case "one timeout can be a blip" 1 no
checkin_case "two in a row moves the tunnel" 2 yes
checkin_case "no timeouts" 0 no

# --- versions: the client's twin of agent/internal/clients.Compare ---------

vcmp_case() {
    local a="$1" b="$2" want="$3" got
    got="$(version_cmp "$a" "$b")" || got="unparsable"
    check_eq "version_cmp $a vs $b" "$got" "$want"
}
vcmp_case 0.3.0 0.3.0 0
vcmp_case v0.3.0 0.3.0 0
vcmp_case 0.2.7 0.3.0 -1
vcmp_case 0.10.0 0.9.9 1
vcmp_case 0.3.0-rc1 0.3.0 -1
vcmp_case 0.3.0 0.3.0-rc1 1
vcmp_case 0.3.0-dev 0.3.0 -1
vcmp_case 0.3.0+b1 0.3.0 0
vcmp_case 0.08.0 0.8.0 0
vcmp_case dev 0.3.0 unparsable
vcmp_case 0.3 0.3.0 unparsable

check_eq "normalize_release v0.3.1" "$(normalize_release v0.3.1)" "0.3.1"
for bad in "latest" "0.3" "0.3.1-rc1" "0.3.1/../../x" "0.3.1 x" "" "v12345.0.0"; do
    if normalize_release "$bad" >/dev/null; then
        echo "FAIL: normalize_release accepted '$bad'"
        fail=1
    else
        echo "ok:   normalize_release refuses '$bad'"
    fi
done

# --- remote_update_decision: every branch -----------------------------------
# args: desired, request id, own version, flavour, remote updates enabled, last request id

rud_case() {
    local name="$1" want="$2"; shift 2
    local got
    got="$(remote_update_decision "$@")"
    check_eq "remote_update_decision: $name" "${got%%:*}" "$want"
}
rud_case "nothing requested" none "" "" 0.3.0 systemd 1 ""
rud_case "newer release requested" run 0.3.1 r1 0.3.0 systemd 1 ""
rud_case "same request is not retried by itself" none 0.3.1 r1 0.3.0 systemd 1 r1
rud_case "a new request id retries" run 0.3.1 r2 0.3.0 systemd 1 r1
rud_case "already there" none 0.3.1 r1 0.3.1 systemd 1 ""
rud_case "never a downgrade" none 0.2.7 r1 0.3.0 systemd 1 ""
rud_case "node switched remote updates off" refuse 0.3.1 r1 0.3.0 systemd 0 ""
rud_case "docker cannot replace itself" refuse 0.3.1 r1 0.3.0 docker 1 ""
rud_case "not a release number" refuse "0.3.1;reboot" r1 0.3.0 systemd 1 ""
rud_case "a development copy follows a request" run 0.3.1 r1 dev systemd 1 ""

# --- checkin_body: valid JSON the agent can decode, whatever the error says --

body="$(checkin_body 0.3.0 systemd true failed 0.3.1 'curl: (6) "quoted" \ back	slash' r1)"
if command -v python3 >/dev/null 2>&1; then
    parsed="$(python3 -c 'import json,sys; d=json.loads(sys.argv[1]); print(d["version"], d["flavour"], d["remote_updates"], d["update"]["state"], d["update"]["request_id"])' "$body" 2>&1)"
    check_eq "checkin_body with an awkward error message is valid JSON" "$parsed" "0.3.0 systemd True failed r1"
    parsed="$(python3 -c 'import json,sys; d=json.loads(sys.argv[1]); print("update" in d)' "$(checkin_body 0.3.0 docker false "" "" "" "")")"
    check_eq "checkin_body without an update carries no update key" "$parsed" "False"
else
    echo "SKIP: checkin_body JSON check (python3 not installed on this host)"
fi

# --- render_client_json: api_port (0.3.0 join codes) round-trips ----------

client_json="$(render_client_json "1" "203.0.113.10:51820" "PUB" "PRIV" "10.66.66.7/32" \
    "10.66.66.0/24" "10.66.66.1" "real" "25" "" "" "8443")"
check_eq "render_client_json: api_port round-trips" "$(json_get "$client_json" api_port)" "8443"

# The script in the tree is never a release: scripts/package-client.sh stamps
# the real number when it builds the tarball.
check_eq "the source tree says AUTOPROXY_VERSION=dev" "$AUTOPROXY_VERSION" "dev"

echo
if [[ $fail -ne 0 ]]; then
    echo "test-render: FAILED"
    exit 1
fi
echo "test-render: all green"
