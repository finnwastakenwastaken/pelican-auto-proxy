# Running the node client under Coolify

If your node host is already managed by Coolify, you can run the Docker flavour of the client as a Coolify resource
instead of a plain `docker compose` invocation. The join code and image are the same as
[install-client.md](install-client.md) describes — this page only covers the Coolify-specific clicks.

## What to click

1. **New Resource → Docker Compose**, on the server that actually hosts the game server (real mode) or the LAN
   gateway (site mode) — Coolify resources run on whichever server you assign them to, so pick the one matching the
   layout you want (see [layouts.md](layouts.md)).
2. Paste the compose snippet from the plugin's Setup step 2 (Docker tab), or the shape shown in
   [install-client.md](install-client.md).
3. Set the environment variable Coolify asks for: `AUTOPROXY_JOIN_CODE`, value = the join code from the plugin.
   Set it through Coolify's own environment variable field, not hard-coded in the compose file, so it isn't
   committed anywhere if this compose file ever ends up in a repo.
4. Confirm the compose file keeps `network_mode: host` and `cap_add: [NET_ADMIN]`. Both are required, not optional:
   the tunnel interface and its firewall rules need to live in the host's own network namespace so a Coolify
   redeploy doesn't drop the tunnel or disconnect players (the interface and its connection tracking live in the
   kernel, not in the container). Coolify does not add `sysctls:` for `network_mode: host` compose services — leave
   that block out; the client sets what it needs itself once it's running with `NET_ADMIN`.
5. Deploy.

If Coolify refuses `network_mode: host` or `cap_add` for a particular resource type or Coolify version, fall back
to a plain `docker compose up -d` on that server directly, outside Coolify's management, for this one container.

## Healthcheck

Add a healthcheck so Coolify (and its own dashboard) can tell a stalled tunnel from a running one, rather than just
"container is up":

```yaml
    healthcheck:
      test: ["CMD", "autoproxy-client", "status"]
      interval: 30s
      timeout: 5s
      start_period: 30s
      retries: 3
```

`autoproxy-client status` exits non-zero if the WireGuard handshake is missing or too old, or if the expected
firewall rules aren't present — the same check described in
[install-client.md](install-client.md#how-to-verify). Its exit code is what the healthcheck reads; the output it
prints is only there for a human running it by hand. (The client's own image already declares this healthcheck, so
you only need the block above if you paste the compose snippet without it.)
