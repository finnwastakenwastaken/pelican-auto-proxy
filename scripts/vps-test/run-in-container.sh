#!/bin/bash
# End-to-end gate for autoproxy-agent, run INSIDE a throwaway container.
# Never run this on a real host: it installs, starts and then removes the agent.
# No "pipefail" on purpose. "grep -q" exits as soon as it matches and closes
# the pipe, so the producer (nft, wg, ss) dies of SIGPIPE and the pipeline
# reports failure for a check that actually PASSED. That cost an hour here; it
# is exactly the "measurement that lies convincingly" this harness exists to
# avoid. Commands whose exit status matters are run into a file and checked
# explicitly instead.
set -u

PASS=0
FAIL=0
ok()   { printf 'PASS  %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf 'FAIL  %s\n' "$*"; FAIL=$((FAIL+1)); }
step() { printf '\n=== %s ===\n' "$*"; }

IP=203.0.113.10

# --- a network to detect --------------------------------------------------
# --network none leaves only lo. Give it the documentation address and a
# default route so "ip route get" has something to answer.
ip addr add "$IP/32" dev lo 2>/dev/null
ip link set lo up
ip route add default dev lo 2>/dev/null

# --- stubs ----------------------------------------------------------------
# systemd and apt do not run here. Everything else is the real thing.
mkdir -p /stub
cat > /stub/systemctl <<'EOS'
#!/bin/sh
echo "[stub systemctl] $*" >> /tmp/systemctl.log
case "$1" in is-active) echo active ;; esac
exit 0
EOS
cat > /stub/apt-get <<'EOS'
#!/bin/sh
echo "[stub apt-get] $*" >> /tmp/apt.log
exit 0
EOS
chmod +x /stub/systemctl /stub/apt-get
export PATH=/stub:$PATH

# b64url decodes an unpadded base64url string from stdin.
b64url() { python3 -c 'import base64,sys; s=sys.stdin.read().strip(); sys.stdout.buffer.write(base64.urlsafe_b64decode(s+"="*(-len(s)%4)))'; }
jget()   { python3 -c "import json,sys; print(json.load(sys.stdin).get('$1',''))"; }

step "OS gate refuses an unsupported release"
cp /etc/os-release /tmp/os-release.real
printf 'ID=centos\nVERSION_ID="9"\nPRETTY_NAME="CentOS 9"\n' > /etc/os-release
# Captured to a file first: with "set -o pipefail" a non-zero producer fails
# the whole pipeline even when grep matches, which would fail this check for
# exactly the reason it is testing for.
/agent setup --yes --public-ip "$IP" > /tmp/osgate.log 2>&1
rc=$?
if [ $rc -ne 0 ] && grep -q 'Debian 12 and Debian 13' /tmp/osgate.log; then
	ok "unsupported OS refused (exit $rc) and names what is supported"
else
	bad "unsupported OS was not refused with the supported list"; cat /tmp/osgate.log
fi
cp /tmp/os-release.real /etc/os-release

step "OS gate refuses Ubuntu with the explicit follow-up message"
printf 'ID=ubuntu\nVERSION_ID="24.04"\nPRETTY_NAME="Ubuntu 24.04.1 LTS"\n' > /etc/os-release
/agent setup --yes --public-ip "$IP" > /tmp/osgate-ubuntu.log 2>&1
rc=$?
if [ $rc -ne 0 ] && grep -q 'Ubuntu is not supported in this release' /tmp/osgate-ubuntu.log \
	&& grep -q 'tracked for a later release' /tmp/osgate-ubuntu.log; then
	ok "Ubuntu refused (exit $rc) with the follow-up message, not a generic unsupported error"
else
	bad "Ubuntu was not refused with the explicit follow-up message"; cat /tmp/osgate-ubuntu.log
fi
cp /tmp/os-release.real /etc/os-release

step "setup --dry-run changes nothing"
/agent setup --dry-run --public-ip "$IP" > /tmp/dry.log 2>&1
if grep -q 'DRY RUN complete' /tmp/dry.log; then ok "dry run completed"; else bad "dry run did not complete"; cat /tmp/dry.log; fi
NFT_BEFORE=$(md5sum /etc/nftables.conf 2>/dev/null)
for f in /etc/autoproxy /etc/wireguard/wg0.conf /usr/local/bin/autoproxy-agent /etc/systemd/system/autoproxy-agent.service; do
	if [ -e "$f" ]; then bad "dry run created $f"; fi
