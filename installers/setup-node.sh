#!/usr/bin/env bash
# Prepares a fresh Debian 12/13 machine to become a Pelican Wings node:
# Docker, the Wings binary and its systemd unit (enabled, not started, because
# there is no config yet), an HTTPS certificate for the node's hostname with
# automatic renewal, and printed instructions for the panel.
#
# It does NOT install the Auto Proxy tunnel client. That is install-client.sh,
# and this script only prints when and how to run it.
#
# Usage:
#   sudo ./setup-node.sh                 ask a few questions, then do it
#   sudo ./setup-node.sh --dry-run       print every action, change nothing
#   sudo ./setup-node.sh --yes           no questions; answers come from flags/env
#   ./setup-node.sh --print-certbot-command
#                                        print the certbot command that would run
#
# Flags (each one also has an environment variable, for scripting):
#   --hostname <fqdn>        AUTOPROXY_NODE_HOSTNAME=node1.example.com
#   --placement local|remote AUTOPROXY_NODE_PLACEMENT=local|remote
#   --cert cloudflare|http|existing
#                            AUTOPROXY_NODE_CERT=cloudflare|http|existing
#   --cert-email <address>   AUTOPROXY_NODE_CERT_EMAIL (empty: register without one)
#   --fullchain <path>       AUTOPROXY_NODE_CERT_FULLCHAIN (only with --cert existing)
#   --key <path>             AUTOPROXY_NODE_CERT_KEY       (only with --cert existing)
#   --proxied yes|no         AUTOPROXY_NODE_PROXIED=yes|no (Auto Proxy for game ports)
#   --join-command <cmd>     AUTOPROXY_JOIN_COMMAND: run the plugin's join command at the end
#                            CLOUDFLARE_API_TOKEN: Zone:DNS:Edit token for the zone
#                            AUTOPROXY_SKIP_DOCKER=1: skip the Docker install (tests only)
#
# Running it twice is safe: anything already present is reported and left alone.

set -euo pipefail

REPO="finnwastakenwastaken/pelican-auto-proxy"
WINGS_REPO="pelican-dev/wings"
INSTALL_CLIENT_URL="https://github.com/${REPO}/releases/latest/download/install-client.sh"
CF_CREDENTIALS="/etc/letsencrypt/cloudflare.ini"
DEPLOY_HOOK="/etc/letsencrypt/renewal-hooks/deploy/wings-reload.sh"
WINGS_BIN="/usr/local/bin/wings"
WINGS_UNIT="/etc/systemd/system/wings.service"
PELICAN_DIR="/etc/pelican"
PELICAN_CONFIG="${PELICAN_DIR}/config.yml"

DRY_RUN=0
ASSUME_YES=0
PRINT_CERTBOT=0
CHANGES_MADE=0

NODE_HOSTNAME="${AUTOPROXY_NODE_HOSTNAME:-}"
NODE_PLACEMENT="${AUTOPROXY_NODE_PLACEMENT:-}"
NODE_CERT="${AUTOPROXY_NODE_CERT:-}"
CERT_EMAIL="${AUTOPROXY_NODE_CERT_EMAIL:-}"
CERT_FULLCHAIN="${AUTOPROXY_NODE_CERT_FULLCHAIN:-}"
CERT_KEY="${AUTOPROXY_NODE_CERT_KEY:-}"
NODE_PROXIED="${AUTOPROXY_NODE_PROXIED:-}"
JOIN_COMMAND="${AUTOPROXY_JOIN_COMMAND:-}"

# --print-certbot-command must print one line and nothing else, so the
# explanations are suppressed for it. Errors still go to stderr.
log() { [[ "$PRINT_CERTBOT" -eq 1 ]] || printf '[setup-node] %s\n' "$*"; }
err() { printf '[setup-node] ERROR: %s\n' "$*" >&2; }
say() { [[ "$PRINT_CERTBOT" -eq 1 ]] || printf '%s\n' "$*"; }
rule() { [[ "$PRINT_CERTBOT" -eq 1 ]] || printf '%s\n' "------------------------------------------------------------"; }

usage() {
    sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'
}

