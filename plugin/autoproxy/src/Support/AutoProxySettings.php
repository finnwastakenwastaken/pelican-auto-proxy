<?php

namespace Arrowtje\AutoProxy\Support;

use Arrowtje\AutoProxy\Models\Setting;
use Illuminate\Database\QueryException;
use Throwable;

/**
 * Thin accessor over the autoproxy_settings key/value table.
 *
 * Secrets listed in ENCRYPTED are stored in `encrypted_value` (Laravel `encrypted`
 * cast) so the API token never sits in the database in clear text. The agent's
 * certificate is not a secret and lives in a file, not here.
 */
class AutoProxySettings
{
    public const ENCRYPTED = ['api_token'];

    /**
     * Join codes for tunnel clients that are not tied to a Pelican node (site
     * clients). Keyed by peer id, and encrypted: a join code carries that
     * client's WireGuard private key.
     */
    public const JOIN_CODE_PREFIX = 'join_code.';

    /** Everything the VPS code fills in. Kept in one place so Setup can clear it. */
    public const FROM_VPS_CODE = [
        'api_url', 'api_token', 'api_spki_sha256', 'endpoint_ip', 'api_port',
        'wg_pubkey', 'wg_port', 'tunnel_subnet', 'vps_tunnel_ip', 'agent_version',
    ];

    /** @var array<string, mixed>|null */
    protected static ?array $cache = null;

    public static function isEncrypted(string $key): bool
    {
        return in_array($key, static::ENCRYPTED, true) || str_starts_with($key, static::JOIN_CODE_PREFIX);
    }

    public static function get(string $key, mixed $default = null): mixed
    {
        $value = static::all()[$key] ?? null;

        if ($value === null || $value === '') {
            return $default ?? config('autoproxy.' . $key);
        }

        return $value;
    }

    /** @return array<string, mixed> */
    public static function all(): array
    {
        if (static::$cache !== null) {
            return static::$cache;
        }

        $values = [];

        try {
            foreach (Setting::query()->get() as $setting) {
                $values[$setting->key] = static::isEncrypted($setting->key)
                    ? $setting->encrypted_value
                    : $setting->value;
            }
        } catch (QueryException) {
            // Table not migrated yet (plugin installing): fall back to config defaults.
            return static::$cache = [];
        } catch (Throwable) {
            // Undecryptable token (APP_KEY changed): behave as "not configured".
            return static::$cache = [];
        }

        return static::$cache = $values;
    }

    public static function set(string $key, mixed $value): void
    {
        $column = static::isEncrypted($key) ? 'encrypted_value' : 'value';

        Setting::query()->updateOrCreate(['key' => $key], [$column => $value]);

        static::forget();
    }

    /** @param array<string, mixed> $values */
    public static function setMany(array $values): void
    {
        foreach ($values as $key => $value) {
            static::set($key, $value);
        }
    }

    public static function forget(): void
    {
        static::$cache = null;
    }

    // --- the VPS ------------------------------------------------------------

    public static function apiUrl(): string
    {
        return rtrim((string) static::get('api_url', ''), '/');
    }

    public static function apiToken(): string
    {
        return (string) static::get('api_token', '');
    }

    public static function endpointIp(): string
    {
        return (string) static::get('endpoint_ip', '');
    }

    public static function spkiPin(): string
    {
        return (string) static::get('api_spki_sha256', '');
    }

    public static function wgPubkey(): string
    {
        return (string) static::get('wg_pubkey', '');
    }

    public static function agentVersionFromCode(): string
    {
        return (string) static::get('agent_version', '');
    }

    /** True once a VPS code has been pasted and stored. */
    public static function isConnected(): bool
    {
        return static::apiUrl() !== '' && static::apiToken() !== '';
    }

    /**
     * Where the agent's certificate lives. Written 0600 by VpsCode::store();
     * handed to Guzzle as `verify`, so a missing file must fail loudly rather
     * than silently fall back to the system trust store.
     */
    public static function certPath(): string
    {
        return storage_path('app/autoproxy/agent.pem');
    }

    public static function certExists(): bool
    {
        return is_file(static::certPath());
    }

    // --- publishing ---------------------------------------------------------

    public static function publicHostname(): string
    {
        return trim((string) static::get('public_hostname', ''));
    }

    /**
     * What players connect to, and what the alias is rewritten to: the hostname
     * when one is set, otherwise the VPS IP from the VPS code. Empty only when
     * no VPS is connected yet, and then nothing is published at all.
     */
    public static function publicAddress(): string
    {
        $hostname = static::publicHostname();

        return $hostname !== '' ? $hostname : static::endpointIp();
    }

    /** @return string[] lower-cased, non-empty, de-duplicated */
    public static function keywords(): array
    {
        $raw = (string) static::get('keywords');

        $keywords = [];
        foreach (explode(',', $raw) as $keyword) {
            $keyword = strtolower(trim($keyword));
            if ($keyword !== '' && !in_array($keyword, $keywords, true)) {
                $keywords[] = $keyword;
            }
        }

        return $keywords;
    }

    public static function staleAfterMinutes(): int
    {
        return max(1, (int) static::get('stale_after_minutes', 5));
    }

    // --- join codes for clients without a node ------------------------------

    public static function joinCodeFor(string $peerId): ?string
    {
        $code = static::all()[static::JOIN_CODE_PREFIX . $peerId] ?? null;

        return filled($code) ? (string) $code : null;
    }

    public static function setJoinCode(string $peerId, string $code): void
    {
        static::set(static::JOIN_CODE_PREFIX . $peerId, $code);
    }

    public static function forgetJoinCode(string $peerId): void
    {
        Setting::query()->where('key', static::JOIN_CODE_PREFIX . $peerId)->delete();

        static::forget();
    }

    /** @return array<string, string> peer id => join code */
    public static function joinCodes(): array
    {
        $codes = [];

        foreach (static::all() as $key => $value) {
            if (str_starts_with($key, static::JOIN_CODE_PREFIX) && filled($value)) {
                $codes[substr($key, strlen(static::JOIN_CODE_PREFIX))] = (string) $value;
            }
        }

        return $codes;
    }

    // --- generated commands and help links ----------------------------------

    public static function releaseUrl(): string
    {
        return rtrim((string) config('autoproxy.release_url'), '/');
    }

    /**
     * The one line a new tunnel client is installed with. Extracted from the
     * Setup page and the console command, which each built this string by hand:
     * the URL, the pipe and the join code have to stay identical everywhere,
     * because an admin copies whichever one they happen to be looking at.
     *
     * It carries the client's private key. Every caller warns about that before
     * printing it.
     */
    public static function joinCommand(string $joinCode): string
    {
        return 'curl -fsSL ' . static::releaseUrl() . '/install-client.sh | sudo bash -s -- ' . $joinCode;
    }

    public static function docsUrl(string $page): string
    {
        return rtrim((string) config('autoproxy.docs_url'), '/') . '/' . ltrim($page, '/');
    }
}
