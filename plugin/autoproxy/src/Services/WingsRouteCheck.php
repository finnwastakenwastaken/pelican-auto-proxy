<?php

namespace Arrowtje\AutoProxy\Services;

use App\Models\Node;
use Arrowtje\AutoProxy\Support\AutoProxySettings;
use Arrowtje\AutoProxy\Support\WingsRoute;
use Illuminate\Process\Exceptions\ProcessTimedOutException;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Process;
use Throwable;

/**
 * Looks up every node's hostname the way the panel itself would, and keeps
 * the answer. WingsRoute decides what the answer means.
 *
 * Cheap on purpose. The lookups run from the once-a-minute `autoproxy:sync`,
 * at most every REFRESH_SECONDS, and from the Status page's "Check again"
 * button; pages and the dashboard banner only read the stored result, so no
 * page view ever waits on DNS. Each lookup is `getent ahosts <name>` with a
 * LOOKUP_TIMEOUT: getent asks the same resolver the panel's own HTTP calls use,
 * /etc/hosts included - which is the point, because a hosts entry is the fix -
 * and unlike PHP's gethostbynamel() it can be given a deadline. The whole run
 * stops after BUDGET_SECONDS; names not reached count as "not looked up",
 * which is never reported as a problem.
 */
class WingsRouteCheck
{
    public const CACHE_KEY = 'autoproxy.wings_route';

    public const REFRESH_SECONDS = 600;

    public const LOOKUP_TIMEOUT = 2;

    public const BUDGET_SECONDS = 10;

    /**
     * The stored result, or null when there is none yet.
     *
     * @return array{checked_at: int, vps_ip: string, findings: list<array<string, mixed>>, unresolved: list<string>}|null
     */
    public static function cached(): ?array
    {
        try {
            $stored = Cache::get(static::CACHE_KEY);
        } catch (Throwable) {
            return null;
        }

        return is_array($stored) && isset($stored['checked_at'], $stored['findings']) ? $stored : null;
    }

    /**
     * Nodes the panel reaches through the VPS, as last checked, trimmed to
     * nodes that still exist with the same hostname and to the VPS that is
     * connected now. One small query; never a lookup.
     *
     * @return list<array{node_id: int|string, node: string, fqdn: string, literal: bool}>
     */
    public static function current(): array
    {
        $stored = static::cached();

        if ($stored === null || $stored['findings'] === [] || $stored['vps_ip'] !== AutoProxySettings::endpointIp()) {
            return [];
        }

        try {
            $fqdns = Node::query()->pluck('fqdn', 'id')->all();
        } catch (Throwable) {
            return [];
        }

        return WingsRoute::stillCurrent($stored['findings'], $fqdns);
    }

    /** Called from `autoproxy:sync`: looks again only when the stored result is old. */
    public static function refreshIfDue(): ?array
    {
        $stored = static::cached();

        if (!WingsRoute::isDue($stored['checked_at'] ?? null, time(), static::REFRESH_SECONDS)
            && ($stored['vps_ip'] ?? null) === AutoProxySettings::endpointIp()) {
            return null;
        }

        return static::refresh();
    }

    /**
     * Look every hostname up now and store the result.
     *
     * @return array{checked_at: int, vps_ip: string, findings: list<array<string, mixed>>, unresolved: list<string>}
     */
    public static function refresh(): array
    {
        $vpsIp = AutoProxySettings::endpointIp();
        $nodes = [];

        foreach (Node::query()->get(['id', 'name', 'fqdn']) as $node) {
            $nodes[$node->id] = ['name' => (string) $node->name, 'fqdn' => (string) $node->fqdn];
        }

        $resolved = [];
        $unresolved = [];
        $deadline = microtime(true) + static::BUDGET_SECONDS;

        foreach (WingsRoute::hostsToResolve($nodes) as $host) {
            $resolved[$host] = microtime(true) < $deadline ? static::lookup($host) : null;

            if ($resolved[$host] === null) {
                $unresolved[] = $host;
            }
        }

        $result = [
            'checked_at' => time(),
            'vps_ip' => $vpsIp,
            'findings' => WingsRoute::detours($nodes, $resolved, $vpsIp),
            'unresolved' => $unresolved,
        ];

        try {
            Cache::put(static::CACHE_KEY, $result, 86400);
        } catch (Throwable) {
            // No cache store: nothing is shown, which is the quiet failure.
        }

        return $result;
    }

    public static function forget(): void
    {
        try {
            Cache::forget(static::CACHE_KEY);
        } catch (Throwable) {
        }
    }

    /**
     * Every address the name resolves to from here, [] when it does not
     * resolve, null when the answer is unknown (timeout, no resolver).
     *
     * @return list<string>|null
     */
    protected static function lookup(string $host): ?array
    {
        try {
            $process = Process::timeout(static::LOOKUP_TIMEOUT)->run(['getent', 'ahosts', $host]);
        } catch (ProcessTimedOutException) {
            return null;
        } catch (Throwable) {
            return static::fallbackLookup($host);
        }

        return match ($process->exitCode()) {
            0 => WingsRoute::parseGetent($process->output()),
            // getent: "key not found". The name does not resolve at all, so it
            // certainly does not resolve to the VPS.
            2 => [],
            // 127: no getent on this system.
            127 => static::fallbackLookup($host),
            default => null,
        };
    }

    /**
     * Without getent: PHP's own resolver call, IPv4 only and with no timeout
     * of its own, which is why it is only the fallback.
     *
     * @return list<string>|null
     */
    protected static function fallbackLookup(string $host): ?array
    {
        $addresses = @gethostbynamel($host);

        return $addresses === false ? null : array_values(array_unique($addresses));
    }
}
