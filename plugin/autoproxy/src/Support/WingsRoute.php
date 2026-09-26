<?php

namespace Arrowtje\AutoProxy\Support;

/**
 * Does the panel reach a node's Wings through the VPS?
 *
 * When a node's hostname resolves to the VPS (so its own address stays hidden,
 * or because browsers reach the console through the VPS), the panel's own calls
 * to Wings take the tunnel too. Pelican gives its server-status call one second
 * in total (DaemonServerRepository::getDetails, connectTimeout(1)->timeout(1)),
 * so one lost packet on that detour makes a node flicker offline and the
 * console page's charts answer 403. The fix is a hosts entry on the panel; see
 * docs/node-setup.md, "Let the panel reach this node directly".
 *
 * This class is the decision only: given what each hostname resolved to from
 * the panel, which nodes take the detour. No DNS, no database, no cache, so it
 * is covered by test/rules-test.php. WingsRouteCheck does the lookups.
 */
class WingsRoute
{
    public const DOCS_PAGE = 'node-setup.md#let-the-panel-reach-this-node-directly';

    /** Lower case, no surrounding space, no trailing dot, no IPv6 brackets. */
    public static function normaliseHost(?string $host): string
    {
        $host = strtolower(trim((string) $host));
        $host = rtrim($host, '.');

        if (str_starts_with($host, '[') && str_ends_with($host, ']')) {
            $host = substr($host, 1, -1);
        }

        return $host;
    }

    public static function isIpLiteral(string $host): bool
    {
        return filter_var(static::normaliseHost($host), FILTER_VALIDATE_IP) !== false;
    }

    /** One spelling per address, so ::ffff:192.0.2.1 and 192.0.2.1 compare equal. */
    public static function normaliseAddress(string $address): ?string
    {
        $address = trim($address);

        if (str_starts_with(strtolower($address), '::ffff:') && filter_var(substr($address, 7), FILTER_VALIDATE_IP, FILTER_FLAG_IPV4) !== false) {
            $address = substr($address, 7);
        }

        $packed = @inet_pton($address);

        return $packed === false ? null : (string) inet_ntop($packed);
    }

    /**
     * The addresses in `getent ahosts <name>` output: one per line, first
     * column, each listed once per socket type.
     *
     * @return list<string>
     */
    public static function parseGetent(string $output): array
    {
        $addresses = [];

        foreach (preg_split('/\R/', $output) ?: [] as $line) {
            $first = strtok(trim($line), " \t");

            if ($first === false) {
                continue;
            }

            $address = static::normaliseAddress($first);

            if ($address !== null && !in_array($address, $addresses, true)) {
                $addresses[] = $address;
            }
        }

        return $addresses;
    }

    /**
     * The hostnames that need a lookup: every node's, once, IP literals left out.
     *
     * @param array<int|string, array{name: string, fqdn: string|null}> $nodes
     * @return list<string>
     */
    public static function hostsToResolve(array $nodes): array
    {
        $hosts = [];

        foreach ($nodes as $node) {
            $host = static::normaliseHost($node['fqdn'] ?? null);

            if ($host !== '' && !static::isIpLiteral($host) && !in_array($host, $hosts, true)) {
                $hosts[] = $host;
            }
        }

        return $hosts;
    }

    /**
     * Which nodes the panel reaches through the VPS. A name that could not be
     * looked up (null) is never reported: an unanswered lookup is not evidence
     * of a detour, and a false alarm here would send the admin editing hosts
     * files for nothing. A name with several addresses counts when any of them
     * is the VPS, because the panel may pick that one.
     *
     * @param array<int|string, array{name: string, fqdn: string|null}> $nodes node id => node
     * @param array<string, list<string>|null> $resolved normalised host => addresses
     * @return list<array{node_id: int|string, node: string, fqdn: string, literal: bool}>
     */
    public static function detours(array $nodes, array $resolved, string $vpsIp): array
    {
        $vps = static::normaliseAddress($vpsIp);

        if ($vps === null) {
            return [];
        }

        $found = [];

        foreach ($nodes as $id => $node) {
            $host = static::normaliseHost($node['fqdn'] ?? null);

            if ($host === '') {
                continue;
            }

            $literal = static::isIpLiteral($host);
            $addresses = $literal ? [$host] : ($resolved[$host] ?? null);

            if ($addresses === null) {
                continue;
            }

            $normalised = array_filter(array_map(static fn (string $a): ?string => static::normaliseAddress($a), $addresses));

            if (in_array($vps, $normalised, true)) {
                $found[] = [
                    'node_id' => $id,
                    'node' => (string) ($node['name'] ?? ('node ' . $id)),
                    'fqdn' => $host,
                    'literal' => $literal,
                ];
            }
        }

        return $found;
    }

    /**
     * Drop findings for a node that has since been deleted or given another
     * hostname, so a stale check never names the wrong thing.
     *
     * @param list<array{node_id: int|string, node: string, fqdn: string, literal: bool}> $findings
     * @param array<int|string, string|null> $fqdnsNow node id => fqdn
     * @return list<array{node_id: int|string, node: string, fqdn: string, literal: bool}>
     */
    public static function stillCurrent(array $findings, array $fqdnsNow): array
    {
        return array_values(array_filter($findings, static fn (array $row): bool => array_key_exists($row['node_id'], $fqdnsNow)
            && static::normaliseHost($fqdnsNow[$row['node_id']]) === $row['fqdn']));
    }

    /** Is a stored result old enough to look again? */
    public static function isDue(?int $checkedAt, int $now, int $everySeconds): bool
    {
        return $checkedAt === null || $now - $checkedAt >= $everySeconds;
    }

    /** @param array{node: string, fqdn: string, literal: bool} $row */
    public static function message(array $row, string $vpsIp): string
    {
        if ($row['literal']) {
            return sprintf(
                'Node %s: its address is the VPS\'s own address (%s), so the panel reaches its Wings through the VPS. '
                . 'A hosts entry cannot redirect an address: give the node a hostname that points at the VPS, then give the panel a hosts entry for that name with the node\'s own address.',
                $row['node'],
                $vpsIp,
            );
        }

        return sprintf(
            'Node %s: %s resolves to the VPS (%s) from this panel, so the panel reaches its Wings through the VPS. '
            . 'Add a hosts entry on the panel for %s with the node\'s own address; public DNS stays as it is.',
            $row['node'],
            $row['fqdn'],
            $vpsIp,
            $row['fqdn'],
        );
    }

    /** The dashboard banner's one line. */
    public static function shortMessage(array $row): string
    {
        return sprintf(
            'The panel reaches node %s through the VPS (%s points at it), so it can flicker offline and its console page can show 403 errors. See the Status page.',
            $row['node'],
            $row['fqdn'],
        );
    }
}