done
[ -e /etc/autoproxy ] || ok "dry run left the filesystem alone"
# The nftables package ships /etc/nftables.conf, so its existence proves
# nothing; what matters is that the dry run did not rewrite it.
if [ "$(md5sum /etc/nftables.conf 2>/dev/null)" = "$NFT_BEFORE" ]; then
	ok "dry run did not touch /etc/nftables.conf"
else
	bad "dry run rewrote /etc/nftables.conf"
fi
if grep -q 'Secrets are only shown on a real run' /tmp/dry.log; then ok "dry run withholds secrets"; else bad "dry run secret notice missing"; fi

step "install-vps.sh on a stock box, no --yes needed"
# Both Debian and Ubuntu ship an /etc/nftables.conf. If setup treated that as
# somebody else's firewall, the advertised one-command install would refuse on
# every fresh VPS.
AUTOPROXY_LOCAL_BINARY=/agent bash /install-vps.sh --public-ip "$IP" > /tmp/install.log 2>&1
rc=$?
if [ $rc -eq 0 ]; then
	ok "install-vps.sh completed with a local binary (exit 0)"
else
	bad "install-vps.sh exited $rc"; tail -25 /tmp/install.log
fi
if grep -q 'autoproxy-managed' /etc/nftables.conf 2>/dev/null; then
	ok "the distribution's default firewall was replaced without --yes"
else
	bad "setup refused the stock /etc/nftables.conf; every fresh VPS would fail"; grep -i 'refus' /tmp/install.log
fi
if grep -q "distribution's unmodified default" /tmp/install.log; then
	ok "and said so in the output"
else
	bad "the replacement was not explained"
fi
if [ -f /etc/autoproxy/vps-code ]; then ok "install-vps.sh produced a VPS code"; else bad "no VPS code after install-vps.sh"; fi
if [ -x /usr/local/bin/autoproxy-agent ]; then ok "install-vps.sh installed the binary"; else bad "binary missing after install-vps.sh"; fi
/usr/local/bin/autoproxy-agent uninstall --yes > /tmp/uninstall0.log 2>&1
if grep -q 'flush ruleset' /etc/nftables.conf 2>/dev/null; then
	ok "uninstall put the distribution default back"
else
	bad "the stock firewall was not restored"
fi

step "setup refuses to overwrite a firewall that is not ours"
printf '# somebody elses firewall\ntable inet mine {}\n' > /etc/nftables.conf
/agent setup --public-ip "$IP" > /tmp/refuse.log 2>&1
if grep -q "Refusing to overwrite somebody else's firewall" /tmp/refuse.log; then
	ok "foreign /etc/nftables.conf refused without --yes"
else
	bad "foreign /etc/nftables.conf was not protected"; tail -20 /tmp/refuse.log
fi
if grep -q 'somebody elses firewall' /etc/nftables.conf; then ok "the foreign firewall file is untouched"; else bad "the foreign firewall file was overwritten"; fi

step "setup --yes provisions the box"
/agent setup --yes --public-ip "$IP" > /tmp/setup.log 2>&1
rc=$?
if [ $rc -ne 0 ]; then bad "setup exited $rc"; tail -30 /tmp/setup.log; else ok "setup completed"; fi
if [ -f /etc/nftables.conf.pre-autoproxy ]; then ok "previous firewall kept at .pre-autoproxy"; else bad "no .pre-autoproxy copy"; fi
if grep -q 'autoproxy-managed' /etc/nftables.conf; then ok "base firewall installed with its marker"; else bad "base firewall marker missing"; fi
if [ -x /usr/local/bin/autoproxy-agent ]; then ok "binary installed"; else bad "binary not installed"; fi
if [ -f /etc/systemd/system/autoproxy-agent.service ]; then ok "unit installed"; else bad "unit not installed"; fi
for p in /etc/autoproxy/agent.env /etc/autoproxy/vps-code /etc/autoproxy/tls/agent.key; do
	m=$(stat -c '%a' "$p" 2>/dev/null)
	if [ "$m" = "600" ]; then ok "$p is 0600"; else bad "$p mode is '$m', expected 600"; fi
