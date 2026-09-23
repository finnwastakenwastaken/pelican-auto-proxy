<?php

namespace Arrowtje\AutoProxy;

use App\Contracts\Plugins\HasPluginSettings;
use Arrowtje\AutoProxy\Exceptions\AutoProxyException;
use Arrowtje\AutoProxy\Filament\Admin\Pages\AutoProxyStatus;
use Arrowtje\AutoProxy\Filament\Admin\Pages\Setup;
use Arrowtje\AutoProxy\Filament\Admin\Resources\ForwardRules\ForwardRuleResource;
use Arrowtje\AutoProxy\Filament\Admin\Widgets\StaleSyncBanner;
use Arrowtje\AutoProxy\Models\SyncState;
use Arrowtje\AutoProxy\Services\AgentClient;
use Arrowtje\AutoProxy\Support\AutoProxySettings;
use Filament\Actions\Action;
use Filament\Contracts\Plugin;
use Filament\Notifications\Notification;
use Filament\Panel;
use Filament\Schemas\Components\Actions;
use Filament\Schemas\Components\Callout;
use Filament\Schemas\Components\Grid;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Components\Text;
use Filament\Support\Enums\FontFamily;
use Filament\Support\Enums\FontWeight;
use Filament\Support\Enums\IconPosition;
use Filament\Support\Enums\Size;
use Filament\Support\Enums\TextSize;
use Illuminate\Support\HtmlString;
use Throwable;

class AutoProxyPlugin implements HasPluginSettings, Plugin
{
    /** Where the docs live for a user reading them from inside the panel. */
    public const DOCS_BASE = 'https://github.com/finnwastakenwastaken/pelican-auto-proxy/blob/main/docs/';

    public function getId(): string
    {
        return 'autoproxy';
    }

    public function register(Panel $panel): void
    {
        // plugin.json limits this plugin to the admin panel.
        $panel->discoverResources(plugin_path($this->getId(), 'src/Filament/Admin/Resources'), 'Arrowtje\\AutoProxy\\Filament\\Admin\\Resources');
        $panel->discoverPages(plugin_path($this->getId(), 'src/Filament/Admin/Pages'), 'Arrowtje\\AutoProxy\\Filament\\Admin\\Pages');
        $panel->discoverWidgets(plugin_path($this->getId(), 'src/Filament/Admin/Widgets'), 'Arrowtje\\AutoProxy\\Filament\\Admin\\Widgets');
    }

    public function boot(Panel $panel): void {}

    /**
     * The modal reached from Settings -> Plugins. Everything editable still lives
     * on the Setup page, which can gate itself on root admin; this modal is
     * shown to anyone with the "update plugin" permission, so it holds no secret
     * and no editable field. What it does hold is the thing an admin opening it
     * actually wants: what this plugin is, whether it is working right now, where
     * the pages are, and what to try when it is not working.
     *
     * Every number here comes from the row the reconcile already wrote. A modal
     * that called the VPS would hang for its timeout behind a spinner every time
     * somebody opened the Plugins page.
     */
    public function getSettingsFormData(): array
    {
        return [];
    }

