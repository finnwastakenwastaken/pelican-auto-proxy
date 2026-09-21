# Migrating from frp or playit.gg

Do this alongside your existing setup, not instead of it — nothing here requires taking your current forwarding
down first, and doing it in this order means players never see a gap.

## 1. Install Auto Proxy alongside what you already have

Follow the [quick start](quickstart.md) on your VPS and node hosts as normal. Your existing frp or playit.gg tunnel
keeps running the whole time — they use different ports and a different public address, so the two don't conflict.

## 2. Test one server through the new address

Pick one low-stakes server, mark its allocation public (or add it as a manual forward), and connect to it through
Auto Proxy's address — not the one players currently use. Confirm the game actually works end to end: join, play a
minute, check the server's own logs for the player IP you'd expect given the mode you chose (see
[layouts.md](layouts.md) for what to expect in each mode). Do this for each server type you run if your servers
use meaningfully different network behaviour (e.g. one uses a query port your old setup forwarded separately).

## 3. Move DNS

Once you're satisfied, point whatever hostname players actually use at the new address — Auto Proxy's VPS IP, or
your own hostname if you set a public hostname in Setup step 3. Do this server by server if you have several, so a
problem with one doesn't take all of them down at once; or all at once if you've already tested broadly enough to
be confident.

Keep the old tunnel running until DNS has actually propagated and you've reconnected yourself through the new
address using the hostname (not the raw IP) — a cached DNS record somewhere can mean some players are still hitting
the old path for a while after you've switched the record.

## 4. Retire the old tunnel

Once nothing is using the old address any more (check its own traffic logs or connection counters if it has them),
stop and remove it — following whichever tool's own uninstall or teardown instructions apply. Auto Proxy's own
[uninstall page](uninstall.md) is only relevant if you're removing Auto Proxy itself, not the old tunnel.

There is no automated migration tool for this — each existing tunnel tool has its own config format and its own way
of being torn down, so this is a manual, deliberate cutover rather than a one-command switch.
