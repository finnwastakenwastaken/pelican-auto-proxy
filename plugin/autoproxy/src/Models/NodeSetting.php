<?php

namespace Arrowtje\AutoProxy\Models;

use App\Models\Node;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/**
 * What Auto Proxy knows about one Pelican node that Pelican itself does not:
 * whether its ports should be published, and how its traffic reaches it.
 *
 * @property int $id
 * @property int $node_id
 * @property bool $proxied
 * @property string $mode
 * @property string|null $lan_ip
 * @property string|null $via_peer_id
 * @property string|null $peer_id
 * @property string|null $encrypted_join_code
 * @property \Illuminate\Support\Carbon|null $join_code_issued_at
 */
class NodeSetting extends Model
{
    /** A tunnel client runs on this node's own machine: players keep their real IP. */
    public const MODE_REAL = 'real';

    /** Reached across a LAN through another machine's client: shared IP. */
    public const MODE_SITE = 'site';

    public const MODES = [
        self::MODE_REAL => 'Tunnel client on this node\'s machine (real player IPs)',
        self::MODE_SITE => 'Via another client + LAN IP (shared IP)',
    ];

    protected $table = 'autoproxy_node_settings';

    protected $fillable = [
        'node_id', 'proxied', 'mode', 'lan_ip', 'via_peer_id',
        'peer_id', 'encrypted_join_code', 'join_code_issued_at',
    ];

    protected function casts(): array
    {
        return [
            'node_id' => 'integer',
            'proxied' => 'boolean',
            // The join code carries the peer's WireGuard private key, so it never
            // sits in the database in clear text.
            'encrypted_join_code' => 'encrypted',
            'join_code_issued_at' => 'datetime',
        ];
    }

    public function node(): BelongsTo
    {
        return $this->belongsTo(Node::class, 'node_id');
    }

    public function isReal(): bool
    {
        return $this->mode !== self::MODE_SITE;
    }

    public function isSite(): bool
    {
        return $this->mode === self::MODE_SITE;
    }

    /**
     * The row for a node, created on demand so callers never juggle nulls.
     * Not persisted until something is actually set.
     */
    public static function forNode(int $nodeId): self
    {
        return static::query()->firstOrNew(['node_id' => $nodeId], [
            'proxied' => false,
            'mode' => self::MODE_REAL,
        ]);
    }

    /**
     * node_id => row, for the reconcile and the admin pages.
     *
     * @return array<int, NodeSetting>
     */
    public static function map(): array
    {
        return static::query()->get()->keyBy('node_id')->all();
    }

    /**
     * The proxied nodes in the shape PeerHealth wants: node id => name, mode and
     * both peer ids. One query, no VPS call, so a dashboard widget can use it.
     *
     * @return array<int, array{name: string, mode: string, peer_id: string|null, via_peer_id: string|null}>
     */
    public static function proxiedNodeMap(): array
    {
        $rows = static::query()->where('proxied', true)->get();

        if ($rows->isEmpty()) {
            return [];
        }

        $names = Node::query()->whereIn('id', $rows->pluck('node_id'))->pluck('name', 'id')->all();
        $out = [];

        foreach ($rows as $row) {
            $out[(int) $row->node_id] = [
                'name' => (string) ($names[$row->node_id] ?? ('node ' . $row->node_id)),
                'mode' => $row->isSite() ? self::MODE_SITE : self::MODE_REAL,
                'peer_id' => $row->peer_id,
                'via_peer_id' => $row->via_peer_id,
            ];
        }

        return $out;
    }

    /**
     * The join code is a one-time secret. Once the peer has handshaked, the
     * client already has it and keeping a copy only widens the blast radius.
     */
    public function forgetJoinCode(): void
    {
        if ($this->encrypted_join_code === null) {
            return;
        }

        $this->encrypted_join_code = null;
        $this->join_code_issued_at = null;
        $this->save();
    }

    public function joinCode(): ?string
    {
        try {
            $code = $this->encrypted_join_code;
        } catch (\Throwable) {
            // APP_KEY changed: behave as "no code stored", the admin regenerates.
            return null;
        }

        return filled($code) ? (string) $code : null;
    }
}
