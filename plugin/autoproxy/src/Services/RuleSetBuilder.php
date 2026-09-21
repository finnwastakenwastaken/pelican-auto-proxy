<?php

namespace Arrowtje\AutoProxy\Services;

use App\Models\Allocation;
use Arrowtje\AutoProxy\Models\ForwardRule;
use Arrowtje\AutoProxy\Models\NodeSetting;
use Arrowtje\AutoProxy\Support\AutoProxySettings;
use Arrowtje\AutoProxy\Support\Ip;
use Arrowtje\AutoProxy\Support\RuleSet;
use Illuminate\Database\Eloquent\Builder;

/**
 * Turns "what the panel says" into "what the agent should apply".
 *
 * Reconcile, not events: bulk allocation deletes and server deletion update
 * allocations with a query and fire no model events, so this rebuilds the whole
 * desired set from scratch every run.
 *
 * A rule targets a peer directly (real-IP mode: the tunnel client runs on that
 * node's own machine) or a LAN address reached through a peer (site mode).
 */
class RuleSetBuilder
{
    /**
     * Exact match on the trimmed, case-folded alias. Bind the wanted value lowered.
     * Live aliases are free text ("batch", "project zomboid", "Palworld"), so a
     * LIKE or a contains-match would publish fifty ports by accident.
     */
    public const ALIAS_MATCH_SQL = 'LOWER(TRIM(ip_alias)) = ?';

    public function build(): RuleSet
    {
        $publicAddress = AutoProxySettings::publicAddress();
        $keywords = AutoProxySettings::keywords();
        $nodes = NodeSetting::map();

        // Rewrite keyword aliases to the public address FIRST, so the rows below
        // see one single alias value and players get a connectable address.
        // Only on proxied nodes: advertising the VPS address for a port nothing
        // forwards would send players somewhere that never answers.
        $aliasesRewritten = $this->rewriteKeywordAliases($keywords, $publicAddress, $this->proxiedNodeIds($nodes));

        $rows = $this->allocationRows($publicAddress, $keywords, $nodes);
        [$rules, $nodeNotConfigured, $notProxied, $unassigned] = $this->partitionAllocationRows($rows);

        foreach ($this->manualRules() as $rule) {
            $rules[] = [
                'id' => 'manual-' . $rule->id,
                'proto' => $rule->protocol,
                'public_port' => (int) $rule->public_port,
                'public_port_end' => $rule->public_port_end !== null ? (int) $rule->public_port_end : null,
                'target_peer' => $rule->targetsPeer() ? $rule->target_peer : null,
                'target_ip' => $rule->targetsPeer() ? null : $rule->target_ip,
                'via_peer' => $rule->targetsPeer() ? null : $rule->via_peer,
                'target_port' => $rule->target_port !== null ? (int) $rule->target_port : null,
                'note' => $this->note($rule->name),
            ];
        }

        [$kept, $conflicts] = $this->resolveConflicts($rules);

        $allocationRuleCount = 0;
        $manualRuleCount = 0;
        foreach ($kept as $rule) {
            str_starts_with($rule['id'], 'alloc-') ? $allocationRuleCount++ : $manualRuleCount++;
        }

        return new RuleSet(
            rules: $kept,
            conflicts: $conflicts,
            nodeNotConfigured: $nodeNotConfigured,
            notProxied: $notProxied,
            unassigned: $unassigned,
            allocations: $rows,
            hash: $this->hash($kept),
            allocationRuleCount: $allocationRuleCount,
            manualRuleCount: $manualRuleCount,
            aliasesRewritten: $aliasesRewritten,
        );
    }