    public function getSettingsForm(): array
    {
        if (!$this->isRootAdmin()) {
            return [
                Callout::make('Root admins only')
                    ->description('Auto Proxy is configured by root admins, on its own Setup page. Nothing here is editable.')
                    ->warning(),
            ];
        }

        $connected = AutoProxySettings::isConnected();
        $problems = $connected ? StaleSyncBanner::problems() : [];

        // Every action in this modal chains ->button() after ->icon(): with
        // Pelican's icon button style on (the default for every user), its
        // global iconButton() otherwise renders them as bare icons, no text.
        return array_values(array_filter([
            $this->statusCallout($connected, $problems),

            $connected ? $this->glanceGrid() : null,

            Grid::make(1)
                ->schema([
                    Section::make('Pages')
                        ->icon('tabler-layout-grid')
                        ->compact()
                        ->schema([
                            Actions::make([
                                Action::make('autoproxy_setup')
                                    ->label('Setup')
                                    ->icon('tabler-settings')
                                    ->button()
                                    ->url(fn (): string => Setup::getUrl()),
                                Action::make('autoproxy_status')
                                    ->label('Status')
                                    ->icon('tabler-activity-heartbeat')
                                    ->button()
                                    ->url(fn (): string => AutoProxyStatus::getUrl()),
                                Action::make('autoproxy_forwards')
                                    ->label('Forwards')
                                    ->icon('tabler-arrows-right-left')
                                    ->button()
                                    ->url(fn (): string => ForwardRuleResource::getUrl()),
                            ]),
                        ]),
                    Section::make('Documentation')
                        ->description('Opens on GitHub, in a new tab.')
                        ->icon('tabler-book')
                        ->compact()
                        ->schema([
                            Actions::make(array_map(
                                static fn (array $doc): Action => Action::make('autoproxy_docs_' . $doc[0])
                                    ->label($doc[1])
                                    ->icon('tabler-external-link')
                                    ->iconPosition(IconPosition::After)
                                    ->button()
                                    ->outlined()
                                    ->color('gray')
                                    ->size(Size::Small)
                                    ->url(static::DOCS_BASE . $doc[0] . '.md', shouldOpenInNewTab: true),
                                [
                                    ['quickstart', 'Quickstart'],
                                    ['plugin', 'The plugin'],
                                    ['troubleshooting', 'Troubleshooting'],
                                    ['security', 'Security'],
                                    ['uninstall', 'Uninstall'],
                                ],
                            )),
                        ]),
                ]),

            $connected ? $this->apiSection() : null,

            Section::make('If something is not working')
                ->description('The five that come up most, and the first thing to check.')
                ->icon('tabler-lifebuoy')
                ->collapsible()
                ->collapsed($problems === [])
                ->schema($this->troubleshootingItems()),

            Section::make('About Auto Proxy')
                ->icon('tabler-info-circle')
                ->collapsible()
                ->collapsed($connected)
                ->schema([
                    Text::make('Auto Proxy publishes your Pelican servers through a cheap VPS, so players connect to the VPS address instead of your home IP. A WireGuard tunnel from each node to the VPS carries the traffic, and this plugin keeps the VPS forwarding table in step with your panel: give an allocation the public alias and its port opens, take the alias away and it closes.'),
                    Text::make('Nothing in this window is editable, so its Submit button does nothing. Everything is set on the Setup page.')
                        ->color('gray')
                        ->size(TextSize::Small),
                ]),
        ]));
    }

    /**
     * The first thing in the modal: is it working, and if not, what is wrong.
     * Uses the dashboard banner's own list, so the two can never disagree.
     *
     * @param  string[]  $problems
     */
    protected function statusCallout(bool $connected, array $problems): Callout
    {
        if (!$connected) {
            return Callout::make('Not connected to a VPS yet')
                ->description('Nothing is being forwarded. Open Setup and paste the code your VPS installer printed; it takes a minute.')
                ->info()
                ->actions([
                    Action::make('autoproxy_callout_setup')
                        ->label('Open Setup')
                        ->icon('tabler-settings')
                        ->button()
                        ->url(fn (): string => Setup::getUrl()),
                ]);
        }

        if ($problems === []) {
            return Callout::make('Everything is working')
                ->description('The VPS answers, every tunnel client is connected and the forwards match the panel.')
                ->success();
        }

        return Callout::make(count($problems) === 1 ? 'One thing needs attention' : count($problems) . ' things need attention')
            ->description(new HtmlString(implode('<br>', array_map(
                static fn (string $problem): string => (count($problems) > 1 ? '&bull; ' : '') . e($problem),
                $problems,
            ))))
            ->warning()
            ->actions([
                Action::make('autoproxy_callout_status')
                    ->label('Open Status')
                    ->icon('tabler-activity-heartbeat')
                    ->button()
                    ->color('warning')
                    ->url(fn (): string => AutoProxyStatus::getUrl()),
            ]);
    }

