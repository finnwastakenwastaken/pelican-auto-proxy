# Testing

How this project is tested without touching anyone's production panel, node or VPS, followed by the one test plan
that does need real machines.

## Gates (CI, and runnable locally through Docker — never against live infrastructure)

| Gate | How |
|---|---|
| Go vet + tests | `golang:1.25-alpine` container, `go vet ./... && go test ./...` in `agent/`. |
| Golden nftables files | `nft -c -f` (check only, never load) against every file in `agent/internal/nft/testdata/*.nft`, in a `debian:trixie` container with `--cap-add NET_ADMIN --network none` — network access is deliberately removed; only the nft syntax/semantics checker runs. |
| Shell | `shellcheck` (koalaman/shellcheck:stable) and `bash -n` on every script, including `client/autoproxy-client` and everything under `installers/` and `scripts/`. |
| PHP | `php -l` on every plugin file; `php test/rules-test.php` (pure logic, no Laravel needed). |
| Infra sweep | `scripts/infra-sweep.sh` against the tree, and again against the built plugin zip and release assets — catches real IPs, hostnames, names and secret-shaped strings before they can leave this repo. |
| Workflow lint | `actionlint` (via `docker run --rm -v $PWD:/repo -w /repo rhysd/actionlint:latest`) against both workflow files. |

Every gate here must be seen failing once before it's trusted: break it on purpose, confirm the failure looks like
what a real regression would produce, then fix it back. A gate that has never failed might just never run.

## Fake agent

`test/fake-agent.py` stands in for the real VPS API and serves the whole v1 surface: `GET /v1/status`,
`GET|PUT /v1/rules`, `GET|POST /v1/peers`, `DELETE /v1/peers/{id}`, `POST /v1/peers/{id}/rotate` and
`POST /v1/token/rotate`, including join codes, the same bearer-token requirement, the same reserved-port and
validation rejections, and the per-IP lockout after five bad tokens. `--tls` makes it generate a self-signed
certificate with an IP SAN and print the PEM and the public-key pin, which is what the plugin needs: the plugin
only ever speaks HTTPS. It lets the plugin be developed and its own test suite run without any VPS at all.

## Throwaway panel

The plugin is exercised end to end against a disposable Pelican panel container, never a real one:

```bash
docker run -d --name autoproxy-devpanel -e APP_URL=http://localhost:8085 -e APP_ENV=local \
  -e APP_INSTALLED=false -e DB_CONNECTION=sqlite ghcr.io/pelican/panel:latest
docker exec autoproxy-devpanel sh -c 'touch /pelican-data/database/database.sqlite && php artisan migrate --force'
# unzip the built autoproxy-<version>.zip into /var/www/html/plugins/autoproxy, then:
docker exec autoproxy-devpanel php artisan p:plugin:install autoproxy
docker exec -d autoproxy-devpanel python3 /tmp/fake-agent.py --tls --host 127.0.0.1 --port 17443 --token devtoken
docker exec autoproxy-devpanel php artisan autoproxy:sync
docker rm -f autoproxy-devpanel
```

This is how Setup → peer join-code rendering → sync (with real-mode and site-mode targets) is checked before any
live test. The panel image's actual PHP and Filament versions should be re-confirmed against whatever the plugin is
currently written against — that pairing drifts as Pelican itself updates.

## Installing a build that has not been released

Both installers have an escape hatch so a container or VM test can install the binary you just built, instead of
downloading a release that does not exist yet. Both skip the checksum gate, and both say so in their output:

| Variable | Installer | Effect |
|---|---|---|
| `AUTOPROXY_LOCAL_BINARY` | `install-vps.sh` | Install this local file as `autoproxy-agent`. No download, no checksum check. |
| `AUTOPROXY_LOCAL_TARBALL` | `install-client.sh` | Use this local `autoproxy-client.tar.gz`. A `<tarball>.sha256` next to it is used if present; without one, the installer refuses, because it will not extract a tarball it cannot verify. |
| `AUTOPROXY_VERSION` | both | Pin a release tag instead of resolving `latest`. This keeps the checksum check. |

Because the first two bypass verification, they belong in tests and air-gapped installs only — never in an
instruction given to a user.

## Agent against real nftables

The agent binary is run against a genuine nftables stack, but never on the development machine itself: a throwaway
Debian 13 container with `NET_ADMIN` and `--network none`, so the ruleset is real but has nowhere to actually route
traffic. This is where the golden-file outputs are produced and where the atomic apply/rollback behaviour (a
rejected ruleset leaving the previous one live) is checked in practice, not just in the Go test suite.