    /**
     * Read-only view of every allocation that asks to be published, with the
     * target the agent would get. No writes: the admin UI renders this on page load.
     *
     * @param array<int, NodeSetting>|null $nodes
     * @param string[]|null $keywords
     * @return array<int, array<string, mixed>>
     */
    public function allocationRows(?string $publicAddress = null, ?array $keywords = null, ?array $nodes = null): array
    {
        $publicAddress ??= AutoProxySettings::publicAddress();
        $keywords ??= AutoProxySettings::keywords();
        $nodes ??= NodeSetting::map();

        $rows = [];

        foreach ($this->publicAllocations($publicAddress, $keywords) as $allocation) {
            $node = $nodes[$allocation->node_id] ?? null;

            $rows[] = [
                'allocation_id' => $allocation->id,
                'server' => $allocation->server?->name ?? 'unassigned',
                'server_id' => $allocation->server_id,
                'node' => $allocation->node?->name ?? ('node ' . $allocation->node_id),
                'node_id' => $allocation->node_id,
                'ip' => $allocation->ip,
                'port' => (int) $allocation->port,
                'alias' => $allocation->ip_alias,
                'proxied' => (bool) ($node?->proxied ?? false),
                'mode' => $node?->mode ?? NodeSetting::MODE_REAL,
                'peer_id' => $node?->peer_id,
                'via_peer_id' => $node?->via_peer_id,
                'node_lan_ip' => $node?->lan_ip,
            ];
        }

        return $rows;
    }

    /**
     * One UPDATE query: every allocation on a proxied node whose alias is a
     * keyword becomes the public address. Query update, so no model events -
     * intentional, the same reason the whole reconcile exists.
     *
     * @param string[] $keywords
     * @param int[] $proxiedNodeIds
     */
    public function rewriteKeywordAliases(array $keywords, string $publicAddress, array $proxiedNodeIds): int
    {
        if ($publicAddress === '' || $keywords === [] || $proxiedNodeIds === []) {
            return 0;
        }

        // A keyword equal to the address itself would be a no-op update.
        $keywords = array_values(array_filter($keywords, fn ($k) => $k !== strtolower($publicAddress)));

        if ($keywords === []) {
            return 0;
        }

        return Allocation::query()
            ->whereIn('node_id', $proxiedNodeIds)
            ->where(function (Builder $query) use ($keywords) {
                foreach ($keywords as $keyword) {
                    $query->orWhereRaw(self::ALIAS_MATCH_SQL, [$keyword]);
                }
            })
            ->update(['ip_alias' => $publicAddress]);
    }

    /**
     * Allocations asking to be published: the public address itself, or one of
     * the keywords (still unrewritten, e.g. because their node is not proxied).
     *
     * @param string[] $keywords
     * @return \Illuminate\Support\Collection<int, Allocation>
     */
    public function publicAllocations(string $publicAddress, array $keywords = [])
    {
        $wanted = [];

        if ($publicAddress !== '') {
            $wanted[] = strtolower(trim($publicAddress));
        }

        foreach ($keywords as $keyword) {
            $keyword = strtolower(trim($keyword));
            if ($keyword !== '' && !in_array($keyword, $wanted, true)) {
                $wanted[] = $keyword;
            }
        }

        if ($wanted === []) {
            return collect();
        }

        return Allocation::query()
            ->with(['node', 'server'])
            ->where(function (Builder $query) use ($wanted) {
                foreach ($wanted as $value) {
                    $query->orWhereRaw(self::ALIAS_MATCH_SQL, [$value]);
                }
            })
            ->orderBy('node_id')
            ->orderBy('port')
            ->get();
    }

    /**
     * Splits allocation rows into forwarding rules and the three reasons a row
     * gets none. Pure array logic, no DB, so it is unit-tested directly against
     * fabricated rows.
     *
     * Order matters, because each row gets exactly one reason and the admin must
     * be told the FIRST thing to fix:
     *   1. no server assigned      - the alias is set, but nothing would answer
     *   2. node is not proxied     - Auto Proxy was never switched on for it
     *   3. the node's mode is half configured (no client yet, no LAN IP, ...)
     *
     * A stopped server still counts as assigned: server_id stays set.
     *
     * @param array<int, array<string, mixed>> $rows
     * @return array{0: array<int, array<string, mixed>>, 1: array<int, array<string, mixed>>, 2: array<int, array<string, mixed>>, 3: array<int, array<string, mixed>>}
     */
    public function partitionAllocationRows(array $rows): array
    {
        $rules = [];
        $nodeNotConfigured = [];
        $notProxied = [];
        $unassigned = [];

        foreach ($rows as $row) {
            if (($row['server_id'] ?? null) === null) {
                $unassigned[] = $this->skipped($row, 'no server assigned');

                continue;
            }

            if (!($row['proxied'] ?? false)) {
                $notProxied[] = $this->skipped($row, 'node not proxied');

                continue;
            }

            $target = $this->targetFor($row);

            if (isset($target['reason'])) {
                $nodeNotConfigured[] = $this->skipped($row, (string) $target['reason']);

                continue;
            }

            $rules[] = [
                'id' => 'alloc-' . $row['allocation_id'],
                'proto' => 'both',
                'public_port' => $row['port'],
                'public_port_end' => null,
                'target_peer' => $target['target_peer'] ?? null,
                'target_ip' => $target['target_ip'] ?? null,
                'via_peer' => $target['via_peer'] ?? null,
                'target_port' => null,
                'note' => $this->note((string) $row['server']),
            ];
        }

        return [$rules, $nodeNotConfigured, $notProxied, $unassigned];
    }

