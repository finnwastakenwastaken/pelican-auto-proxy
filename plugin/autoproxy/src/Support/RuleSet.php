<?php

namespace Arrowtje\AutoProxy\Support;

/**
 * The desired state: what the agent should be applying right now, plus every
 * reason an allocation that asked to be published is not being forwarded.
 */
readonly class RuleSet
{
    /**
     * @param array<int, array<string, mixed>> $rules             rules that survived conflict detection
     * @param array<int, array<string, mixed>> $conflicts         dropped pairs, with a human reason
     * @param array<int, array<string, mixed>> $nodeNotConfigured proxied nodes whose mode is half set up
     * @param array<int, array<string, mixed>> $notProxied        allocations on nodes Auto Proxy is switched off for
     * @param array<int, array<string, mixed>> $unassigned        public alias, but no server assigned; port stays closed
     * @param array<int, array<string, mixed>> $allocations       display rows for the admin UI
     */
    public function __construct(
        public array $rules,
        public array $conflicts,
        public array $nodeNotConfigured,
        public array $notProxied,
        public array $unassigned,
        public array $allocations,
        public string $hash,
        public int $allocationRuleCount,
        public int $manualRuleCount,
        public int $aliasesRewritten,
    ) {}

    public function ruleCount(): int
    {
        return count($this->rules);
    }
}
