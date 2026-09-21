<?php

namespace Arrowtje\AutoProxy\Services;

use Arrowtje\AutoProxy\Models\NodeSetting;
use Arrowtje\AutoProxy\Models\SyncState;
use Arrowtje\AutoProxy\Support\AutoProxySettings;
use Arrowtje\AutoProxy\Support\SyncResult;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\Log;
use Throwable;

/**
 * One reconcile: build the desired state, push it when it matters, and write down
 * what happened. Never throws - the scheduler must survive a dead VPS.
 */
class SyncService
{
    public function __construct(
        protected RuleSetBuilder $builder,
        protected AgentClient $client,
    ) {}

    public function run(bool $force = false): SyncResult
    {
        $state = SyncState::current();
        $now = Carbon::now();

        $attributes = ['last_run_at' => $now];

        try {
            $set = $this->builder->build();

            $attributes += [
                'conflicts' => $set->conflicts,
                'node_not_configured' => $set->nodeNotConfigured,
                'not_proxied' => $set->notProxied,
                'unassigned' => $set->unassigned,
                'rule_count' => $set->ruleCount(),
                'allocation_rule_count' => $set->allocationRuleCount,
                'manual_rule_count' => $set->manualRuleCount,
            ];

            $pushed = false;

            if ($this->shouldPush($state, $set->hash, $force)) {
                $attributes['agent_status'] = $this->client->push($this->builder->wire($set->rules));
                $pushed = true;

                $attributes['last_pushed_hash'] = $set->hash;
                $attributes['last_pushed_at'] = $now;
            } else {
                // Nothing changed, so no nft apply. Still ask the agent whether it is
                // alive: without this, a healthy system with a 10 minute push
                // heartbeat looks "stale" to the 5 minute banner, and a dead agent
                // would stay invisible until the next heartbeat.
                $attributes['agent_status'] = $this->client->status();
            }

            // The peers call is made once per run and written down: the dashboard
            // banner and the Status page both need per-peer handshake ages, and
            // neither may make an HTTP call of its own.
            $peers = $this->peerSnapshot();

            if ($peers !== null) {
                $attributes['peer_health'] = $peers;
                $attributes['peer_health_at'] = $now;
            }

            // "Success" means the agent answered us in this run, push or no push.
            $attributes['last_success_at'] = $now;
            $attributes['last_error'] = null;

            $state->fill($attributes)->save();

            $this->forgetUsedJoinCodes($peers ?? []);

            return new SyncResult(
                ok: true,
                pushed: $pushed,
                ruleCount: $set->ruleCount(),
                allocationRuleCount: $set->allocationRuleCount,
                manualRuleCount: $set->manualRuleCount,
                aliasesRewritten: $set->aliasesRewritten,
                conflicts: $set->conflicts,
                nodeNotConfigured: $set->nodeNotConfigured,
                notProxied: $set->notProxied,
                unassigned: $set->unassigned,
                hash: $set->hash,
            );
        } catch (Throwable $exception) {
            $attributes['last_error'] = mb_substr($exception->getMessage(), 0, 2000);
            $state->fill($attributes)->save();

            Log::warning('autoproxy: sync failed', ['error' => $exception->getMessage()]);

            return new SyncResult(
                ok: false,
                pushed: false,
                ruleCount: (int) ($attributes['rule_count'] ?? 0),
                allocationRuleCount: (int) ($attributes['allocation_rule_count'] ?? 0),
                manualRuleCount: (int) ($attributes['manual_rule_count'] ?? 0),
                aliasesRewritten: 0,
                conflicts: $attributes['conflicts'] ?? [],
                nodeNotConfigured: $attributes['node_not_configured'] ?? [],
                notProxied: $attributes['not_proxied'] ?? [],
                unassigned: $attributes['unassigned'] ?? [],
                error: mb_substr($exception->getMessage(), 0, 2000),
            );
        }
    }

    /**
     * A join code is a one-time secret kept only so the admin can copy the join
     * command again. Once the peer has handshaked, the client holds it and our
     * copy is pure risk, so it is dropped on the first reconcile that sees a
     * handshake. Best effort: a VPS that will not answer must not fail the sync.
     */
    /**
     * The peers the VPS reports, keyed by id, or null when it would not answer.
     * Null and empty mean different things: null leaves the last snapshot alone,
     * an empty list is a VPS that genuinely has no peers.
     *
     * @return array<string, array<string, mixed>>|null
     */
    protected function peerSnapshot(): ?array
    {
        try {
            $snapshot = [];

            foreach ($this->client->peers() as $peer) {
                $id = (string) ($peer['id'] ?? '');

                if ($id === '') {
                    continue;
                }

                $age = $peer['handshake_age_s'] ?? null;

                $snapshot[$id] = [
                    'name' => (string) ($peer['name'] ?? $id),
                    'mode' => (string) ($peer['mode'] ?? ''),
                    'handshake_age_s' => is_numeric($age) ? (int) $age : null,
                ];
            }

            return $snapshot;
        } catch (Throwable $exception) {
            Log::debug('autoproxy: could not read peers', ['error' => $exception->getMessage()]);

            return null;
        }
    }

    /**
     * @param array<string, array<string, mixed>> $peers
     */
    protected function forgetUsedJoinCodes(array $peers): void
    {
        try {
            $nodes = NodeSetting::query()
                ->whereNotNull('encrypted_join_code')
                ->whereNotNull('peer_id')
                ->get();

            $looseCodes = AutoProxySettings::joinCodes();

            if ($nodes->isEmpty() && $looseCodes === []) {
                return;
            }

            $handshaked = [];
            foreach ($peers as $peerId => $peer) {
                if (($peer['handshake_age_s'] ?? null) !== null) {
                    $handshaked[(string) $peerId] = true;
                }
            }

            foreach ($nodes as $node) {
                if (isset($handshaked[(string) $node->peer_id])) {
                    $node->forgetJoinCode();
                }
            }

            foreach (array_keys($looseCodes) as $peerId) {
                if (isset($handshaked[$peerId])) {
                    AutoProxySettings::forgetJoinCode($peerId);
                }
            }
        } catch (Throwable $exception) {
            Log::debug('autoproxy: could not check peer handshakes', ['error' => $exception->getMessage()]);
        }
    }

    /**
     * Push when the desired state changed, when the agent has not had the full set
     * from us in a while (heartbeat, so a restarted agent is refilled), or when a
     * human asked. Measured on the last accepted push, not on the last contact.
     */
    protected function shouldPush(SyncState $state, string $hash, bool $force): bool
    {
        if ($force || $state->last_pushed_hash !== $hash || $state->last_pushed_at === null) {
            return true;
        }

        return $state->last_pushed_at->lt(
            Carbon::now()->subMinutes((int) config('autoproxy.heartbeat_minutes', 10))
        );
    }

    /** Nothing to sync before a VPS is connected; callers use this to stay quiet. */
    public function isConfigured(): bool
    {
        return AutoProxySettings::isConnected();
    }
}