done
if grep -qE '^\[Peer\]' /etc/wireguard/wg0.conf; then
	bad "wg0.conf must not contain peers"
else
	ok "wg0.conf has no static peers"
fi
# The mark has to be in wg0.conf, not only applied by the agent: on a reboot
# nothing but wg-quick reads that file, and an interface that comes up
# unmarked routes its own handshakes into the tunnel for as long as it takes
# the agent to start.
if grep -qE '^FwMark = 0x2b' /etc/wireguard/wg0.conf; then
	ok "wg0.conf carries FwMark = 0x2b so the mark survives a reboot"
else
	bad "wg0.conf is missing FwMark = 0x2b"
	grep -v PrivateKey /etc/wireguard/wg0.conf
fi
ip rule show > /tmp/iprule.txt 2>&1
if grep -qE 'not .*fwmark 0x2b lookup 201' /tmp/iprule.txt; then
	ok "setup installed the peer-routing ip rule"
else
	bad "the peer-routing ip rule is missing after setup"; cat /tmp/iprule.txt
fi

step "the VPS code"
CODE=$(cat /etc/autoproxy/vps-code)
echo "$CODE" | b64url > /tmp/code.json
if python3 -m json.tool /tmp/code.json > /dev/null; then ok "VPS code is base64url JSON"; else bad "VPS code is not decodable"; fi
printf '  decoded (token and PEM elided):\n'
python3 -c "import json;d=json.load(open('/tmp/code.json'));d['token']='<redacted>';d['api_ca_pem']='<base64 PEM, %d chars>'%len(d['api_ca_pem']);print(json.dumps(d,indent=2))"
TOKEN=$(jget token < /tmp/code.json)
APIPORT=$(jget api_port < /tmp/code.json)
jget api_ca_pem < /tmp/code.json | base64 -d > /tmp/agent.pem
if grep -q 'BEGIN CERTIFICATE' /tmp/agent.pem; then ok "api_ca_pem decodes to a PEM certificate"; else bad "api_ca_pem is not a PEM"; fi
if /agent show-code | grep -q "$CODE"; then ok "show-code re-prints the saved code"; else bad "show-code did not reprint"; fi

step "bring the tunnel up and start the agent"
wg-quick up wg0 > /tmp/wg.log 2>&1 || { bad "wg-quick up failed"; cat /tmp/wg.log; }
# wg-quick, and nothing else, is what brings the interface up after a reboot.
# The mark therefore has to come out of wg0.conf: if it only arrived later
# from the agent, every boot would have an unmarked window.
wg show wg0 fwmark > /tmp/fwmark.txt 2>&1
if grep -q '0x2b' /tmp/fwmark.txt; then
	ok "wg-quick brought wg0 up already carrying fwmark 0x2b"
else
	bad "wg show wg0 fwmark reported '$(cat /tmp/fwmark.txt)', expected 0x2b"
fi
if wg show wg0 > /dev/null 2>&1; then ok "wg0 is up"; else bad "wg0 is not up"; fi
set -a
# shellcheck source=/dev/null
. /etc/autoproxy/agent.env
set +a
/usr/local/bin/autoproxy-agent run > /tmp/agent.log 2>&1 &
AGENT_PID=$!
for _ in $(seq 1 40); do sleep 0.25; ss -lnt 2>/dev/null | grep -q ":$APIPORT" && break; done
if ss -lnt | grep -q ":$APIPORT"; then
	ok "agent is listening on :$APIPORT"
else
	bad "agent is not listening"; cat /tmp/agent.log
fi

API="https://$IP:$APIPORT"
c() { curl -sS --cacert /tmp/agent.pem -H "Authorization: Bearer $TOKEN" "$@"; }

step "HTTPS, certificate and TLS version"
if curl -sS --cacert /tmp/agent.pem -o /dev/null -w '%{http_code} %{ssl_verify_result}\n' "$API/v1/status" | grep -q '^401 0$'; then
	ok "TLS verified against the shipped PEM (and no token means 401)"