# Runs a command, or prints it when --dry-run is set. Every change to the
# machine goes through run() or write_file(), so --dry-run is complete by
# construction rather than by remembering to add a branch.
run() {
    if [[ "$DRY_RUN" -eq 1 ]]; then
        printf '  would run: %s\n' "$*"
        return 0
    fi
    CHANGES_MADE=1
    "$@"
}

# Wraps systemctl so a container without systemd (used by the tests) reports
# the step instead of dying on "systemctl: command not found". A real node
# always has systemd, so this never hides anything there.
sctl() {
    if command -v systemctl >/dev/null 2>&1; then
        run systemctl "$@"
    else
        log "no systemd on this machine, skipped: systemctl $*"
    fi
}

# write_file <path> <mode> <<<content on stdin>
write_file() {
    local path="$1" mode="$2" content
    content="$(cat)"
    if [[ "$DRY_RUN" -eq 1 ]]; then
        printf '  would write: %s (mode %s, %s lines)\n' "$path" "$mode" "$(printf '%s\n' "$content" | wc -l)"
        return 0
    fi
    # Rewriting a file with the same bytes still counts as a change to
    # anything watching mtimes, and it would make a second run report work it
    # did not do. Identical content is left alone.
    if [[ -f "$path" && "$(cat "$path")" == "$content" ]]; then
        log "${path} is already correct, nothing to do"
        chmod "$mode" "$path"
        return 0
    fi
    CHANGES_MADE=1
    install -d -m 0755 "$(dirname "$path")"
    printf '%s\n' "$content" > "$path"
    chmod "$mode" "$path"
}

# ---------------------------------------------------------------- arguments

while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run) DRY_RUN=1 ;;
        --yes|-y) ASSUME_YES=1 ;;
        --print-certbot-command) PRINT_CERTBOT=1 ;;
        --hostname) NODE_HOSTNAME="${2:-}"; shift ;;
        --placement) NODE_PLACEMENT="${2:-}"; shift ;;
        --cert) NODE_CERT="${2:-}"; shift ;;
        --cert-email) CERT_EMAIL="${2:-}"; shift ;;
        --fullchain) CERT_FULLCHAIN="${2:-}"; shift ;;
        --key) CERT_KEY="${2:-}"; shift ;;
        --proxied) NODE_PROXIED="${2:-}"; shift ;;
        --join-command) JOIN_COMMAND="${2:-}"; shift ;;
        -h|--help) usage; exit 0 ;;
        *) err "unknown option: $1"; usage >&2; exit 1 ;;
    esac
    shift
done

# --print-certbot-command answers a question about arguments, so it needs no
# root and touches nothing. Everything else does need root.
if [[ "$(id -u)" -ne 0 && "$PRINT_CERTBOT" -ne 1 && "$DRY_RUN" -ne 1 ]]; then
    err "must run as root (sudo)"
    exit 1
fi

[[ -r /etc/os-release ]] || { err "cannot read /etc/os-release; unsupported system"; exit 1; }
# shellcheck disable=SC1091
source /etc/os-release
case "${ID:-}:${VERSION_ID:-}" in
    debian:12|debian:13)
        : ;;
    ubuntu:*)
        err "Ubuntu is not supported in this release; Debian 12 or 13 only. Ubuntu support is tracked for a later release."
        exit 1
        ;;
    *)
        err "unsupported OS: ${PRETTY_NAME:-${ID:-} ${VERSION_ID:-}}. Supported: Debian 12/13."
        exit 1
        ;;
esac

INTERACTIVE=1
if [[ "$ASSUME_YES" -eq 1 || ! -t 0 ]]; then
    INTERACTIVE=0
fi

# ------------------------------------------------------------- facts first