## Live test plan (needs a real VPS and two VMs — never the maintainer's own production systems)

Live proof only happens against machines set aside specifically for this: one VPS that can be freely wiped and
reinstalled, and two fresh VMs (one Debian 12, one Debian 13, 2 vCPU / 4 GB / 30 GB is enough, root access,
internet-reachable, on a network that can reach the test VPS). This release is Debian-only: Debian 13 has a passed
live run; Debian 12 is expected to work but has not had one yet — say exactly that until it does. Ubuntu is a
follow-up, not covered by this matrix.

### Checklist

1. **Fresh VPS, from the release URL, with a stopwatch.** Reinstall the OS, then run `install-vps.sh` exactly as a
   stranger would from the README, timing it. Confirm `autoproxy-agent show-code` and `wg show wg0` both look
   right afterwards (`wg0` is the VPS's interface; the node hosts use `autoproxy0`). Check the setup output's own
   verification block too: `ip_forward = 1`, both `inet autoproxy_base` and `inet autoproxy_rules` loaded, `wg0`
   up, unit active.
2. **VM 1 — panel + node on one box (layout 1).** Fresh Pelican panel and Wings on the same VM, install the plugin
   zip, complete Setup, run the generated join command, real mode. Create a test server (a UDP echo egg, or a
   small real game) and confirm — from a machine that is neither the VPS nor this VM — that the server sees this
   machine's own public IP as the player address, not the VPS's and not the tunnel's internal address. Restart the
   client and reboot the VM; confirm the tunnel and forwarding both recover without re-running install.
3. **VM 2 — a second, remote node (layout 3).** Wings only, added as a second Pelican node pointed at VM 1's panel;
   a second peer, same checks as VM 1. Try the Docker flavour of the client here specifically, since VM 1 already
   covers the system-service flavour.
4. **Site mode (layout 4).** A manual forward from the VPS through VM 1's client to a third address on that VM's
   own LAN (or a third VM if one is available); confirm the shared-IP behaviour is what actually happens, not just
   what the design predicts.
5. **Negative gates** — these should fail, and failing correctly is the pass condition:
   - A wrong API token from the plugin locks out after five attempts.
   - Regenerating a node's join code (peer rotate) kills the old one: the host still running with the previous
     code loses its handshake within a keepalive or two, and recovers only after the installer is re-run with the
     new code. (Join codes are deliberately *not* single-use — nothing consumes one — so this rotate is the only
     thing that revokes one; prove it actually does.)
   - Setup on a VPS whose `/etc/nftables.conf` has been edited by hand refuses without `--yes`, says so, and leaves
     both that file and the agent's own table alone; with `--yes` it replaces it and leaves a readable copy at
     `/etc/nftables.conf.pre-autoproxy`.
   - Deleting a peer closes the ports that targeted it — check from outside, not just that the API call succeeded.
   - `autoproxy-client uninstall` leaves no trace: `nft list ruleset`, `ip rule`, and `systemctl list-units` all
     come back clean afterwards.
6. **The about-five-minute run.** Someone who has not been building this follows only the README, on a freshly
   re-imaged VM, with a stopwatch. This is where the "about five minutes" claim in the docs comes from — a measured
   timing on a clean test setup, not a first-time user's clock. Every point where they stumble, hesitate, or need to ask a
   question is a documentation bug, not a user error — fix the doc, not just their understanding.

### What's expected to only be settled here, not by design review

- Real-IP reply routing on a container-publishing Wings host (the `type route` output-hook restore chain).
- Whether the test VPS's provider passes UDP unfiltered end to end (test with the echo target in step 1 before
  assuming anything about game traffic specifically).
- Whether the exact nftables/WireGuard versions on a given Debian point release support the concatenated DNAT map
  syntax the golden files use; if not, the documented fallback (one explicit rule per remapped port) needs to
  actually be exercised, not just described. (Ubuntu's package versions are out of scope until Ubuntu support
  returns.) Container gate on Debian 12, 2026-09-21: `scripts/vps-test` built with `--build-arg
  DEBIAN_CODENAME=bookworm` passed all 80 checks, same as Debian 13, with nftables 1.0.6 and wireguard-tools
  1.0.20210914; the 1000-rule map was accepted. One difference seen, not asserted on: nftables 1.0.6 prints the
  concatenated map as far fewer element lines than 1.1.3 does. A container shares the host's kernel and stubs
  systemd, so a real Debian 12 machine (its own kernel, real units, a real handshake) is still the only thing that
  settles this.