    /**
     * Where one allocation's traffic should go, or why it cannot be worked out.
     *
     * real mode: straight at the node's own peer, so the game server sees the
     * player's real address. site mode: a LAN address behind someone else's
     * client, which means shared IPs and needs both halves configured.
     *
     * @param array<string, mixed> $row
     * @return array<string, string>
     */
    public function targetFor(array $row): array
    {
        $mode = (string) ($row['mode'] ?? NodeSetting::MODE_REAL);

        if ($mode === NodeSetting::MODE_SITE) {
            $viaPeer = (string) ($row['via_peer_id'] ?? '');

            if ($viaPeer === '') {
                return ['reason' => 'site mode without a tunnel client to route through'];
            }

            // A concrete private allocation IP beats the node mapping; 0.0.0.0
            // (the Wings default) tells us nothing, so fall back to the node's LAN IP.
            $ip = Ip::isPrivateV4($row['ip'] ?? null)
                ? (string) $row['ip']
                : (string) ($row['node_lan_ip'] ?? '');

            if (!Ip::isPrivateV4($ip)) {
                return ['reason' => 'site mode without a LAN IP for this node'];
            }

            return ['target_ip' => $ip, 'via_peer' => $viaPeer];
        }

        $peer = (string) ($row['peer_id'] ?? '');

        if ($peer === '') {
            return ['reason' => 'no tunnel client on this node yet'];
        }

        return ['target_peer' => $peer];
    }

    /** @return \Illuminate\Support\Collection<int, ForwardRule> */
    public function manualRules()
    {
        return ForwardRule::query()->enabled()->orderBy('public_port')->get();
    }

    /**
     * The wire shape the agent's API accepts: exactly one target form, and no
     * null keys, so "not set" and "set to nothing" can never be confused.
     *
     * @param array<int, array<string, mixed>> $rules
     * @return array<int, array<string, mixed>>
     */
    public function wire(array $rules): array
    {
        return array_values(array_map(
            fn (array $rule) => array_filter($rule, fn ($value) => $value !== null),
            $rules,
        ));
    }

    /**
     * @param array<int, NodeSetting> $nodes
     * @return int[]
     */
    public function proxiedNodeIds(array $nodes): array
    {
        $ids = [];

        foreach ($nodes as $nodeId => $node) {
            if ($node->proxied) {
                $ids[] = (int) $nodeId;
            }
        }

        return $ids;
    }

    /**
     * @param array<string, mixed> $row
     * @return array<string, mixed>
     */
    protected function skipped(array $row, string $reason): array
    {
        return [
            'node_id' => $row['node_id'] ?? null,
            'node' => $row['node'] ?? null,
            'allocation_id' => $row['allocation_id'] ?? null,
            'port' => $row['port'] ?? null,
            'ip' => $row['ip'] ?? null,
            'reason' => $reason,
        ];
    }

    protected function note(string $note): string
    {
        return mb_substr(trim($note), 0, 100);
    }