valid_hostname() {
    local h="$1"
    [[ ${#h} -le 253 ]] || return 1
    [[ "$h" == *.* ]] || return 1
    [[ "$h" =~ ^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)+$ ]]
}

resolve_host() {
    local h="$1"
    if command -v dig >/dev/null 2>&1; then
        dig +short +time=3 +tries=1 A "$h" 2>/dev/null | grep -E '^[0-9.]+$' | sort -u || true
    else
        getent ahostsv4 "$h" 2>/dev/null | awk '{print $1}' | sort -u || true
    fi
}

is_rfc1918() {
    case "$1" in
        10.*|192.168.*|127.*|169.254.*) return 0 ;;
        172.1[6-9].*|172.2[0-9].*|172.3[01].*) return 0 ;;
        100.6[4-9].*|100.[7-9][0-9].*|100.1[01][0-9].*|100.12[0-7].*) return 0 ;;
        *) return 1 ;;
    esac
}

LOCAL_IP="$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p' | head -1 || true)"
# A minimal image may have no iproute2 yet; hostname -I is the fallback so the
# local/remote hint is not silently wrong on a fresh machine.
if [[ -z "$LOCAL_IP" ]]; then
    LOCAL_IP="$(hostname -I 2>/dev/null | awk '{print $1}' || true)"
fi
PUBLIC_IP="$(curl -4 -fsS --max-time 8 https://api.ipify.org 2>/dev/null || true)"
MEM_MIB="$(awk '/^MemTotal:/ {print int($2/1024)}' /proc/meminfo 2>/dev/null || echo 0)"
DISK_MIB="$(df -BM --output=avail / 2>/dev/null | tail -1 | tr -dc '0-9' || echo 0)"
ARCH_RAW="$(uname -m)"
case "$ARCH_RAW" in
    x86_64) WINGS_ARCH="amd64" ;;
    aarch64|arm64) WINGS_ARCH="arm64" ;;
    *) err "unsupported CPU architecture: ${ARCH_RAW}. Wings ships amd64 and arm64 builds only."; exit 1 ;;
esac

DOCKER_PRESENT=0
command -v docker >/dev/null 2>&1 && DOCKER_PRESENT=1
WINGS_PRESENT=0
[[ -x "$WINGS_BIN" ]] && WINGS_PRESENT=1
WINGS_CONFIGURED=0
[[ -f "$PELICAN_CONFIG" ]] && WINGS_CONFIGURED=1

# ------------------------------------------------------------- the questions

ask() {
    # ask <prompt> <default>; echoes the answer
    local prompt="$1" default="$2" reply=""
    if [[ "$INTERACTIVE" -eq 0 ]]; then
        printf '%s' "$default"
        return 0
    fi
    read -r -p "$prompt [$default]: " reply </dev/tty || true
    printf '%s' "${reply:-$default}"
}

ask_choice() {
    # ask_choice <prompt> <default> <option>...
    local prompt="$1" default="$2"; shift 2
    local options=("$@") reply="" opt
    while :; do
        reply="$(ask "$prompt ($(IFS='/'; printf '%s' "${options[*]}"))" "$default")"
        for opt in "${options[@]}"; do
            if [[ "$reply" == "$opt" ]]; then printf '%s' "$opt"; return 0; fi
        done
        if [[ "$INTERACTIVE" -eq 0 ]]; then
            err "invalid value '${reply}'; expected one of: ${options[*]}"
            exit 1
        fi
        say "Please answer with one of: ${options[*]}"
    done
}

say ""
rule
say " Pelican Wings node setup"
rule
say "This prepares this machine to run game servers for a Pelican Panel."
say "It installs Docker and Wings, gets an HTTPS certificate for this node,"
say "and prints exactly what to type in the panel afterwards."
[[ "$DRY_RUN" -eq 1 ]] && say "DRY RUN: nothing on this machine will be changed."
say ""

# --- 1. hostname ------------------------------------------------------------
say "1) Node hostname."
say "   The panel talks to this machine by name, and the certificate is issued"
say "   for that name. Without a name of its own the panel cannot verify it."
default_hostname="$NODE_HOSTNAME"
if [[ -z "$default_hostname" ]]; then
    candidate="$(hostname -f 2>/dev/null || true)"
    if valid_hostname "$candidate"; then default_hostname="$candidate"; fi
fi
while :; do
    NODE_HOSTNAME="$(ask "   Hostname for this node" "$default_hostname")"
    if valid_hostname "$NODE_HOSTNAME"; then break; fi
    err "'${NODE_HOSTNAME}' is not a valid hostname. Use a full name with a dot in it, like node1.example.com."
    [[ "$INTERACTIVE" -eq 1 ]] || exit 1
