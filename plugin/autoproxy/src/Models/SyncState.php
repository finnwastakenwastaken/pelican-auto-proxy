<?php

namespace Arrowtje\AutoProxy\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Support\Carbon;

/**
 * @property int $id
 * @property Carbon|null $last_run_at
 * @property Carbon|null $last_success_at
 * @property Carbon|null $last_pushed_at
 * @property string|null $last_error
 * @property string|null $last_pushed_hash
 * @property array<int, mixed>|null $conflicts
 * @property array<int, mixed>|null $node_not_configured
 * @property array<int, mixed>|null $not_proxied
 * @property array<int, mixed>|null $unassigned
 * @property array<string, mixed>|null $agent_status
 * @property array<string, mixed>|null $peer_health
 * @property Carbon|null $peer_health_at
 * @property int $rule_count
 * @property int $allocation_rule_count
 * @property int $manual_rule_count
 */
class SyncState extends Model
{
    protected $table = 'autoproxy_sync_state';

    protected $guarded = [];

    protected function casts(): array
    {
        return [
            'last_run_at' => 'datetime',
            'last_success_at' => 'datetime',
            'last_pushed_at' => 'datetime',
            'conflicts' => 'array',
            'node_not_configured' => 'array',
            'not_proxied' => 'array',
            'unassigned' => 'array',
            'agent_status' => 'array',
            'peer_health' => 'array',
            'peer_health_at' => 'datetime',
            'rule_count' => 'integer',
            'allocation_rule_count' => 'integer',
            'manual_rule_count' => 'integer',
        ];
    }

    /**
     * The one and only state row. Created on first read so every page can rely on it.
     */
    public static function current(): self
    {
        return static::query()->firstOrCreate(['id' => 1]);
    }

    public function hasEverRun(): bool
    {
        return $this->last_run_at !== null;
    }

    /**
     * peer id => what the VPS said about it, as of peerHealthAgeSeconds() ago.
     *
     * @return array<string, array<string, mixed>>
     */
    public function peerSnapshot(): array
    {
        $snapshot = $this->peer_health;

        return is_array($snapshot) ? $snapshot : [];
    }

    /**
     * How long ago that snapshot was taken. Handshake ages in it are counted from
     * then, not from now, so a reconcile that stopped running cannot make a dead
     * tunnel client look freshly connected.
     */
    public function peerHealthAgeSeconds(): int
    {
        if ($this->peer_health_at === null) {
            return 0;
        }

        return max(0, (int) $this->peer_health_at->diffInSeconds(Carbon::now()));
    }

    public function isStale(int $staleAfterMinutes): bool
    {
        if ($this->last_success_at === null) {
            return true;
        }

        return $this->last_success_at->lt(Carbon::now()->subMinutes(max(1, $staleAfterMinutes)));
    }
}
