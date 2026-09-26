<?php

namespace Arrowtje\AutoProxy\Filament\Admin\Pages;

use App\Models\Node;
use Arrowtje\AutoProxy\Exceptions\AutoProxyException;
use Arrowtje\AutoProxy\Models\ClientUpdate;
use Arrowtje\AutoProxy\Models\NodeSetting;
use Arrowtje\AutoProxy\Models\SyncState;
use Arrowtje\AutoProxy\Services\AgentClient;
use Arrowtje\AutoProxy\Services\LatestRelease;
use Arrowtje\AutoProxy\Services\SyncService;
use Arrowtje\AutoProxy\Services\WingsRouteCheck;
use Arrowtje\AutoProxy\Filament\Admin\Widgets\StaleSyncBanner;
use Arrowtje\AutoProxy\Support\AutoProxySettings;
use Arrowtje\AutoProxy\Support\ClientVersion;
use Arrowtje\AutoProxy\Support\PeerHealth;
use Arrowtje\AutoProxy\Support\WingsRoute;
use BackedEnum;
use Filament\Actions\Action;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Illuminate\Support\Carbon;

/**
 * "Is it working, and if not, what exactly is wrong." Everything here is read
 * live from the VPS or from the last reconcile; nothing is cached optimistically.
 */
class AutoProxyStatus extends Page
{
    protected static string|BackedEnum|null $navigationIcon = 'tabler-activity-heartbeat';

    protected string $view = 'autoproxy::filament.admin.pages.status';

    protected static ?string $slug = 'autoproxy/status';

    protected static ?int $navigationSort = 2;

    /**
     * Filament pages have no polling of their own (only chart and stats widgets
     * carry CanPoll), so the refresh is a wire:poll on the page root in the view.
     * Every poll re-runs getViewData(), which is two calls to the VPS with the
     * configured short timeout - cheap, and only while somebody is looking.
     */
    public const POLL_SECONDS = 30;

    public static function getNavigationLabel(): string
    {
        return 'Status';
    }

    public static function getNavigationGroup(): ?string
    {
        return 'Auto Proxy';
    }

    public function getTitle(): string
    {
        return 'Auto Proxy status';
    }

    /**
     * Any admin with a single permission can enter the admin panel, so this page
     * gates itself instead of relying on that.
     */
    public static function canAccess(): bool
    {
        return user()?->isRootAdmin() ?? false;
    }

    protected function getHeaderActions(): array
    {
        return [
            Action::make('sync')
                ->label('Sync now')
                ->icon('tabler-refresh')
                ->button()
                ->action(function (): void {
                    $result = app(SyncService::class)->run(true);

                    $notification = Notification::make()->title($result->ok ? 'Sync finished' : 'Sync failed');

                    $result->ok
                        ? $notification->body($result->summary())->success()
                        : $notification->body((string) $result->error)->danger()->persistent();

                    $notification->send();
                }),
            Action::make('test')
                ->label('Test VPS')
                ->icon('tabler-plug')
                ->button()
                ->color('gray')
                ->action(function (): void {
                    try {
                        $status = app(AgentClient::class)->status();

                        Notification::make()
                            ->title('The VPS answered')
                            ->body(sprintf(
                                'version %s, %d forward(s) applied, %d tunnel client(s)',
                                (string) ($status['version'] ?? '?'),
                                (int) ($status['applied']['rules'] ?? 0),
                                (int) ($status['peers']['total'] ?? 0),
                            ))
                            ->success()
                            ->send();
                    } catch (AutoProxyException $exception) {
                        Notification::make()
                            ->title('The VPS is unreachable')
                            ->body($exception->getMessage())
                            ->danger()
                            ->persistent()
                            ->send();
                    }
                }),
        ];
    }

    // --- client updates ------------------------------------------------------

    /**
     * Livewire calls these with whatever arguments the browser sends, and a page's
     * canAccess() is only checked when it is first opened, so every action here
     * checks again rather than trusting that.
     */
    protected function guard(): void
    {
        abort_unless(static::canAccess(), 403);
    }

    /** @return array<string, mixed>|null the peer as the VPS reports it now */
    protected function livePeer(string $peerId): ?array
    {
        try {
            foreach (app(AgentClient::class)->peers() as $peer) {
                if ((string) ($peer['id'] ?? '') === $peerId) {
                    return $peer;
                }
            }
        } catch (AutoProxyException $exception) {
            $this->notify('The VPS did not answer', $exception->getMessage(), false);

            return null;
        }

        $this->notify('Unknown tunnel client', 'The VPS does not know that client any more.', false);

        return null;
    }

