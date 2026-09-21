#!/bin/bash
# Feed a REAL join code, minted by the real agent, to the real client's decoder.
#
# Runs INSIDE a throwaway container. Sources client/autoproxy-client with
# AUTOPROXY_RENDER_ONLY=1, so main() never runs: nothing here touches the
# network, WireGuard or nftables. It only proves the client understands what
# the agent actually emits - field names, unpadded base64url, endpoint format,
# lan_cidrs, keepalive - rather than what a fixture says it emits.
set -u

SCRIPT=/client/autoproxy-client
PASS=0
FAIL=0
ok()  { printf 'PASS  %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf 'FAIL  %s\n' "$*"; FAIL=$((FAIL+1)); }

export AUTOPROXY_RENDER_ONLY=1
# shellcheck source=/dev/null
source "$SCRIPT"

check_code() {
	local label="$1" file="$2" want_mode="$3" want_lan="$4"
	local code json

	code="$(tr -d '\n\r' < "$file")"

	printf '\n=== %s ===\n' "$label"
	printf 'join code: %d chars, alphabet check: ' "${#code}"
	if [[ "$code" =~ ^[A-Za-z0-9_-]+$ ]]; then
		printf 'base64url, unpadded\n'
		ok "$label: join code is unpadded base64url (no +, / or =)"
	else
		printf 'NOT base64url\n'
		bad "$label: join code is not unpadded base64url"
	fi

	# Both decoders, because the client picks one at runtime and a host
	# without python3 must get the same answer.
	# shellcheck disable=SC2034  # read by decode_join_code/json_get, sourced from autoproxy-client
	HAVE_PYTHON3=1
	local json_py
	json_py="$(decode_join_code "$code")"
	HAVE_PYTHON3=0
	local json_bash
	json_bash="$(decode_join_code "$code")"

	if [[ "$json_py" == "$json_bash" ]]; then
		ok "$label: python3 and the bash fallback decode it identically"
	else
		bad "$label: the two decoders disagree"
	fi

	# shellcheck disable=SC2034  # read by json_get, sourced from autoproxy-client
	HAVE_PYTHON3=1
	json="$json_py"
	printf 'decoded (private key elided):\n'
	printf '%s\n' "$json" | python3 -c '
import json, sys
d = json.load(sys.stdin)
d["client_privkey"] = "<redacted>"
print(json.dumps(d, indent=2))
'

	# Exactly the keys agent/internal/peers.JoinCode marshals. lan_cidrs
	# carries omitempty, so it is absent in real mode and present in site mode.
	local want_keys
	if [[ "$want_mode" == "site" ]]; then
		want_keys='client_address,client_privkey,endpoint,keepalive,lan_cidrs,mode,tunnel_subnet,v,vps_tunnel_ip,vps_wg_pubkey'
	else
		want_keys='client_address,client_privkey,endpoint,keepalive,mode,tunnel_subnet,v,vps_tunnel_ip,vps_wg_pubkey'
	fi
	local got_keys
	got_keys="$(printf '%s' "$json" | python3 -c 'import json,sys; print(",".join(sorted(json.load(sys.stdin))))')"
	if [[ "$got_keys" == "$want_keys" ]]; then
		ok "$label: the agent emits exactly the keys the client reads"
	else
		bad "$label: key mismatch"
		printf '  agent sent: %s\n  client expects: %s\n' "$got_keys" "$want_keys"
	fi

	local v endpoint vps_wg_pubkey client_privkey client_address
	local tunnel_subnet vps_tunnel_ip mode keepalive lan_cidrs
	v="$(json_get "$json" v)"
	endpoint="$(json_get "$json" endpoint)"
	vps_wg_pubkey="$(json_get "$json" vps_wg_pubkey)"
	client_privkey="$(json_get "$json" client_privkey)"
	client_address="$(json_get "$json" client_address)"
	tunnel_subnet="$(json_get "$json" tunnel_subnet)"
	vps_tunnel_ip="$(json_get "$json" vps_tunnel_ip)"
	mode="$(json_get "$json" mode)"
	keepalive="$(json_get "$json" keepalive)"
	lan_cidrs="$(json_get "$json" lan_cidrs)"

	if validate_join_code "$v" "$endpoint" "$vps_wg_pubkey" "$client_privkey" \
		"$client_address" "$tunnel_subnet" "$vps_tunnel_ip" "$mode" "$keepalive"; then
		ok "$label: validate_join_code accepts the agent's own code"
	else
		bad "$label: the client refused a join code the agent produced"
	fi

	# Written out rather than "A && B || C": ok/bad are counters, and a
	# counter that ever returned non-zero would silently run the other branch.
	if [[ "$v" == "1" ]]; then ok "$label: version 1"; else bad "$label: version is '$v'"; fi
	if [[ "$mode" == "$want_mode" ]]; then ok "$label: mode is $want_mode"; else bad "$label: mode is '$mode'"; fi
	if [[ "$keepalive" == "25" ]]; then ok "$label: keepalive 25"; else bad "$label: keepalive is '$keepalive'"; fi
	if [[ "$endpoint" =~ ^203\.0\.113\.10:51820$ ]]; then ok "$label: endpoint is ip:port"; else bad "$label: endpoint is '$endpoint'"; fi
	if [[ "$lan_cidrs" == "$want_lan" ]]; then ok "$label: lan_cidrs '$want_lan'"; else bad "$label: lan_cidrs is '$lan_cidrs'"; fi

	printf -- '--- rendered %s.conf ---\n' "$IFACE"
	render_wg_conf "$client_privkey" "$client_address" "$vps_wg_pubkey" "$endpoint" "$keepalive" 1

	printf -- '--- rendered client.json (key elided) ---\n'
	render_client_json "$v" "$endpoint" "$vps_wg_pubkey" "<redacted>" "$client_address" \
		"$tunnel_subnet" "$vps_tunnel_ip" "$mode" "$keepalive" "$lan_cidrs" ""
}

check_code "real-IP join code" /shared/join-real real ""
check_code "site join code" /shared/join-site site "192.168.40.0/24"

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