done

RESOLVED="$(resolve_host "$NODE_HOSTNAME" | tr '\n' ' ' | sed 's/ *$//')"
say ""
if [[ -z "$RESOLVED" ]]; then
    say "   Right now ${NODE_HOSTNAME} does not resolve to anything. The panel will"
    say "   not reach this node until you add the DNS record described below."
else
    say "   Right now ${NODE_HOSTNAME} resolves to: ${RESOLVED}"
    if [[ -n "$PUBLIC_IP" && " $RESOLVED " == *" $PUBLIC_IP "* ]]; then
        say "   That is this machine's own public address, so the name already points here."
    elif [[ -n "$PUBLIC_IP" ]]; then
        say "   This machine's public address is ${PUBLIC_IP}, which is not in that list."
        say "   That is correct for a node behind the Auto Proxy VPS, and wrong for a"
        say "   rented server that players should reach directly."
    fi
fi
say ""

# --- 2. placement -----------------------------------------------------------
say "2) Where is this machine?"
say "   LOCAL:  same network as the panel, no public address of its own (home or office)."
say "   REMOTE: a rented server with its own public address."
placement_hint="remote"
hint_reason="this machine has a public address of its own"
if [[ -n "$LOCAL_IP" ]] && is_rfc1918 "$LOCAL_IP"; then
    placement_hint="local"
    hint_reason="this machine's own address (${LOCAL_IP}) is a private one"
fi
if [[ -n "$PUBLIC_IP" && -n "$LOCAL_IP" && "$PUBLIC_IP" == "$LOCAL_IP" ]]; then
    placement_hint="remote"
    hint_reason="this machine's address ${LOCAL_IP} is itself a public address"
fi
say "   Detected: ${placement_hint} (${hint_reason})."
if [[ -n "$NODE_PLACEMENT" ]]; then
    placement_hint="$NODE_PLACEMENT"
fi
NODE_PLACEMENT="$(ask_choice "   Placement" "$placement_hint" local remote)"
say ""

# --- 3. certificate ---------------------------------------------------------
say "3) How should this node get its HTTPS certificate?"
say "   cloudflare: your DNS is at Cloudflare. Works local and remote, nothing"
say "               has to be reachable from the internet. Needs an API token"
say "               with Zone:DNS:Edit for this zone."
say "   http:       remote machines only. Let's Encrypt connects to port 80 on"
say "               this machine to check the name, so port 80 must be open."
say "   existing:   you already have a certificate and will give its paths."
cert_default="${NODE_CERT:-cloudflare}"
while :; do
    NODE_CERT="$(ask_choice "   Certificate method" "$cert_default" cloudflare http existing)"
    if [[ "$NODE_CERT" == "http" && "$NODE_PLACEMENT" == "local" ]]; then
        err "the HTTP challenge cannot work on a local node: Let's Encrypt has to open a connection to port 80 on this machine from the internet, and a machine with no public address of its own cannot be reached that way. Use cloudflare, or a certificate you already have."
        [[ "$INTERACTIVE" -eq 1 ]] || exit 1
        cert_default="cloudflare"
        continue
    fi
    break
done

CF_TOKEN="${CLOUDFLARE_API_TOKEN:-}"
if [[ "$NODE_CERT" == "cloudflare" && -z "$CF_TOKEN" && "$PRINT_CERTBOT" -ne 1 ]]; then
    if [[ -f "$CF_CREDENTIALS" ]]; then
        say "   A Cloudflare token file already exists at ${CF_CREDENTIALS}; it will be reused."
    elif [[ "$INTERACTIVE" -eq 1 ]]; then
        say "   Paste a Cloudflare API token with Zone:DNS:Edit for this zone."
        say "   It is not shown as you type and is written to ${CF_CREDENTIALS}, readable by root only."
        read -r -s -p "   Cloudflare API token: " CF_TOKEN </dev/tty || true
        say ""
    else
        err "CLOUDFLARE_API_TOKEN is not set and there is no ${CF_CREDENTIALS}; cannot issue a certificate with the Cloudflare method."
        exit 1
    fi
