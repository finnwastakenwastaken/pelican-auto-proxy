<?php

namespace Arrowtje\AutoProxy\Support;

/**
 * Whether the tunnel client each proxied node depends on is actually connected.
 *
 * A stopped client is the most common real failure and the one the rest of the
 * plugin used to miss completely: the VPS still answers, the push is still
 * accepted, the sync is still "ok", and every port for that node is a black
 * hole. Nothing here talks to the VPS - it reads the peer snapshot the reconcile
 * already stored, so a dashboard widget costs one query and no network.
 *
 * Pure: no Laravel, no database, no HTTP, so test/rules-test.php can require it.
 */
final class PeerHealth
{
    /**
     * The peer a node's traffic depends on. Site nodes are reached through
     * another machine's client, so their health is that client's health.
     *
     * @param array{mode?: string|null, peer_id?: string|null, via_peer_id?: string|null} $node
     */
    public static function peerIdFor(array $node): string
    {
        $peer = ($node['mode'] ?? 'real') === 'site'
            ? ($node['via_peer_id'] ?? null)
            : ($node['peer_id'] ?? null);

        return is_string($peer) ? trim($peer) : '';
    }

    /**
     * The proxied nodes whose client has not handshaked recently enough.
     *
     * $snapshot is peer id => ['name' => ?string, 'handshake_age_s' => ?int], as
     * the VPS reported it $snapshotAgeSeconds ago; the age is added back on, so a
     * snapshot that itself went stale does not read as healthy. An empty snapshot
     * means "nothing has been recorded yet" and reports nothing: a plugin that
     * cried wolf before its first successful reconcile would train admins to
     * ignore the banner.
     *
     * A node with no peer at all is not reported here - that is the existing
     * "node only half set up" message, and two banners for one cause is noise.
     *
     * @param array<string, array<string, mixed>> $snapshot
     * @param array<int|string, array{name?: string, mode?: string|null, peer_id?: string|null, via_peer_id?: string|null}> $nodes proxied nodes, keyed by node id
     * @return list<array{node_id: int|string, node: string, peer_id: string, minutes: int|null, known: bool}>
     */
    public static function unhealthy(array $snapshot, array $nodes, int $snapshotAgeSeconds, int $staleAfterMinutes): array
    {
        if ($snapshot === []) {
            return [];
        }

        $limit = max(1, $staleAfterMinutes) * 60;
        $drift = max(0, $snapshotAgeSeconds);
        $out = [];

        foreach ($nodes as $nodeId => $node) {
            $peerId = static::peerIdFor($node);

            if ($peerId === '') {
                continue;
            }

            $known = array_key_exists($peerId, $snapshot);
            $age = $known ? ($snapshot[$peerId]['handshake_age_s'] ?? null) : null;

            if ($known && is_numeric($age) && ((int) $age + $drift) <= $limit) {
                continue;
            }

            $out[] = [
                'node_id' => $nodeId,
                'node' => (string) ($node['name'] ?? ('node ' . $nodeId)),
                'peer_id' => $peerId,
                'minutes' => (is_numeric($age) && $known) ? intdiv((int) $age + $drift, 60) : null,
                'known' => $known,
            ];
        }

        return $out;
    }

    /**
     * One sentence an admin can act on. "Never connected" and "stopped talking"
     * are different problems - the first is an install that never finished, the
     * second is a machine that went away - so they never share a wording.
     *
     * @param array{node: string, minutes: int|null, known: bool} $row
     */
    public static function message(array $row): string
    {
        if (!$row['known']) {
            return sprintf(
                'The VPS does not know a tunnel client for node %s any more, so its ports are closed. Re-run step 2 in Setup for that node.',
                $row['node'],
            );
        }

        if ($row['minutes'] === null) {
            return sprintf(
                'The tunnel client for node %s has never connected. Players cannot reach its servers. Run the join command from Setup on that machine.',
                $row['node'],
            );
        }

        return sprintf(
            'The tunnel client for node %s has not connected for %d minute(s). Players cannot reach its servers. Check that autoproxy-client is running on that machine.',
            $row['node'],
            $row['minutes'],
        );
    }
}
