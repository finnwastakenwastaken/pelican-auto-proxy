<?php

namespace Arrowtje\AutoProxy\Services;

use Arrowtje\AutoProxy\Support\AutoProxySettings;
use Arrowtje\AutoProxy\Support\ClientVersion;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Http;
use Throwable;

/**
 * The newest published release number, which is also the newest client
 * version: every release ships the agent, the client and this plugin together
 * under one tag.
 *
 * Read from the same update.json the panel's own plugin updater polls (the
 * release_url setting + /update.json, which resolves to the latest release),
 * so "update available" for a node and for this plugin can never disagree.
 * A GitHub API "releases/latest" answer ({"tag_name": "v1.2.3"}) is accepted
 * too, for an admin who points latest_version_url there instead.
 *
 * Cached, because the Status page polls every 30 seconds: an hour after a
 * successful lookup, ten minutes after a failed one (so a GitHub outage costs
 * the page one slow request per ten minutes, not one per poll).
 */
class LatestRelease
{
    public const CACHE_KEY = 'autoproxy.latest_release';

    public static function url(): string
    {
        $override = trim((string) AutoProxySettings::get('latest_version_url', ''));

        if ($override !== '') {
            return $override;
        }

        return AutoProxySettings::releaseUrl() . '/update.json';
    }

    /** @return array{version: string|null, error: string|null, checked_at: int} */
    public static function get(): array
    {
        try {
            $cached = Cache::get(static::CACHE_KEY);
        } catch (Throwable) {
            $cached = null;
        }

        if (is_array($cached) && array_key_exists('version', $cached)) {
            return $cached;
        }

        $result = static::fetch();

        try {
            Cache::put(static::CACHE_KEY, $result, $result['version'] !== null ? 3600 : 600);
        } catch (Throwable) {
            // No cache store: the next page load asks again. Slower, not wrong.
        }

        return $result;
    }

    public static function version(): ?string
    {
        return static::get()['version'];
    }

    public static function forget(): void
    {
        try {
            Cache::forget(static::CACHE_KEY);
        } catch (Throwable) {
        }
    }

    /** @return array{version: string|null, error: string|null, checked_at: int} */
    protected static function fetch(): array
    {
        $url = static::url();

        try {
            $response = Http::acceptJson()->connectTimeout(3)->timeout(5)->get($url);
        } catch (Throwable $exception) {
            return ['version' => null, 'error' => 'Could not reach ' . $url . ': ' . $exception->getMessage(), 'checked_at' => time()];
        }

        if ($response->failed()) {
            return ['version' => null, 'error' => $url . ' answered HTTP ' . $response->status(), 'checked_at' => time()];
        }

        $body = $response->json();
        $raw = null;

        if (is_array($body)) {
            $raw = $body['*']['version'] ?? $body['tag_name'] ?? null;
        }

        $version = ClientVersion::release(is_string($raw) ? $raw : null);

        if ($version === null) {
            return ['version' => null, 'error' => $url . ' did not name a release number', 'checked_at' => time()];
        }

        return ['version' => $version, 'error' => null, 'checked_at' => time()];
    }
}
