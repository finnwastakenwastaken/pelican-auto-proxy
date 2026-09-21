#!/bin/bash
# Bring test/fake-agent.py up inside a throwaway container, on TLS, with a
# certificate a caller can verify against - the same shape the plugin gets from
# a real VPS code. Runs INSIDE the container only.
#
# Writes into /shared: vps-code, agent.pem, join-real, join-site, then ready.
set -u

# Bound on loopback: this container has no NET_ADMIN, so it cannot put a
# documentation address on an interface. The address clients are TOLD to dial
# is a separate thing, and --advertise-ip keeps that a documentation range.
BIND=127.0.0.1
ADVERTISE=203.0.113.10
PORT=7443
TOKEN=devtoken0000000000000000000000000000
SHARED=/shared
mkdir -p "$SHARED"

echo ">> certificate for $BIND"
openssl req -x509 -newkey rsa:2048 -nodes -days 2 \
	-subj "/CN=$BIND" -addext "subjectAltName=IP:$BIND" \
	-keyout /tmp/agent.key -out /tmp/agent.pem >/dev/null 2>&1 || {
	echo "openssl failed"; exit 1;
}
cp /tmp/agent.pem "$SHARED/agent.pem"

echo ">> fake agent"
# A long simulated handshake delay so handshake_age_s is reliably null while
# the probe runs: the probe asserts the "waiting for first handshake" state,
# and a peer that connects mid-run would make that assertion flap.
python3 /fake-agent.py --host "$BIND" --port "$PORT" --token "$TOKEN" \
	--tls --tls-cert /tmp/agent.pem --tls-key /tmp/agent.key \
	--advertise-ip "$ADVERTISE" --simulate-handshake-after 3600 \
	> /tmp/fake.log 2>&1 &

for _ in $(seq 1 60); do
	sleep 0.25
	if ss -lnt 2>/dev/null | grep -q ":$PORT"; then break; fi
done
if ! ss -lnt 2>/dev/null | grep -q ":$PORT"; then
	echo "the fake agent is not listening"; cat /tmp/fake.log; exit 1
fi

# The same VPS code the installer would print, so the probe's seeding path is
# identical for both servers.
python3 - "$BIND" "$PORT" "$TOKEN" > "$SHARED/vps-code" <<'EOF'
import base64, json, sys
ip, port, token = sys.argv[1], int(sys.argv[2]), sys.argv[3]
pem = open("/tmp/agent.pem", "rb").read()
code = {
    "endpoint_ip": ip,
    "api_port": port,
    "token": token,
    "api_ca_pem": base64.b64encode(pem).decode(),
    "api_spki_sha256": "",
    "wg_pubkey": "",
    "wg_port": 51820,
    "tunnel_subnet": "10.66.66.0/24",
    "vps_tunnel_ip": "10.66.66.1",
    "version": "fake-0.2.0",
}
sys.stdout.write(base64.urlsafe_b64encode(json.dumps(code).encode()).decode().rstrip("="))
EOF

api() {
	curl -sS --cacert /tmp/agent.pem -H "Authorization: Bearer $TOKEN" \
		-H 'Content-Type: application/json' "$@"
}
api -X POST -d '{"name":"joincode-real","mode":"real"}' \
	"https://$BIND:$PORT/v1/peers" > /tmp/jc-real.json
api -X POST -d '{"name":"joincode-site","mode":"site","lan_cidrs":["192.168.40.0/24"]}' \
	"https://$BIND:$PORT/v1/peers" > /tmp/jc-site.json
python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["join_code"])' /tmp/jc-real.json > "$SHARED/join-real"
python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["join_code"])' /tmp/jc-site.json > "$SHARED/join-site"

# An empty join code would let the caller run a whole suite against nothing and
# report the silence as a contract failure. Fail here instead.
for f in join-real join-site; do
	if [ ! -s "$SHARED/$f" ]; then
		echo "no $f was produced; the API calls above failed"; cat /tmp/fake.log; exit 1
	fi
done

touch "$SHARED/ready"
echo ">> ready"

for _ in $(seq 1 600); do
	sleep 1
	[ -f "$SHARED/done" ] && break
done
cat /tmp/fake.log
