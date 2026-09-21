<?php

namespace Arrowtje\AutoProxy\Filament\Admin\Pages;

use App\Models\Node;
use Arrowtje\AutoProxy\Exceptions\AutoProxyException;
use Arrowtje\AutoProxy\Models\NodeSetting;
use Arrowtje\AutoProxy\Models\SyncState;
use Arrowtje\AutoProxy\Services\AgentClient;
use Arrowtje\AutoProxy\Services\SyncService;
use Arrowtje\AutoProxy\Filament\Admin\Widgets\StaleSyncBanner;
use Arrowtje\AutoProxy\Support\AutoProxySettings;
use Arrowtje\AutoProxy\Support\PeerHealth;
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

        return [
            'state' => $state,
            'connected' => AutoProxySettings::isConnected(),
            'agentStatus' => $agentStatus,
            'agentError' => $agentError,
            'peers' => $peers,
            'peerError' => $peerError,
            'peerNodeNames' => $this->peerNodeNames(),
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