    /**
     * Four small cards. Reads the row the reconcile already wrote; never talks
     * to the VPS, so the modal cannot hang behind a spinner.
     */
    protected function glanceGrid(): ?Grid
    {
        try {
            $state = SyncState::current();
        } catch (Throwable) {
            return null;
        }

        $status = is_array($state->agent_status) ? $state->agent_status : [];

        $card = static fn (string $label, string $value, string $detail): Section => Section::make()
            ->compact()
            ->schema([
                Text::make($label)->color('gray')->size(TextSize::Small),
                Text::make($value)->weight(FontWeight::Bold)->size(TextSize::Large),
                Text::make($detail)->color('gray')->size(TextSize::ExtraSmall),
            ]);

        return Grid::make(['default' => 2, 'lg' => 4])
            ->schema([
                $card(
                    'Forwards',
                    (string) $state->rule_count,
                    sprintf('%d from allocations, %d manual', $state->allocation_rule_count, $state->manual_rule_count),
                ),
                $card(
                    'Tunnel clients',
                    sprintf('%s of %s', (string) ($status['peers']['healthy'] ?? '?'), (string) ($status['peers']['total'] ?? '?')),
                    'connected',
                ),
                $card(
                    'Last sync',
                    $state->last_success_at?->diffForHumans() ?? 'never',
                    'last change pushed ' . ($state->last_pushed_at?->diffForHumans() ?? 'never'),
                ),
                $card(
                    'VPS agent',
                    (string) ($status['version'] ?? 'unknown'),
                    'from the last sync',
                ),
            ]);
    }

    protected function apiSection(): Section
    {
        return Section::make('VPS API')
            ->description('Read-only. It comes from the VPS code you pasted in Setup.')
            ->icon('tabler-key')
            ->compact()
            ->schema([
                Text::make(AutoProxySettings::apiUrl())
                    ->fontFamily(FontFamily::Mono)
                    ->copyable(),
                Text::make('Rotating the API token invalidates every copy of the old one immediately, including any you wrote down or pasted into another panel. This panel switches to the new token in the same request.')
                    ->color('gray')
                    ->size(TextSize::Small),
                Actions::make([
                    Action::make('autoproxy_rotate_token')
                        ->label('Rotate API token')
                        ->icon('tabler-refresh')
                        ->button()
                        ->color('danger')
                        ->requiresConfirmation()
                        ->modalHeading('Rotate the VPS API token?')
                        ->modalDescription('The old token stops working the moment the VPS answers. This panel starts using the new one straight away; anything else that uses the old token will be refused.')
                        ->modalSubmitActionLabel('Rotate token')
                        ->action(function (): void {
                            try {
                                app(AgentClient::class)->rotateToken();
                            } catch (AutoProxyException $exception) {
                                Notification::make()
                                    ->title('The token was not rotated')
                                    ->body($exception->getMessage())
                                    ->danger()
                                    ->persistent()
                                    ->send();

                                return;
                            }

                            Notification::make()
                                ->title('New API token stored')
                                ->body('The panel is already using it. Any copy of the old token - in your notes, or in another panel - no longer works.')
                                ->success()
                                ->send();
                        }),
                ]),
            ]);
    }

    /**
     * @return array<int, Callout>
     */
    protected function troubleshootingItems(): array
    {
        $item = static fn (string $symptom, string $explanation, ?string $command = null): Callout => Callout::make($symptom)
            ->description($explanation)
            ->icon('tabler-help-circle')
            ->color('gray')
            ->footer($command === null ? [] : [
                Text::make($command)->fontFamily(FontFamily::Mono)->size(TextSize::Small)->copyable(),
            ]);

        return [
            $item(
                'A node shows "never connected" or an old handshake',
                'The tunnel client on that machine is not running. On that machine:',
                'systemctl status autoproxy-client; journalctl -u autoproxy-client -n 50',
            ),
            $item(
                'The port is open on the VPS but nothing answers',
                'The node host is dropping the forwarded traffic. Check its own firewall; on a Docker host, check the DOCKER-USER chain and that the allocation listens on the LAN address, not only on 127.0.0.1.',
            ),
            $item(
                'Players show up with the VPS address instead of their own',
                'That node is in site mode, which shares one address by design. Real player IPs need the tunnel client on the machine that publishes the game ports, in real-IP mode (Setup, step 2).',
            ),
            $item(
                'An allocation with the alias is ignored',
                'The alias must match the public address or one of the keywords exactly, the allocation must have a server assigned, and its node must be switched on in Setup step 2. The Forwards page says which of the three it is.',
            ),
            $item(
                'The banner says the scheduler has never run',
                'The panel\'s own cron and queue worker are not running, so nothing this plugin does takes effect. Check that schedule:run runs every minute and that a queue worker is up:',
                'php artisan schedule:list',
            ),
        ];
    }

    public function saveSettings(array $data): void
    {
        // Nothing here is editable; the Setup page owns every setting.
    }

    protected function isRootAdmin(): bool
    {
        return user()?->isRootAdmin() ?? false;
    }
}