    public function allowRemoteUpdates(string $peerId): void
    {
        $this->guard();

        if ($this->livePeer($peerId) === null) {
            return;
        }

        ClientUpdate::setAllowed($peerId, true);
        $this->notify('Remote updates allowed', 'This client can now be updated from this page. Nothing is installed until you press Update.', true);
    }

    /**
     * Switching off also withdraws a pending request on the VPS, and refuses to
     * flip the switch if the VPS cannot be told: a switch that reads "off" while
     * a request is still waiting on the VPS would be a lie.
     */
    public function disallowRemoteUpdates(string $peerId): void
    {
        $this->guard();
        $peer = $this->livePeer($peerId);

        if ($peer === null) {
            return;
        }

        if (($peer['client']['desired_version'] ?? null) !== null) {
            try {
                app(AgentClient::class)->setClientVersion($peerId, null);
            } catch (AutoProxyException $exception) {
                $this->notify('Could not withdraw the pending update', $exception->getMessage() . ' Remote updates stay on until the VPS can be told.', false);

                return;
            }
        }

        ClientUpdate::setAllowed($peerId, false);
        $this->notify('Remote updates off', 'This client will not be asked to update from here.', true);
    }

    public function requestClientUpdate(string $peerId): void
    {
        $this->guard();

        if (!ClientUpdate::isAllowed($peerId)) {
            $this->notify('Remote updates are off for this client', 'Allow them first.', false);

            return;
        }

        $peer = $this->livePeer($peerId);

        if ($peer === null) {
            return;
        }

        $latest = LatestRelease::version();
        $info = ClientVersion::describe($peer, $latest, true, AutoProxySettings::releaseUrl(), time());

        if (!$info['can_remote'] || $latest === null) {
            $this->notify('This client cannot be updated from here right now', $info['remote_note'] ?? ('It already runs ' . ($info['version'] ?? '?') . '.'), false);

            return;
        }

        try {
            app(AgentClient::class)->setClientVersion($peerId, $latest);
        } catch (AutoProxyException $exception) {
            $this->notify('The VPS did not take the request', $exception->getMessage(), false);

            return;
        }

        $this->notify('Update requested', 'The client installs ' . $latest . ' at its next check-in, within about three minutes. Progress shows here.', true);
    }

    public function withdrawClientUpdate(string $peerId): void
    {
        $this->guard();

        try {
            app(AgentClient::class)->setClientVersion($peerId, null);
        } catch (AutoProxyException $exception) {
            $this->notify('Could not withdraw the request', $exception->getMessage(), false);

            return;
        }

        $this->notify('Request withdrawn', 'An update that already started finishes; one that has not is not started.', true);
    }

    public function checkLatestRelease(): void
    {
        $this->guard();
        LatestRelease::forget();
        $latest = LatestRelease::get();

        $latest['version'] !== null
            ? $this->notify('Latest release: ' . $latest['version'], 'Looked up at ' . LatestRelease::url(), true)
            : $this->notify('Could not look up the latest release', (string) $latest['error'], false);
    }

    /**
     * Look every node's hostname up again now, for an admin who just added a
     * hosts entry and wants to see the warning go. Bounded by
     * WingsRouteCheck::BUDGET_SECONDS; the page itself never looks anything up.
     */
    public function recheckWingsRoute(): void
    {
        $this->guard();

        try {
            $checked = WingsRouteCheck::refresh();
        } catch (\Throwable $exception) {
            $this->notify('Could not check the nodes\' hostnames', $exception->getMessage(), false);

            return;
        }

        $count = count($checked['findings']);

        $count === 0
            ? $this->notify('The panel reaches every node directly', 'No node\'s hostname resolves to the VPS from this panel any more.', true)
            : $this->notify($count . ' node(s) still reached through the VPS', 'Their hostnames still resolve to the VPS from this panel. A hosts entry in a Docker panel needs the container re-created.', false);
    }

    protected function notify(string $title, string $body, bool $ok): void
    {
        $notification = Notification::make()->title($title)->body($body);
        $ok ? $notification->success() : $notification->danger()->persistent();
        $notification->send();
    }

