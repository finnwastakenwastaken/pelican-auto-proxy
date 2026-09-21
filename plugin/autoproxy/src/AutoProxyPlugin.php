<?php

namespace Arrowtje\AutoProxy;

use App\Contracts\Plugins\HasPluginSettings;
use Arrowtje\AutoProxy\Exceptions\AutoProxyException;
use Arrowtje\AutoProxy\Filament\Admin\Pages\AutoProxyStatus;
use Arrowtje\AutoProxy\Filament\Admin\Pages\Setup;
use Arrowtje\AutoProxy\Filament\Admin\Resources\ForwardRules\ForwardRuleResource;
use Arrowtje\AutoProxy\Models\SyncState;
use Arrowtje\AutoProxy\Services\AgentClient;
use Arrowtje\AutoProxy\Support\AutoProxySettings;
use Filament\Actions\Action;
use Filament\Contracts\Plugin;
use Filament\Notifications\Notification;
use Filament\Panel;
use Filament\Schemas\Components\Actions;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Components\Text;
use Filament\Schemas\Components\UnorderedList;
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
                Text::make('Auto Proxy is configured by root admins only, on its own Setup page. Nothing here is editable.')
                    ->color('warning'),
            ];
        }

        return [
            Section::make('What this plugin does')
                ->schema([
                    Text::make('Auto Proxy publishes your Pelican servers through a cheap VPS, so players connect to the VPS address instead of your home IP. A WireGuard tunnel from each node to the VPS carries the traffic, and this plugin keeps the VPS forwarding table in step with your panel: give an allocation the public alias and its port opens, take the alias away and it closes.'),
                    Text::make('Nothing on this page is editable. Everything is set on the Setup page.')
                        ->color('gray'),
                ]),

            Section::make('Right now')
                ->schema(array_map(
                    static fn (string $line): Text => Text::make($line),
                    $this->stateLines(),
                )),

            Section::make('Pages')
                ->schema([
                    Actions::make([
                        Action::make('autoproxy_setup')
                            ->label('Setup')
                            ->icon('tabler-settings')
                            ->url(fn (): string => Setup::getUrl()),
                        Action::make('autoproxy_status')
                            ->label('Status')
                            ->icon('tabler-activity-heartbeat')
                            ->url(fn (): string => AutoProxyStatus::getUrl()),
                        Action::make('autoproxy_forwards')
                            ->label('Forwards')
                            ->icon('tabler-arrows-right-left')
                            ->url(fn (): string => ForwardRuleResource::getUrl()),
                    ]),
                ]),

            Section::make('Documentation')
                ->description('Opens on GitHub, in a new tab.')
                ->schema([
                    Actions::make(array_map(
                        static fn (array $doc): Action => Action::make('autoproxy_docs_' . $doc[0])
                            ->label($doc[1])
                            ->icon('tabler-book')
                            ->color('gray')
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

            Section::make('If something is not working')
                ->description('The five that come up most, and the first thing to run.')
                ->schema([
                    UnorderedList::make([
                        Text::make('A node shows "never connected" or an old handshake: the tunnel client on that machine is not running. On that machine: systemctl status autoproxy-client, then journalctl -u autoproxy-client -n 50.'),
                        Text::make('The port is open on the VPS but nothing answers: the node host is dropping the forwarded traffic. Check its own firewall, and on a Docker host check the DOCKER-USER chain and that the allocation really listens on the LAN address, not only on 127.0.0.1.'),
                        Text::make('Players show up with the VPS address instead of their own: that node is in site mode, which shares one address by design. Real player IPs need the tunnel client on the machine that publishes the game ports, in real-IP mode (Setup, step 2).'),
                        Text::make('An allocation with the alias is ignored: the alias must match the public address or one of the keywords exactly, the allocation must have a server assigned, and its node must be switched on in Setup step 2. The Forwards page names which of the three it is.'),
                        Text::make('The banner says the scheduler has never run: the panel\'s own cron and queue worker are not running, so nothing this plugin does takes effect. Check that the panel container runs schedule:run every minute (php artisan schedule:list) and that a queue worker is up.'),
                    ])
                        // UnorderedList defaults to two columns from sm up; these are
                        // sentences, not labels, and read as one list.
                        ->columns(1)
                        ->size('sm'),
                ]),

            Section::make('VPS API')
                ->schema($this->apiLines()),
        ];
    }

    /**
     * The at-a-glance block. Reads one row; never talks to the VPS.
     *
     * @return string[]
     */
    protected function stateLines(): array
    {
        if (!AutoProxySettings::isConnected()) {
            return ['No VPS is connected yet, so nothing is being forwarded. Open the Setup page and paste the code your VPS installer printed.'];
        }

        try {
            $state = SyncState::current();
        } catch (Throwable) {
            return ['A VPS is connected. The plugin could not read its own sync state - check the panel log.'];
        }

        $status = is_array($state->agent_status) ? $state->agent_status : [];

        return [
            sprintf(
                'VPS connected. Agent version %s, %s forward(s) applied.',
                (string) ($status['version'] ?? 'unknown'),
                (string) ($status['applied']['rules'] ?? '?'),
            ),
            sprintf(
                'Tunnel clients: %s of %s connected.',
                (string) ($status['peers']['healthy'] ?? '?'),
                (string) ($status['peers']['total'] ?? '?'),
            ),
            sprintf(
                'Forwards from the panel: %d (%d from allocations, %d manual).',
                $state->rule_count,
                $state->allocation_rule_count,
                $state->manual_rule_count,
            ),
            sprintf(
                'Last successful sync: %s. Last push: %s.',
                $state->last_success_at?->diffForHumans() ?? 'never',
                $state->last_pushed_at?->diffForHumans() ?? 'never',
            ),
            filled($state->last_error)
                ? 'Last error: ' . $state->last_error
                : 'No error on the last run.',
        ];
    }

    /**
     * @return array<int, Actions|Text>
     */
    protected function apiLines(): array
    {
        $apiUrl = AutoProxySettings::apiUrl();

        if ($apiUrl === '') {
            return [
                Text::make('No VPS is connected yet, so there is no API address and no token to rotate.')
                    ->color('gray'),
            ];
        }

        return [
            Text::make('VPS API: ' . $apiUrl . ' (read-only; it comes from the VPS code you pasted in Setup).'),
            Text::make('Rotating invalidates every copy of the old token immediately, including one written down elsewhere. The panel starts using the new one in the same request.')
                ->color('gray'),
            Actions::make([
                Action::make('autoproxy_rotate_token')
                    ->label('Rotate the API token now')
                    ->icon('tabler-key')
                    ->color('danger')
                    ->requiresConfirmation()
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
