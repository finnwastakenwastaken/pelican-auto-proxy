<?php

namespace Arrowtje\AutoProxy\Support;

readonly class SyncResult
{
    /**
     * @param array<int, array<string, mixed>> $conflicts
     * @param array<int, array<string, mixed>> $nodeNotConfigured
     * @param array<int, array<string, mixed>> $notProxied
     * @param array<int, array<string, mixed>> $unassigned
     */
    public function __construct(
        public bool $ok,
        public bool $pushed,
        public int $ruleCount,
        public int $allocationRuleCount,
        public int $manualRuleCount,
        public int $aliasesRewritten,
        public array $conflicts,
        public array $nodeNotConfigured,
        public array $notProxied = [],
        public array $unassigned = [],
        public ?string $error = null,
        public ?string $hash = null,
    ) {}

    public function summary(): string
    {
        if (!$this->ok) {
            return 'Sync failed: ' . $this->error;
        }

        $summary = $this->pushed
            ? sprintf('Pushed %d forward(s) to the VPS', $this->ruleCount)
            : sprintf('No change; %d forward(s) already applied', $this->ruleCount);

        if ($this->aliasesRewritten > 0) {
            $summary .= sprintf(', rewrote %d alias(es)', $this->aliasesRewritten);
        }

        if ($this->conflicts !== []) {
            $summary .= sprintf(', %d conflict(s)', count($this->conflicts));
        }

        if ($this->nodeNotConfigured !== []) {
            $summary .= sprintf(', %d allocation(s) skipped (node half configured)', count($this->nodeNotConfigured));
        }

        if ($this->notProxied !== []) {
            $summary .= sprintf(', %d on nodes that are not proxied', count($this->notProxied));
        }

        if ($this->unassigned !== []) {
            $summary .= sprintf(', %d unassigned (alias set, no server)', count($this->unassigned));
        }

        return $summary;
    }
}