    /**
     * @param array<int, array<string, mixed>> $peers
     * @param array<string, string> $names
     * @return array<int, array<string, mixed>>
     */
    protected function clientRows(array $peers, array $names, ?string $latest): array
    {
        $allowed = ClientUpdate::allowedMap();
        $rows = [];

        foreach ($peers as $peer) {
            $id = (string) ($peer['id'] ?? '');

            if ($id === '') {
                continue;
            }

            $rows[] = [
                'id' => $id,
                'name' => (string) ($peer['name'] ?? $id),
                'node' => $names[$id] ?? null,
                'allowed' => $allowed[$id] ?? false,
                'info' => ClientVersion::describe($peer, $latest, $allowed[$id] ?? false, AutoProxySettings::releaseUrl(), time()),
            ];
        }

        return $rows;
    }

    protected function getViewData(): array
    {
        $state = SyncState::current();

        $agentStatus = null;
        $agentError = null;
        $peers = [];
        $peerError = null;

        if (AutoProxySettings::isConnected()) {
            try {
                $agentStatus = app(AgentClient::class)->status();
            } catch (AutoProxyException $exception) {
                $agentError = $exception->getMessage();
            }

            try {
                $peers = app(AgentClient::class)->peers();
            } catch (AutoProxyException $exception) {
                $peerError = $exception->getMessage();
            }
        }

        $names = $this->peerNodeNames();
        $latest = AutoProxySettings::isConnected() ? LatestRelease::get() : ['version' => null, 'error' => null];

        return [
            'clientRows' => $this->clientRows($peers, $names, $latest['version']),
            'latestRelease' => $latest['version'],
            'latestError' => $latest['error'],
            'state' => $state,
            'connected' => AutoProxySettings::isConnected(),
            'agentStatus' => $agentStatus,
            'agentError' => $agentError,
            'peers' => $peers,
            'peerError' => $peerError,
            'peerNodeNames' => $names,
            'setupUrl' => Setup::getUrl(),
            'apiUrl' => AutoProxySettings::apiUrl(),
            'publicAddress' => AutoProxySettings::publicAddress(),
            'keywords' => AutoProxySettings::keywords(),
            'staleAfterMinutes' => AutoProxySettings::staleAfterMinutes(),
            'conflicts' => $state->conflicts ?? [],
            'nodeNotConfigured' => $state->node_not_configured ?? [],
            'notProxied' => $state->not_proxied ?? [],
            'unassigned' => $state->unassigned ?? [],
            'unhealthyPeers' => array_map(
                static fn (array $row): array => $row + ['message' => PeerHealth::message($row)],
                StaleSyncBanner::unhealthyPeers($state),
            ),
            'wingsDetours' => array_map(
                static fn (array $row): array => $row + ['message' => WingsRoute::message($row, AutoProxySettings::endpointIp())],
                WingsRouteCheck::current(),
            ),
            'wingsCheckedAt' => ($checked = WingsRouteCheck::cached()) !== null ? Carbon::createFromTimestamp($checked['checked_at']) : null,
            'wingsDocsUrl' => AutoProxySettings::docsUrl(WingsRoute::DOCS_PAGE),
            'wingsTroubleshootingUrl' => AutoProxySettings::docsUrl('troubleshooting.md#nodes-flicker-offline-or-the-console-page-shows-403-errors'),
            'refreshedAt' => Carbon::now(),
            'pollSeconds' => static::POLL_SECONDS,
            'publicAllocationCount' => $state->allocation_rule_count
                + count($state->node_not_configured ?? [])
                + count($state->not_proxied ?? [])
                + count($state->unassigned ?? []),
        ];
    }

    /**
     * peer id => the node(s) it serves, so a peer on the VPS can be traced back
     * to something the admin recognises instead of an opaque id.
     *
     * @return array<string, string>
     */
    protected function peerNodeNames(): array
    {
        $names = Node::query()->pluck('name', 'id')->all();
        $map = [];

        foreach (NodeSetting::query()->get() as $setting) {
            $nodeName = (string) ($names[$setting->node_id] ?? ('node ' . $setting->node_id));

            foreach ([$setting->peer_id, $setting->via_peer_id] as $index => $peerId) {
                if (blank($peerId)) {
                    continue;
                }

                $label = $nodeName . ($index === 1 ? ' (site mode)' : '');
                $map[(string) $peerId] = isset($map[(string) $peerId])
                    ? $map[(string) $peerId] . ', ' . $label
                    : $label;
            }
        }

        return $map;
    }
}
