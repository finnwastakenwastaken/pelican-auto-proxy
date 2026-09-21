<?php

namespace Arrowtje\AutoProxy\Console\Commands;

use Arrowtje\AutoProxy\Services\SyncService;
use Arrowtje\AutoProxy\Support\AutoProxySettings;
use Illuminate\Console\Command;

class SyncCommand extends Command
{
    protected $signature = 'autoproxy:sync {--force : Push even when nothing changed}';

    protected $description = 'Reconcile public allocations and manual forwards with the Auto Proxy VPS agent';

    public function handle(SyncService $sync): int
    {
        if (!AutoProxySettings::isConnected()) {
            // Not an error: the plugin is installed but no VPS has been connected
            // yet. Saying so beats a stack trace every minute in the log.
            $this->line('No VPS connected yet. Open Auto Proxy -> Setup in the admin panel and paste the VPS code.');

            return self::SUCCESS;
        }

        $result = $sync->run((bool) $this->option('force'));

        if (!$result->ok) {
            $this->error($result->summary());

            return self::FAILURE;
        }

        $this->info($result->summary());

        foreach ($result->conflicts as $conflict) {
            $this->warn(sprintf('conflict: %s vs %s (%s) - %s', $conflict['a'], $conflict['b'], $conflict['ports'], $conflict['reason']));
        }

        foreach ($result->nodeNotConfigured as $entry) {
            $this->warn(sprintf('skipped allocation %s on %s: %s', $entry['allocation_id'], $entry['node'], $entry['reason']));
        }

        foreach ($result->notProxied as $entry) {
            $this->line(sprintf('allocation %s on %s: that node is not proxied, so nothing is forwarded', $entry['allocation_id'], $entry['node']));
        }

        foreach ($result->unassigned as $entry) {
            $this->line(sprintf('unassigned allocation %s on %s: alias rewritten, no server assigned - port stays closed', $entry['allocation_id'], $entry['node']));
        }

        return self::SUCCESS;
    }
}