else
	bad "TLS verification against the shipped PEM failed"
	curl -sSv --cacert /tmp/agent.pem "$API/v1/status" 2>&1 | tail -20
fi
if curl -sS -o /dev/null "$API/v1/status" 2>/dev/null; then
	bad "an untrusting client accepted the certificate"
else
	ok "an untrusting client refuses the certificate"
fi
if curl -sS --tlsv1.3 --tls-max 1.3 --cacert /tmp/agent.pem -o /dev/null "$API/v1/status" 2>/dev/null; then
	ok "TLS 1.3 is accepted"
else
	bad "TLS 1.3 was refused"
fi
if curl -sS --tlsv1.2 --tls-max 1.2 --cacert /tmp/agent.pem -o /dev/null "$API/v1/status" 2>/dev/null; then
	bad "TLS 1.2 was accepted"
else
	ok "TLS 1.2 is refused"
fi

step "status"
c "$API/v1/status" > /tmp/status.json
if python3 -c "
import json;d=json.load(open('/tmp/status.json'))
assert d['peers']=={'total':0,'healthy':0}, d['peers']
assert 'version' in d
print('  version=%s peers=%s' % (d['version'], d['peers']))
"; then
	ok "status reports version and an empty peer summary"
else
	bad "status payload wrong"
fi

step "create peers"
c -X POST -H 'Content-Type: application/json' -d '{"name":"wings-1","mode":"real"}' "$API/v1/peers" > /tmp/peer1.json
PEER1=$(python3 -c "import json;print(json.load(open('/tmp/peer1.json'))['peer']['id'])")
JOIN=$(python3 -c "import json;print(json.load(open('/tmp/peer1.json'))['join_code'])")
if [ -n "$PEER1" ]; then
	ok "real-IP peer created (id $PEER1)"
else
	bad "peer creation failed"; cat /tmp/peer1.json
fi
echo "$JOIN" | b64url > /tmp/join.json
printf '  join code (private key elided):\n'
python3 -c "import json;d=json.load(open('/tmp/join.json'));d['client_privkey']='<redacted>';print(json.dumps(d,indent=2))"
if wg show wg0 | grep -q 'peer:'; then ok "the peer is live on wg0"; else bad "the peer was not added to wg0"; fi
# The peer's route must be in the dedicated table and NOT in main. A peer
# route in main is the live routing-loop bug: it can shadow the path to an
# address the VPS has to reach directly, including a peer's own WireGuard
# endpoint, and the tunnel then never handshakes.
ip route show table 201 > /tmp/route201.txt 2>/dev/null
ip route show > /tmp/routemain.txt 2>&1
if grep -q '10.66.66.2' /tmp/route201.txt; then
	ok "route to the peer installed in table 201"
else
	bad "no route to the peer in table 201"; cat /tmp/route201.txt
fi
if grep -q '10.66.66.2' /tmp/routemain.txt; then
	bad "the peer route leaked into the main table (the routing-loop bug)"; cat /tmp/routemain.txt
else
	ok "the main table has no peer route"
fi

c -X POST -H 'Content-Type: application/json' -d '{"name":"lan-box","mode":"site","lan_cidrs":["10.0.0.0/24"]}' "$API/v1/peers" > /tmp/peer2.json
PEER2=$(python3 -c "import json;print(json.load(open('/tmp/peer2.json'))['peer']['id'])")
if [ -n "$PEER2" ]; then
	ok "site-mode peer created (id $PEER2)"
else
	bad "site peer failed"; cat /tmp/peer2.json
fi
ip route show table 201 > /tmp/route201b.txt 2>/dev/null
ip route show > /tmp/routemainb.txt 2>&1
if grep -q '10.0.0.0/24' /tmp/route201b.txt; then
	ok "route to the site LAN installed in table 201"
else
	bad "no route to the site LAN in table 201"; cat /tmp/route201b.txt
fi
if grep -q '10.0.0.0/24' /tmp/routemainb.txt; then
	bad "the site LAN route leaked into the main table"; cat /tmp/routemainb.txt
else
	ok "the main table has no site LAN route"
fi