fi

if [[ "$NODE_CERT" == "existing" ]]; then
    CERT_FULLCHAIN="$(ask "   Path to the full chain certificate" "${CERT_FULLCHAIN:-/etc/letsencrypt/live/${NODE_HOSTNAME}/fullchain.pem}")"
    CERT_KEY="$(ask "   Path to the private key" "${CERT_KEY:-/etc/letsencrypt/live/${NODE_HOSTNAME}/privkey.pem}")"
else
    CERT_FULLCHAIN="/etc/letsencrypt/live/${NODE_HOSTNAME}/fullchain.pem"
    CERT_KEY="/etc/letsencrypt/live/${NODE_HOSTNAME}/privkey.pem"
fi
say ""

# --- 4. auto proxy ----------------------------------------------------------
say "4) Auto Proxy for this node's game ports?"
say "   yes: players connect to the Auto Proxy VPS and nothing has to be opened here."
say "   no:  players connect to this machine directly on its own public address."
proxied_default="no"
[[ "$NODE_PLACEMENT" == "local" ]] && proxied_default="yes"
[[ -n "$NODE_PROXIED" ]] && proxied_default="$NODE_PROXIED"
NODE_PROXIED="$(ask_choice "   Use Auto Proxy for this node" "$proxied_default" yes no)"
say ""

# ------------------------------------------------- certbot command building

# Builds the certbot argument list into the global CERTBOT_CMD array. Kept as
# one function with no side effects so --print-certbot-command tests exactly
# the arguments a real run would use, and nothing else.
CERTBOT_CMD=()
build_certbot_cmd() {
    CERTBOT_CMD=(certbot certonly --non-interactive --agree-tos --keep-until-expiring --cert-name "$NODE_HOSTNAME" -d "$NODE_HOSTNAME")
    if [[ -n "$CERT_EMAIL" ]]; then
        CERTBOT_CMD+=(--email "$CERT_EMAIL")
    else
        CERTBOT_CMD+=(--register-unsafely-without-email)
    fi
    case "$NODE_CERT" in
        cloudflare)
            CERTBOT_CMD+=(--dns-cloudflare --dns-cloudflare-credentials "$CF_CREDENTIALS" --dns-cloudflare-propagation-seconds 30)
            ;;
        http)
            CERTBOT_CMD+=(--standalone --preferred-challenges http)
            ;;
        existing)
            CERTBOT_CMD=()
            ;;
    esac
}

