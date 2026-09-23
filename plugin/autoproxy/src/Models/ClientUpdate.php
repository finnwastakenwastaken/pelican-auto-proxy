<?php

namespace Arrowtje\AutoProxy\Models;

use Illuminate\Database\Eloquent\Model;
use Throwable;

/**
 * The per-client "allow remote updates" switch. Off unless an admin turned it
 * on for that client; see docs/security.md for what turning it on allows.
 *
 * @property int $id
 * @property string $peer_id
 * @property bool $remote_updates
 */
class ClientUpdate extends Model
{
    protected $table = 'autoproxy_client_updates';

    protected $fillable = ['peer_id', 'remote_updates'];

    protected function casts(): array
    {
        return ['remote_updates' => 'boolean'];
    }

    /** @return array<string, bool> peer id => allowed */
    public static function allowedMap(): array
    {
        try {
            return static::query()->where('remote_updates', true)->pluck('remote_updates', 'peer_id')
                ->map(fn ($value): bool => (bool) $value)->all();
        } catch (Throwable) {
            // Table not migrated yet (plugin mid-install): nothing is allowed.
            return [];
        }
    }

    public static function isAllowed(string $peerId): bool
    {
        return static::allowedMap()[$peerId] ?? false;
    }

    public static function setAllowed(string $peerId, bool $allowed): void
    {
        static::query()->updateOrCreate(['peer_id' => $peerId], ['remote_updates' => $allowed]);
    }

    /** A deleted peer's switch must not come back if the id were ever reused. */
    public static function forget(string $peerId): void
    {
        static::query()->where('peer_id', $peerId)->delete();
    }
}