# The one overlap the agent can see, it must refuse -- with a message the
# plugin can show the admin as-is, not a 500.
c -X POST -H 'Content-Type: application/json' -d "{\"name\":\"bad-site\",\"mode\":\"site\",\"lan_cidrs\":[\"10.0.0.0/8\"]}" "$API/v1/peers" > /tmp/badpeer.json 2>&1
if grep -q "contains this VPS" /tmp/badpeer.json || grep -q "overlaps" /tmp/badpeer.json; then
	ok "a lan_cidr that swallows an address the VPS must reach is refused with a reason"
else
	bad "an overlapping lan_cidr was not refused"; cat /tmp/badpeer.json
fi

if c "$API/v1/peers" | grep -q "$PEER1"; then ok "GET /v1/peers lists the peers"; else bad "peers not listed"; fi
if c "$API/v1/peers" | grep -q "$(python3 -c "import json;print(json.load(open('/tmp/join.json'))['client_privkey'])")"; then
	bad "the private key leaked from GET /v1/peers"
else
	ok "the private key is not readable back"
fi

step "push a real-IP rule and a site rule"
cat > /tmp/rules.json <<EOR
{"rules":[
 {"id":"alloc-1","proto":"both","public_port":9445,"target_peer":"$PEER1","note":"real player IPs"},
 {"id":"alloc-2","proto":"udp","public_port":27015,"target_ip":"10.0.0.10","via_peer":"$PEER2","note":"site mode"}
]}
EOR
c -X PUT -H 'Content-Type: application/json' -d @/tmp/rules.json "$API/v1/rules" > /tmp/put.json
if grep -q '"rules": 2' /tmp/put.json; then
	ok "both rules applied"
else
	bad "rules not applied"; cat /tmp/put.json
fi

nft list ruleset > /tmp/ruleset.txt
if grep -q '9445 : 10.66.66.2' /tmp/ruleset.txt; then ok "real-IP rule DNATs to the peer's tunnel address"; else bad "real-IP DNAT missing"; fi
if grep -q '27015 : 10.0.0.10' /tmp/ruleset.txt; then ok "site rule DNATs to the LAN address"; else bad "site DNAT missing"; fi
if grep -q 'elements = { 10.66.66.2 }' /tmp/ruleset.txt; then ok "direct_targets holds the real-IP peer"; else bad "direct_targets wrong"; fi
if grep -q 'ip daddr != @direct_targets masquerade' /tmp/ruleset.txt; then ok "masquerade excludes real-IP targets"; else bad "masquerade is unconditional"; fi
if grep -q 'table inet autoproxy_base' /tmp/ruleset.txt; then ok "base table loaded"; else bad "base table missing"; fi

step "memory at 1000 rules"
# The design promise is that memory does not scale with port count: the kernel
# holds the map, not a userspace proxy per port. Measured, not assumed.
python3 - "$PEER1" > /tmp/many.json <<'EOP'
import json,sys
peer=sys.argv[1]
rs=[{"id":"r%d"%i,"proto":"both","public_port":20000+i,"target_peer":peer} for i in range(1000)]
print(json.dumps({"rules":rs}))
EOP
c -X PUT -H 'Content-Type: application/json' -d @/tmp/many.json "$API/v1/rules" > /tmp/many-put.json
if grep -q '"rules": 1000' /tmp/many-put.json; then ok "1000 rules applied"; else bad "1000 rules not applied"; head -5 /tmp/many-put.json; fi
RSS=$(awk '/VmRSS/{print $2}' /proc/$AGENT_PID/status)
printf '  agent RSS with 1000 rules: %s kB\n' "$RSS"
if [ "$RSS" -lt 20480 ]; then
	ok "RSS ${RSS} kB is under the 20 MB target"
else
	bad "RSS ${RSS} kB exceeds the 20 MB target"
fi
ELEMS=$(nft list table inet autoproxy_rules | grep -c ' : ')
printf '  map element lines: %s\n' "$ELEMS"
# Put the small set back for the checks that follow.
c -X PUT -H 'Content-Type: application/json' -d @/tmp/rules.json "$API/v1/rules" > /dev/null