build_certbot_cmd
if [[ "$PRINT_CERTBOT" -eq 1 ]]; then
    if [[ ${#CERTBOT_CMD[@]} -eq 0 ]]; then
        say "certbot is not used with --cert existing"
    else
        printf '%q ' "${CERTBOT_CMD[@]}" | sed 's/ $//'
        printf '\n'
    fi
    exit 0
fi

# Is this the exact join command the plugin's Setup page prints? A pasted line
# is untrusted text, so anything else is refused rather than run.
looks_like_join_command() {
    local candidate="$1" pattern
    pattern="^curl -fsSL ${INSTALL_CLIENT_URL//./[.]} [|] sudo bash -s -- [A-Za-z0-9._:-]+$"
    [[ "$candidate" =~ $pattern ]]
}

# ------------------------------------------------------------------ the plan

rule
say " Plan"
rule
say "  hostname:        ${NODE_HOSTNAME}"
say "  placement:       ${NODE_PLACEMENT}"
say "  certificate:     ${NODE_CERT}"
say "  cert path:       ${CERT_FULLCHAIN}"
say "  key path:        ${CERT_KEY}"
say "  auto proxy:      ${NODE_PROXIED}"
say "  architecture:    ${WINGS_ARCH}"
if [[ "$DOCKER_PRESENT" -eq 1 ]]; then
    say "  docker:          already installed, will be left alone"
elif [[ "${AUTOPROXY_SKIP_DOCKER:-}" == "1" ]]; then
    say "  docker:          skipped (AUTOPROXY_SKIP_DOCKER=1)"
else
    say "  docker:          install Docker CE from Docker's apt repository"
fi
if [[ "$WINGS_PRESENT" -eq 1 ]]; then
    say "  wings:           already installed, will be left alone"
else
    say "  wings:           download the latest release and install the systemd unit"
fi
if [[ "$WINGS_CONFIGURED" -eq 1 ]]; then
    say "  wings config:    ${PELICAN_CONFIG} exists and is never touched by this script"
fi
say "  renewal:         certbot.timer plus a deploy hook that restarts wings"
rule
say ""

if [[ "$DRY_RUN" -eq 0 && "$INTERACTIVE" -eq 1 ]]; then
    confirm="$(ask "Go ahead?" "yes")"
    case "$confirm" in
        y|Y|yes|YES) : ;;
        *) log "nothing was changed"; exit 0 ;;
    esac
fi

# ------------------------------------------------------------------- do it

apt_updated=0
apt_install() {
    if [[ "$apt_updated" -eq 0 ]]; then
        run env DEBIAN_FRONTEND=noninteractive apt-get update -qq
        apt_updated=1
    fi
    run env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "$@"
}

log "step 1/5: base packages"
# Checked by what they provide, not by package name: "dnsutils" is a
# transitional package on Debian 13, so a name check reinstalls it on every
# run and a second run stops being a no-op.
missing_base=()
command -v curl >/dev/null 2>&1 || missing_base+=(curl)
command -v gpg  >/dev/null 2>&1 || missing_base+=(gnupg)
command -v dig  >/dev/null 2>&1 || missing_base+=(dnsutils)
command -v tar  >/dev/null 2>&1 || missing_base+=(tar)
[[ -f /etc/ssl/certs/ca-certificates.crt ]] || missing_base+=(ca-certificates)
if [[ ${#missing_base[@]} -gt 0 ]]; then
    apt_install "${missing_base[@]}"
else
    log "base packages already present, nothing to do"
fi

log "step 2/5: Docker"
if [[ "${AUTOPROXY_SKIP_DOCKER:-}" == "1" ]]; then
    log "AUTOPROXY_SKIP_DOCKER=1, skipping the Docker install (tests only; a real node needs Docker)"
elif [[ "$DOCKER_PRESENT" -eq 1 ]]; then
    log "docker is already installed, nothing to do"
else
    run install -m 0755 -d /etc/apt/keyrings
    if [[ "$DRY_RUN" -eq 1 ]]; then
        printf '  would run: curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc\n'
    else
        CHANGES_MADE=1
        curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc
        chmod a+r /etc/apt/keyrings/docker.asc
    fi
    write_file /etc/apt/sources.list.d/docker.list 0644 <<EOF
deb [arch=${WINGS_ARCH} signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian ${VERSION_CODENAME:-bookworm} stable
EOF
    apt_updated=0
    apt_install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
    sctl enable --now docker
fi

log "step 3/5: Wings"
if [[ "$WINGS_PRESENT" -eq 1 ]]; then
    log "wings is already installed at ${WINGS_BIN}, nothing to do"
    [[ "$WINGS_CONFIGURED" -eq 1 ]] && log "${PELICAN_CONFIG} exists; this node is already configured and is left untouched"
else
    run install -d -m 0700 "$PELICAN_DIR"
    if [[ "$DRY_RUN" -eq 1 ]]; then
        printf '  would run: curl -fsSL -o %s https://github.com/%s/releases/latest/download/wings_linux_%s\n' "$WINGS_BIN" "$WINGS_REPO" "$WINGS_ARCH"
    else
        CHANGES_MADE=1
        curl -fsSL -o "$WINGS_BIN" "https://github.com/${WINGS_REPO}/releases/latest/download/wings_linux_${WINGS_ARCH}"
        chmod u+x "$WINGS_BIN"
    fi
fi

if [[ -f "$WINGS_UNIT" ]]; then
    log "systemd unit ${WINGS_UNIT} already exists, nothing to do"
else
    write_file "$WINGS_UNIT" 0644 <<'EOF'
[Unit]
Description=Wings Daemon
After=docker.service
Requires=docker.service
PartOf=docker.service

[Service]
User=root
WorkingDirectory=/etc/pelican
LimitNOFILE=4096
PIDFile=/var/run/wings/daemon.pid
ExecStart=/usr/local/bin/wings
Restart=on-failure
StartLimitInterval=180
StartLimitBurst=30
RestartSec=5s

[Install]
WantedBy=multi-user.target
EOF
    sctl daemon-reload
fi
# Enabled but never started: wings exits immediately without a config, and the
# config only exists after the panel's auto-deploy command has run.
if systemctl is-enabled wings >/dev/null 2>&1; then
    log "wings is already enabled at boot, nothing to do"
else
    sctl enable wings
fi

log "step 4/5: certificate"
if [[ "$NODE_CERT" == "existing" ]]; then
    log "using the certificate you named; nothing is issued"
    for f in "$CERT_FULLCHAIN" "$CERT_KEY"; do
        if [[ -f "$f" ]]; then
            log "found ${f}"
        else
            err "${f} does not exist. Wings will not start until that path holds a certificate."
        fi
    done
else
    certbot_pkgs=(certbot)
    [[ "$NODE_CERT" == "cloudflare" ]] && certbot_pkgs+=(python3-certbot-dns-cloudflare)
    missing_certbot=()
    for pkg in "${certbot_pkgs[@]}"; do
        dpkg-query -W -f='${Status}' "$pkg" 2>/dev/null | grep -q "ok installed" || missing_certbot+=("$pkg")
    done
    if [[ ${#missing_certbot[@]} -gt 0 ]]; then
        apt_install "${missing_certbot[@]}"
    else
        log "certbot packages already present, nothing to do"
    fi

    if [[ "$NODE_CERT" == "cloudflare" && -n "$CF_TOKEN" ]]; then
        # The token is never echoed and never passed on a command line.
        if [[ "$DRY_RUN" -eq 1 ]]; then
            printf '  would write: %s (mode 0600, the Cloudflare API token)\n' "$CF_CREDENTIALS"
        else
            CHANGES_MADE=1
            install -d -m 0755 /etc/letsencrypt
            ( umask 077; printf 'dns_cloudflare_api_token = %s\n' "$CF_TOKEN" > "$CF_CREDENTIALS" )
            chmod 0600 "$CF_CREDENTIALS"
            log "wrote ${CF_CREDENTIALS} (root only, not shown)"
        fi
    fi

    if [[ -f "$CERT_FULLCHAIN" ]]; then
        log "a certificate for ${NODE_HOSTNAME} already exists at ${CERT_FULLCHAIN}; certbot is asked to keep it until it is close to expiry"
    fi
    build_certbot_cmd
    run "${CERTBOT_CMD[@]}"
fi

log "step 5/5: automatic renewal"
write_file "$DEPLOY_HOOK" 0755 <<EOF
#!/usr/bin/env bash
# Installed by Pelican Auto Proxy setup-node.sh.
# Restarts wings after a renewal, and only when it was this node's
# certificate that renewed: certbot runs every deploy hook for every
# lineage, so an unrelated certificate must not bounce the game servers.
set -euo pipefail
node_hostname="${NODE_HOSTNAME}"
case " \${RENEWED_DOMAINS:-} " in
    *" \${node_hostname} "*) ;;
    *) exit 0 ;;
esac
systemctl try-restart wings
EOF

if systemctl is-active certbot.timer >/dev/null 2>&1; then
    log "certbot.timer is already active, nothing to do"
else
    sctl enable --now certbot.timer
fi
if [[ "$DRY_RUN" -eq 0 ]] && command -v systemctl >/dev/null 2>&1; then
    if systemctl is-active certbot.timer >/dev/null 2>&1; then
        log "verified: certbot.timer is active, renewals will run twice a day"
    else
        err "certbot.timer is not active. Certificates will expire in 90 days with no warning. Run: systemctl enable --now certbot.timer"
    fi
fi

# ------------------------------------------------------- what to do next

say ""
rule
say " What to enter in the panel"
rule
say "  Admin -> Nodes -> Create Node"
say "    Name:              anything you like"
say "    FQDN:              ${NODE_HOSTNAME}"
say "    Communicate over SSL:  yes"
say "    Daemon port:       8080"
say "    Daemon SFTP port:  2022"
say "    Memory:            ${MEM_MIB} MiB detected on this machine"
say "    Disk:              ${DISK_MIB} MiB free on / right now"
say ""
say "  Wings reads its certificate from:"
say "    ${CERT_FULLCHAIN}"
say "    ${CERT_KEY}"
say "  Those are the paths the panel's auto-deploy command writes into the node"
say "  config, so leave them as they are."
say ""
say "  After creating the node, open its Configuration tab, copy the auto-deploy"
say "  command, run it here, then start Wings:"
say "    systemctl start wings"
say ""
rule
say " DNS for ${NODE_HOSTNAME}"
rule
say "  If your DNS is at Cloudflare, the record must be DNS only (grey cloud)."
say "  With the orange cloud (proxied) this node does not work:"
say "    - SFTP on port 2022 fails, because Cloudflare only carries web ports."
say "    - Cloudflare's own certificate sits in front of Wings, so the panel"
say "      cannot verify the node."
say "    - Websocket consoles may break."
if [[ "$NODE_PLACEMENT" == "local" ]]; then
    say "  This is a LOCAL node, so it has no public address of its own."
    say "  Point ${NODE_HOSTNAME} at the Auto Proxy VPS's public address (grey cloud)."
    say "  After this node has joined, the panel admin adds two manual forwards on"
    say "  the Forwards page, both to \"A machine running a tunnel client\" (this node):"
    say "    TCP 8080  -> this node"
    say "    TCP 2022  -> this node"
    say "  Panel to node traffic then goes through the VPS, which is the only way"
    say "  in when the node has no public address."
else
    say "  This is a REMOTE node with its own public address."
    target_ip="${PUBLIC_IP:-the public address of this machine}"
    say "  Point ${NODE_HOSTNAME} at ${target_ip} (grey cloud)."
fi
say ""
rule
say " Auto Proxy for the game ports"
rule
if [[ "$NODE_PROXIED" == "yes" ]]; then
    say "  You said yes. Nothing for it is installed by this script."
    say "  1. In the panel: Admin -> Auto Proxy -> Setup, mark this node proxied."
    say "  2. Run the join command it shows, on this machine."
    if [[ "$NODE_PLACEMENT" == "remote" ]]; then
        say "  3. At your provider: one outbound rule, UDP to <VPS IP>:51820."
        say "     No inbound rules at all."
        say "  Hiding a rented server's address costs one extra network hop."
    fi
else
    say "  You said no. Players connect to this machine directly, so the game"
    say "  ports have to be open at your provider. You can change your mind later:"
    say "  mark the node proxied on the Setup page and run the join command."
fi
say ""

if [[ -n "$JOIN_COMMAND" ]]; then
    if looks_like_join_command "$JOIN_COMMAND"; then
        log "running the join command you pasted"
        run bash -c "$JOIN_COMMAND"
    else
        err "that does not look like the join command from the plugin's Setup page, so it was not run."
        err "Expected: curl -fsSL ${INSTALL_CLIENT_URL} | sudo bash -s -- <join code>"
    fi
elif [[ "$NODE_PROXIED" == "yes" && "$INTERACTIVE" -eq 1 && "$DRY_RUN" -eq 0 ]]; then
    say "If you already have the join command from the Setup page, paste it now."
    say "Leave it empty to skip and run it later."
    read -r -p "Join command: " pasted </dev/tty || true
    if [[ -n "${pasted:-}" ]]; then
        if looks_like_join_command "$pasted"; then
            run bash -c "$pasted"
        else
            err "that does not look like the join command from the plugin's Setup page, so it was not run."
        fi
    fi
fi

if [[ "$DRY_RUN" -eq 1 ]]; then
    log "dry run finished; nothing was changed"
elif [[ "$CHANGES_MADE" -eq 0 ]]; then
    log "everything was already in place; nothing changed"
else
    log "done. Full walkthrough: https://github.com/${REPO}/blob/main/docs/node-setup.md"
fi
