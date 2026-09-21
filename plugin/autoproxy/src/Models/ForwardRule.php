<?php

namespace Arrowtje\AutoProxy\Models;

use Illuminate\Database\Eloquent\Builder;
use Illuminate\Database\Eloquent\Model;

/**
 * A forward the admin typed in by hand, for something that is not a Pelican
 * allocation (the panel itself, SFTP, a service on another LAN machine).
 *
 * A rule points at exactly one of:
 *  - target_peer: a machine running a tunnel client (real client IPs), or
 *  - target_ip reached through via_peer (a LAN address behind a client).
 *
 * @property int $id
 * @property string $name
 * @property string $protocol
 * @property int $public_port
 * @property int|null $public_port_end
 * @property string|null $target_peer
 * @property string|null $target_ip
 * @property string|null $via_peer
 * @property int|null $target_port
 * @property bool $enabled
 * @property int|null $created_by
 * @property string|null $notes
 */
class ForwardRule extends Model
{
    public const PROTOCOLS = ['tcp' => 'TCP', 'udp' => 'UDP', 'both' => 'TCP + UDP'];

    public const TARGET_PEER = 'peer';

    public const TARGET_LAN = 'lan';

    protected $table = 'autoproxy_rules';

    protected $fillable = [
        'name', 'protocol', 'public_port', 'public_port_end',
        'target_peer', 'target_ip', 'via_peer', 'target_port',
        'enabled', 'created_by', 'notes',
    ];

    protected function casts(): array
    {
        return [
            'public_port' => 'integer',
            'public_port_end' => 'integer',
            'target_port' => 'integer',
            'enabled' => 'boolean',
            'created_by' => 'integer',
        ];
    }

    /** @param Builder<ForwardRule> $query */
    public function scopeEnabled(Builder $query): void
    {
        $query->where('enabled', true);
    }

    public function portLabel(): string
    {
        return $this->public_port_end && $this->public_port_end !== $this->public_port
            ? $this->public_port . '-' . $this->public_port_end
            : (string) $this->public_port;
    }

    public function targetsPeer(): bool
    {
        return filled($this->target_peer);
    }

    /** Human-readable target, for tables and conflict messages. */
    public function targetLabel(): string
    {
        $target = $this->targetsPeer()
            ? 'client ' . $this->target_peer
            : (string) $this->target_ip . ($this->via_peer ? ' via ' . $this->via_peer : '');

        return $target . ($this->target_port ? ':' . $this->target_port : '');
    }
}