step "a rule naming an unknown peer is refused"
c -X PUT -H 'Content-Type: application/json' -d '{"rules":[{"id":"x","proto":"tcp","public_port":9000,"target_peer":"ghost"}]}' "$API/v1/rules" > /tmp/reject.json
if grep -q 'not a known peer' /tmp/reject.json; then
	ok "unknown peer rejected with a clear reason"
else
	bad "unknown peer not rejected"; cat /tmp/reject.json
fi

step "deleting a peer closes its ports"
if c -X DELETE "$API/v1/peers/$PEER1" -o /dev/null -w '%{http_code}\n' | grep -q 204; then
	ok "peer deleted (204)"
else
	bad "delete did not return 204"
fi
nft list ruleset > /tmp/ruleset2.txt
if grep -q '9445' /tmp/ruleset2.txt; then
	bad "the deleted peer's port is still open"
else
	ok "the deleted peer's port closed"
fi
if grep -q '27015' /tmp/ruleset2.txt; then ok "the other peer's port stayed open"; else bad "an unrelated port was closed"; fi
if wg show wg0 | grep -q "$(python3 -c "import json;print(json.load(open('/tmp/peer1.json'))['peer']['public_key'])")"; then
	bad "the deleted peer is still on wg0"
else
	ok "the deleted peer is off wg0"
fi

step "lockout after five bad tokens"
for i in 1 2 3 4 5; do
	code=$(curl -sS --cacert /tmp/agent.pem -H 'Authorization: Bearer wrong' -o /dev/null -w '%{http_code}' "$API/v1/status")
	[ "$code" = "401" ] || bad "attempt $i returned $code, expected 401"
done
ok "five wrong tokens each returned 401"
out=$(curl -sS --cacert /tmp/agent.pem -H 'Authorization: Bearer wrong' -o /dev/null -D - -w '%{http_code}' "$API/v1/status")
if echo "$out" | grep -q '429'; then ok "the sixth attempt is locked out (429)"; else bad "no lockout after five failures"; fi
if echo "$out" | grep -qi 'retry-after: 900'; then ok "Retry-After: 900 sent"; else bad "Retry-After header missing or wrong"; fi
code=$(c -o /dev/null -w '%{http_code}' "$API/v1/status")
if [ "$code" = "429" ]; then
	ok "the correct token is refused while locked out"
else
	bad "lockout bypassed by the correct token ($code)"
fi

