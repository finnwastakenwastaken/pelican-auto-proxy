<?php

namespace Arrowtje\AutoProxy\Filament\Admin\Widgets;

use Arrowtje\AutoProxy\Filament\Admin\Pages\AutoProxyStatus;
use Arrowtje\AutoProxy\Filament\Admin\Pages\Setup;
use Arrowtje\AutoProxy\Models\NodeSetting;
use Arrowtje\AutoProxy\Models\SyncState;
use Arrowtje\AutoProxy\Services\WingsRouteCheck;
use Arrowtje\AutoProxy\Support\AutoProxySettings;
use Arrowtje\AutoProxy\Support\PeerHealth;
use Arrowtje\AutoProxy\Support\WingsRoute;
use Filament\Widgets\Widget;
use Illuminate\Database\QueryException;
use Throwable;

/**
 * The cheapest visible failure signal: a block on the admin dashboard that says
 * exactly what is wrong. Hidden entirely when everything is healthy.
 */
class StaleSyncBanner extends Widget
{
    protected string $view = 'autoproxy::filament.admin.widgets.stale-sync-banner';

    protected int|string|array $columnSpan = 'full';

    /**
     * Filament loads widgets lazily by default, which for a red alert means the
     * dashboard paints "everything is fine" first and the warning arrives later,
     * or never if it is below the fold. It costs a few queries and a cache read,
     * no HTTP call and no DNS lookup, so it is rendered with the page.
     */
    protected static bool $isLazy = false;

    protected static ?int $sort = -10;

    /**
     * Rendered for every root admin, even when there is nothing wrong: a widget
     * that is not on the page cannot poll, so a banner gated on problems here
     * could only ever appear after a manual page reload. The view renders nothing
     * but the poll when the list is empty.
     */
    public static function canView(): bool
    {
        return user()?->isRootAdmin() ?? false;
    }

    /** @return string[] */
    public static function problems(): array
    {
        try {
            $state = SyncState::current();
        } catch (QueryException) {
            return [];
        } catch (Throwable) {
            return [];
        }

        // Nothing is configured yet: one hint, not a wall of red.
        if (!AutoProxySettings::isConnected()) {
            return ['Auto Proxy is installed but no VPS is connected yet, so nothing is being forwarded. Open Auto Proxy -> Setup and paste the code your VPS installer printed.'];
        }

        $problems = [];

        if (!$state->hasEverRun()) {
            $problems[] = 'The panel scheduler has not run autoproxy:sync yet, so nothing has ever been pushed to the VPS. Check that the panel\'s cron (schedule:run) and queue worker are running.';
        } elseif ($state->isStale(AutoProxySettings::staleAfterMinutes())) {
            $problems[] = $state->last_success_at === null
                ? 'The VPS has never answered the panel. Nothing new is being forwarded.'
                : sprintf(
                    'The VPS last answered %s (more than %d minutes ago). Ports that are already open keep working, but changes are not arriving.',
                    $state->last_success_at->diffForHumans(),
                    AutoProxySettings::staleAfterMinutes(),
                );
        }

        if (filled($state->last_error)) {
            $problems[] = 'Last error: ' . $state->last_error;
        }

        if (filled($state->conflicts)) {
            $problems[] = sprintf(
                '%d conflicting forward(s) are being withheld: two of them claim the same public port with different targets.',
                count((array) $state->conflicts),
            );
        }

        if (filled($state->node_not_configured)) {
            $problems[] = sprintf(
                '%d public allocation(s) are not forwarded because their node is only half set up (no tunnel client yet, or no LAN IP). See the Status page.',
                count((array) $state->node_not_configured),
            );
        }

        foreach (static::unhealthyPeers($state) as $row) {
            $problems[] = PeerHealth::message($row);
        }

        // The stored result of the last lookup, never a lookup of its own:
        // autoproxy:sync refreshes it every ten minutes in the background.
        foreach (WingsRouteCheck::current() as $row) {
            $problems[] = WingsRoute::shortMessage($row);
        }

        if (static::noNodeProxied()) {
            $problems[] = 'No node is proxied yet. Switch one on in Auto Proxy -> Setup, step 2, or nothing will ever be published.';
        } elseif (filled($state->not_proxied)) {
            $problems[] = sprintf(
                '%d allocation(s) carry the public alias on a node that is not proxied, so their ports stay closed.',
                count((array) $state->not_proxied),
            );
        }

        return $problems;
    }

    /**
     * Proxied nodes whose tunnel client is not connected. Read from the snapshot
     * the reconcile stored, never from the VPS: this runs on every dashboard.
     *
     * @return list<array{node_id: int|string, node: string, peer_id: string, minutes: int|null, known: bool}>
     */
    public static function unhealthyPeers(SyncState $state): array
    {
        try {
            return PeerHealth::unhealthy(
                $state->peerSnapshot(),
                NodeSetting::proxiedNodeMap(),
                $state->peerHealthAgeSeconds(),
                AutoProxySettings::staleAfterMinutes(),
            );
        } catch (Throwable) {
            return [];
        }
    }

    protected static function noNodeProxied(): bool
    {
        try {
            return !NodeSetting::query()->where('proxied', true)->exists();
        } catch (Throwable) {
            return false;
        }
    }

    protected function getViewData(): array
    {
        return [
            'problems' => static::problems(),
            'statusUrl' => AutoProxyStatus::getUrl(),
            'setupUrl' => Setup::getUrl(),
        ];
    }
}