    /**
     * Two rules collide when their protocols overlap ("both" overlaps everything)
     * and their public port spans overlap. Different target: drop BOTH, because we
     * cannot know which one the admin meant. Same target: keep the first.
     *
     * @param array<int, array<string, mixed>> $rules
     * @return array{0: array<int, array<string, mixed>>, 1: array<int, array<string, mixed>>}
     */
    public function resolveConflicts(array $rules): array
    {
        usort($rules, fn ($a, $b) => [$a['public_port'], $a['id']] <=> [$b['public_port'], $b['id']]);

        $drop = [];
        $conflicts = [];
        $count = count($rules);

        for ($i = 0; $i < $count; $i++) {
            $a = $rules[$i];
            $aEnd = $a['public_port_end'] ?? $a['public_port'];

            for ($j = $i + 1; $j < $count; $j++) {
                $b = $rules[$j];

                // Sorted by start port: once b starts past a's end, nothing later overlaps a.
                if ($b['public_port'] > $aEnd) {
                    break;
                }

                if (!$this->protocolsOverlap($a['proto'], $b['proto'])) {
                    continue;
                }

                if ($this->targetKey($a) === $this->targetKey($b)) {
                    // Harmless duplicate for the admin, but nft maps reject overlapping
                    // intervals, so only the first survives.
                    $drop[$b['id']] = true;
                    $conflicts[] = $this->conflict($a, $b, 'same target on overlapping ports; the later rule is ignored');

                    continue;
                }

                $drop[$a['id']] = true;
                $drop[$b['id']] = true;
                $conflicts[] = $this->conflict($a, $b, 'different targets on overlapping ports; both rules are withheld');
            }
        }

        $kept = array_values(array_filter($rules, fn ($rule) => !isset($drop[$rule['id']])));

        return [$kept, $conflicts];
    }

    protected function protocolsOverlap(string $a, string $b): bool
    {
        return $a === $b || $a === 'both' || $b === 'both';
    }

    /**
     * Identity of a destination. Two rules pointing at the same peer are the same
     * target; the same LAN IP behind two different clients is not.
     *
     * @param array<string, mixed> $rule
     */
    public function targetKey(array $rule): string
    {
        $target = ($rule['target_peer'] ?? null) !== null
            ? 'peer:' . $rule['target_peer']
            : 'lan:' . ($rule['via_peer'] ?? '') . '/' . ($rule['target_ip'] ?? '');

        return $target . ':' . ($rule['target_port'] ?? '');
    }

    /**
     * @param array<string, mixed> $a
     * @param array<string, mixed> $b
     * @return array<string, mixed>
     */
    protected function conflict(array $a, array $b, string $reason): array
    {
        return [
            'a' => $a['id'],
            'b' => $b['id'],
            'a_note' => $a['note'],
            'b_note' => $b['note'],
            'ports' => $this->portLabel($a) . ' vs ' . $this->portLabel($b),
            'proto' => $a['proto'] . '/' . $b['proto'],
            'a_target' => $this->targetLabel($a),
            'b_target' => $this->targetLabel($b),
            'reason' => $reason,
        ];
    }

    /** @param array<string, mixed> $rule */
    protected function portLabel(array $rule): string
    {
        return $rule['public_port_end'] !== null && $rule['public_port_end'] !== $rule['public_port']
            ? $rule['public_port'] . '-' . $rule['public_port_end']
            : (string) $rule['public_port'];
    }

    /** @param array<string, mixed> $rule */
    public function targetLabel(array $rule): string
    {
        $label = ($rule['target_peer'] ?? null) !== null
            ? 'client ' . $rule['target_peer']
            : (string) ($rule['target_ip'] ?? '?') . (($rule['via_peer'] ?? null) !== null ? ' via ' . $rule['via_peer'] : '');

        return $label . (($rule['target_port'] ?? null) !== null ? ':' . $rule['target_port'] : '');
    }

    /**
     * Canonical JSON (rules sorted by id, fixed key order) so an unchanged desired
     * state always hashes the same and we do not push every minute.
     *
     * @param array<int, array<string, mixed>> $rules
     */
    public function hash(array $rules): string
    {
        $canonical = array_map(fn ($rule) => [
            'id' => $rule['id'],
            'proto' => $rule['proto'],
            'public_port' => $rule['public_port'],
            'public_port_end' => $rule['public_port_end'] ?? null,
            'target_peer' => $rule['target_peer'] ?? null,
            'target_ip' => $rule['target_ip'] ?? null,
            'via_peer' => $rule['via_peer'] ?? null,
            'target_port' => $rule['target_port'] ?? null,
            'note' => $rule['note'],
        ], $rules);

        usort($canonical, fn ($a, $b) => strcmp($a['id'], $b['id']));

        return hash('sha256', (string) json_encode($canonical, JSON_UNESCAPED_SLASHES));
    }
}