step "token rotate"
kill $AGENT_PID 2>/dev/null; wait $AGENT_PID 2>/dev/null
/usr/local/bin/autoproxy-agent run > /tmp/agent2.log 2>&1 &
AGENT_PID=$!
for _ in $(seq 1 40); do sleep 0.25; ss -lnt 2>/dev/null | grep -q ":$APIPORT" && break; done
NEW=$(c -X POST "$API/v1/token/rotate" | jget token)
if [ ${#NEW} -eq 64 ]; then ok "rotate returned a fresh 32-byte token"; else bad "rotate token length ${#NEW}"; fi
if grep -q "AUTOPROXY_TOKEN=$NEW" /etc/autoproxy/agent.env; then
	ok "the new token is persisted to agent.env"
else
	bad "the new token was not persisted"
fi
code=$(c -o /dev/null -w '%{http_code}' "$API/v1/status")
if [ "$code" = "401" ]; then ok "the old token stopped working"; else bad "the old token still works ($code)"; fi
code=$(curl -sS --cacert /tmp/agent.pem -H "Authorization: Bearer $NEW" -o /dev/null -w '%{http_code}' "$API/v1/status")
if [ "$code" = "200" ]; then ok "the new token works"; else bad "the new token does not work ($code)"; fi

step "restart re-applies peers and rules from state"
kill $AGENT_PID 2>/dev/null; wait $AGENT_PID 2>/dev/null
ip link delete wg0 2>/dev/null
wg-quick up wg0 > /dev/null 2>&1
if wg show wg0 | grep -q 'peer:'; then
	bad "wg0 came up with peers from wg0.conf (it must not)"
else
	ok "a fresh wg0 has no peers until the agent restores them"
fi
/usr/local/bin/autoproxy-agent run > /tmp/agent3.log 2>&1 &
AGENT_PID=$!
for _ in $(seq 1 40); do sleep 0.25; ss -lnt 2>/dev/null | grep -q ":$APIPORT" && break; done
if wg show wg0 | grep -q 'peer:'; then ok "the agent restored the peer onto a fresh wg0"; else bad "peers were not restored"; fi
# Tearing down wg0 drops every route that pointed at it, in any table, and
# leaves the ip rule behind (it names a mark and a table, not a device). This
# is the reboot path: the agent has to put both back without help.
ip rule show > /tmp/iprule2.txt 2>&1
ip route show table 201 > /tmp/route201c.txt 2>/dev/null
if grep -qE 'not .*fwmark 0x2b lookup 201' /tmp/iprule2.txt; then
	ok "the peer-routing ip rule is in place after a restart"
else
	bad "the ip rule was lost across a restart"; cat /tmp/iprule2.txt
fi
if grep -q '10.66.66' /tmp/route201c.txt; then
	ok "peer routes restored into table 201 after a restart"
else
	bad "peer routes were not restored into table 201"; cat /tmp/route201c.txt
fi
wg show wg0 fwmark > /tmp/fwmark2.txt 2>&1
if grep -q '0x2b' /tmp/fwmark2.txt; then
	ok "the fwmark is back on a freshly created wg0"
else
	bad "wg0 came back without the fwmark ('$(cat /tmp/fwmark2.txt)')"
fi
if nft list ruleset | grep -q '27015'; then
	ok "rules restored after restart"
else
	bad "rules not restored"
	echo "--- rules.json ---"; cat /var/lib/autoproxy/rules.json 2>&1
	echo "--- agent log ---"; tail -20 /tmp/agent3.log
	echo "--- rules.nft on disk ---"; head -30 /etc/autoproxy/rules.nft 2>&1
	echo "--- tables ---"; nft list tables 2>&1
	echo "--- autoproxy_rules ---"; nft list table inet autoproxy_rules 2>&1 | head -25
	echo "--- rules.json ---"; cat /var/lib/autoproxy/rules.json 2>&1
fi

step "uninstall"
kill $AGENT_PID 2>/dev/null; wait $AGENT_PID 2>/dev/null
/usr/local/bin/autoproxy-agent uninstall --yes > /tmp/uninstall.log 2>&1 || bad "uninstall exited non-zero"
for f in /etc/autoproxy /var/lib/autoproxy /etc/systemd/system/autoproxy-agent.service /usr/local/bin/autoproxy-agent /etc/wireguard/wg0.conf; do
	if [ -e "$f" ]; then bad "uninstall left $f behind"; fi
done
ok "uninstall removed every file it created"
if nft list ruleset | grep -qE 'table inet autoproxy_(base|rules)'; then
	bad "uninstall left an nftables table"
else
	ok "uninstall removed both nftables tables"
fi
if ip link show wg0 > /dev/null 2>&1; then bad "uninstall left wg0 up"; else ok "uninstall removed wg0"; fi
# The ip rule outlives the interface, so "uninstall leaves no trace" has to
# name it explicitly or it stays behind forever.
ip rule show > /tmp/iprule3.txt 2>&1
if grep -qE 'not .*fwmark 0x2b lookup 201' /tmp/iprule3.txt; then
	bad "uninstall left the peer-routing ip rule behind"; cat /tmp/iprule3.txt
else
	ok "uninstall removed the peer-routing ip rule"
fi
# An empty table is not a table at all to the kernel: "ip route show table
# 201" then prints "FIB table does not exist" on stderr and nothing on stdout.
# That IS the state uninstall must leave behind, so stderr is discarded and
# only stdout is judged.
ip route show table 201 > /tmp/route201d.txt 2>/dev/null
if [ -s /tmp/route201d.txt ]; then
	bad "uninstall left routes in table 201"; cat /tmp/route201d.txt
else
	ok "routing table 201 is empty after uninstall"
fi
if grep -q 'somebody elses firewall' /etc/nftables.conf 2>/dev/null; then
	ok "the operator's original firewall was restored"
else
	bad "the original firewall was not restored"
fi
if command -v wg > /dev/null; then ok "packages left installed, as documented"; else bad "uninstall removed packages"; fi

printf '\n==================== %d passed, %d failed ====================\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
