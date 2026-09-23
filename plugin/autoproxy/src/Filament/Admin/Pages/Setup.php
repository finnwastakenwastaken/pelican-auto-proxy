<?php

namespace Arrowtje\AutoProxy\Filament\Admin\Pages;

use App\Models\Node;
use Arrowtje\AutoProxy\Exceptions\AutoProxyException;
use Arrowtje\AutoProxy\Models\ClientUpdate;
use Arrowtje\AutoProxy\Models\NodeSetting;
use Arrowtje\AutoProxy\Services\AgentClient;
use Arrowtje\AutoProxy\Services\LatestRelease;
use Arrowtje\AutoProxy\Services\SyncService;
use Arrowtje\AutoProxy\Support\AutoProxySettings;
use Arrowtje\AutoProxy\Support\ClientInput;
use Arrowtje\AutoProxy\Support\ClientVersion;
use Arrowtje\AutoProxy\Support\VpsCode;
use BackedEnum;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Throwable;

/**
 * The whole setup on one page, in the order it has to happen: connect the VPS,
 * switch on the nodes you want published, then say what players should connect to.
 *
 * Deliberately plain Livewire (public properties, wire:model, wire:click) rather
 * than a Filament form schema: the node table is not an Eloquent table, half its
 * controls call the VPS, and every Filament abstraction here would be one more
 * thing that breaks silently on a panel upgrade.
 */
class Setup extends Page
{
    /**
     * A WireGuard peer with keepalive re-handshakes about every two minutes, so
     * three minutes without one means something is actually wrong.
     */
    public const STALE_HANDSHAKE_S = 180;

    protected static string|BackedEnum|null $navigationIcon = 'tabler-plug-connected';

    protected string $view = 'autoproxy::filament.admin.pages.setup';

    protected static ?string $slug = 'autoproxy/setup';

    protected static ?int $navigationSort = 1;

    /** Step 1: the code the VPS installer printed. Never kept after it is stored. */
    public string $vpsCode = '';

    /** Step 3. */
    public string $publicHostname = '';

    public string $keywords = '';

    public string $staleAfterMinutes = '5';

    /** @var array<int|string, string> node id => mode */
    public array $nodeMode = [];

    /** @var array<int|string, string> node id => LAN IP (site mode) */
    public array $nodeLanIp = [];

    /** @var array<int|string, string> node id => peer id to route through (site mode) */
    public array $nodeViaPeer = [];

    /** Step 2, site mode: a tunnel client on a LAN machine that is not a node. */
    public string $newClientName = '';

    public string $newClientCidrs = '';

    /** Per-request cache: the peers list costs an HTTPS round trip. */
    protected ?array $peerCache = null;

    public static function getNavigationLabel(): string
    {
        return 'Setup';
    }

    public static function getNavigationGroup(): ?string
    {
        return 'Auto Proxy';
    }

    public function getTitle(): string
    {
        return 'Auto Proxy setup';
    }

    /**
     * Any admin with a single permission can enter the admin panel, so this page
     * gates itself instead of relying on that. The VPS token and the join codes
     * on this page open ports on a public address.
     */
    public static function canAccess(): bool
    {
        return user()?->isRootAdmin() ?? false;
    }

    public function mount(): void
    {
        $this->publicHostname = AutoProxySettings::publicHostname();
        $this->keywords = implode(',', AutoProxySettings::keywords());
        $this->staleAfterMinutes = (string) AutoProxySettings::staleAfterMinutes();

        foreach (NodeSetting::map() as $nodeId => $setting) {
            $this->nodeMode[$nodeId] = $setting->mode;
            $this->nodeLanIp[$nodeId] = (string) $setting->lan_ip;
            $this->nodeViaPeer[$nodeId] = (string) $setting->via_peer_id;
        }
    }

    // --- step 1 -------------------------------------------------------------

    /** Validate the pasted code, store it, and immediately prove it works. */
    public function connectVps(): void
    {
        try {
            $code = VpsCode::decode($this->vpsCode);
            VpsCode::store($code);
        } catch (AutoProxyException $exception) {
            $this->fail('That VPS code was not accepted', $exception->getMessage());

            return;
        }

        // Do not keep the token sitting in the browser's Livewire state.
        $this->vpsCode = '';

        $this->testConnection();
    }

    public function testConnection(): void
    {
        try {
            $status = app(AgentClient::class)->status();
        } catch (AutoProxyException $exception) {
            $this->fail('The VPS did not answer', $exception->getMessage());

            return;
        }

        $peers = $status['peers'] ?? [];

        Notification::make()
            ->title('Connected to the VPS')
            ->body(sprintf(
                'Agent version %s, %s tunnel client(s) known, %s applying %s forward(s).',
                (string) ($status['version'] ?? 'unknown'),
                (string) ($peers['total'] ?? 0),
                AutoProxySettings::apiUrl(),
                (string) ($status['applied']['rules'] ?? 0),
            ))
            ->success()
            ->send();
    }

    // --- step 2 -------------------------------------------------------------

    /**
     * Switch a node on. In real mode this creates its peer on the VPS and keeps
     * the join code so the admin can copy the command again until it is used.
     */
    public function enableProxy(int $nodeId): void
    {
        $node = Node::query()->find($nodeId);

        if ($node === null) {
            $this->fail('Node not found', 'That node no longer exists in this panel.');

            return;
        }

        $setting = NodeSetting::forNode($nodeId);
        $mode = ($this->nodeMode[$nodeId] ?? $setting->mode) === NodeSetting::MODE_SITE
            ? NodeSetting::MODE_SITE
            : NodeSetting::MODE_REAL;

        if ($mode === NodeSetting::MODE_SITE) {
            $via = trim((string) ($this->nodeViaPeer[$nodeId] ?? ''));
            $lanIp = trim((string) ($this->nodeLanIp[$nodeId] ?? ''));

            if ($via === '') {
                $this->fail('Pick a tunnel client first', 'In site mode this node is reached over another machine\'s tunnel client. Choose which one, then switch it on.');

                return;
            }

            $setting->fill([
                'proxied' => true,
                'mode' => NodeSetting::MODE_SITE,
                'via_peer_id' => $via,
                'lan_ip' => $lanIp !== '' ? $lanIp : null,
            ])->save();

            $this->done($node->name . ' is now proxied over an existing tunnel client.');

            return;
        }

        $setting->fill(['mode' => NodeSetting::MODE_REAL]);

        if (blank($setting->peer_id)) {
            try {
                $created = app(AgentClient::class)->createPeer((string) $node->name, NodeSetting::MODE_REAL);
            } catch (AutoProxyException $exception) {
                $this->fail('The VPS could not add a tunnel client', $exception->getMessage());

                return;
            }

            $setting->peer_id = (string) ($created['peer']['id'] ?? '');
            $setting->encrypted_join_code = $created['join_code'];
            $setting->join_code_issued_at = now();

            if ($setting->peer_id === '') {
                $this->fail('The VPS returned no client id', 'The tunnel client may exist on the VPS without the panel knowing about it. Check Auto Proxy -> Status, and remove any stray client on the VPS.');

                return;
            }
        }

        $setting->proxied = true;
        $setting->save();

        $this->done('Run the join command below on ' . $node->name . '\'s machine to finish it.');
    }

    /**
     * Switch a node off. Deleting the peer closes its ports on the VPS at once,
     * which is the point, so the view asks for confirmation first.
     */
    public function disableProxy(int $nodeId): void
    {
        $setting = NodeSetting::forNode($nodeId);

        if ($setting->isReal() && filled($setting->peer_id)) {
            try {
                app(AgentClient::class)->deletePeer((string) $setting->peer_id);
            } catch (AutoProxyException $exception) {
                $this->fail('The VPS would not remove the tunnel client', $exception->getMessage() . ' Nothing was changed here, so you can try again.');

                return;
            }
        }

        $setting->fill([
            'proxied' => false,
            'peer_id' => null,
            'encrypted_join_code' => null,
            'join_code_issued_at' => null,
        ])->save();

        $this->done('That node is no longer proxied. Its ports on the VPS are closed.');
    }

    /** Save mode / LAN IP / via-client without touching the VPS. */
    public function saveNode(int $nodeId): void
    {
        $setting = NodeSetting::forNode($nodeId);
        $mode = ($this->nodeMode[$nodeId] ?? $setting->mode) === NodeSetting::MODE_SITE
            ? NodeSetting::MODE_SITE
            : NodeSetting::MODE_REAL;

        $lanIp = trim((string) ($this->nodeLanIp[$nodeId] ?? ''));

        if ($mode === NodeSetting::MODE_SITE && $lanIp !== '' && filter_var($lanIp, FILTER_VALIDATE_IP, FILTER_FLAG_IPV4) === false) {
            $this->fail('That is not an IPv4 address', 'Give the address this node\'s machine has on its own LAN, for example 10.0.0.10.');

            return;
        }

        $setting->fill([
            'mode' => $mode,
            'lan_ip' => $lanIp !== '' ? $lanIp : null,
            'via_peer_id' => trim((string) ($this->nodeViaPeer[$nodeId] ?? '')) ?: null,
        ])->save();

        $this->done('Saved. The next sync (within a minute) uses it.');
    }

    /**
     * New key for this node's client. The old join code and any client still
     * using it stop working immediately - that is what makes it useful.
     */
    public function regenerateJoinCode(int $nodeId): void
    {
        $setting = NodeSetting::forNode($nodeId);

        if (blank($setting->peer_id)) {
            $this->fail('No tunnel client yet', 'Switch this node on first; that is what creates its client on the VPS.');

            return;
        }

        try {
            $joinCode = app(AgentClient::class)->rotatePeer((string) $setting->peer_id);
        } catch (AutoProxyException $exception) {
            $this->fail('The VPS would not issue a new join code', $exception->getMessage());

            return;
        }

        $setting->fill(['encrypted_join_code' => $joinCode, 'join_code_issued_at' => now()])->save();

        $this->done('New join code. Run the command below again on that machine; the old code no longer works.');
    }

    /**
     * A tunnel client on a machine that is not a Pelican node: the way traffic
     * reaches anything else on that LAN (site mode nodes, manual forwards).
     * Created in site mode, so the VPS masquerades and the LAN host answers.
     */
    public function addSiteClient(): void
    {
        // Shared with `php artisan autoproxy:setup add-client`, so the page and
        // the command accept and refuse exactly the same input.
        $parsed = ClientInput::parse($this->newClientName, $this->newClientCidrs);

        if ($parsed['error'] !== null) {
            $this->fail($parsed['error']['title'], $parsed['error']['body']);

            return;
        }

        $name = $parsed['name'];
        $cidrs = $parsed['cidrs'];

        try {
            $created = app(AgentClient::class)->createPeer($name, NodeSetting::MODE_SITE, $cidrs);
        } catch (AutoProxyException $exception) {
            $this->fail('The VPS could not add that client', $exception->getMessage());

            return;
        }

        $peerId = (string) ($created['peer']['id'] ?? '');

        if ($peerId === '') {
            $this->fail('The VPS returned no client id', 'Check Auto Proxy -> Status for a stray client on the VPS.');

            return;
        }

        AutoProxySettings::setJoinCode($peerId, $created['join_code']);

        $this->newClientName = '';
        $this->newClientCidrs = '';
        $this->peerCache = null;

        $this->done('Run the join command below on that machine.');
    }

    /** Remove a client that is not tied to a node, and forget anything pointing at it. */
    public function removeClient(string $peerId): void
    {
        try {
            app(AgentClient::class)->deletePeer($peerId);
        } catch (AutoProxyException $exception) {
            $this->fail('The VPS would not remove that client', $exception->getMessage() . ' Nothing was changed here.');

            return;
        }

        AutoProxySettings::forgetJoinCode($peerId);

        // Nodes routed through it are now pointing at nothing; say so loudly by
        // switching them off rather than leaving a forward that silently fails.
        NodeSetting::query()->where('via_peer_id', $peerId)->update([
            'via_peer_id' => null,
            'proxied' => false,
        ]);

        $this->peerCache = null;

        $this->done('That client is gone and its ports are closed. Any node that used it has been switched off.');
    }

    public function regenerateClientCode(string $peerId): void
    {
        try {
            $joinCode = app(AgentClient::class)->rotatePeer($peerId);
        } catch (AutoProxyException $exception) {
            $this->fail('The VPS would not issue a new join code', $exception->getMessage());

            return;
        }

        AutoProxySettings::setJoinCode($peerId, $joinCode);

        $this->done('New join code. Run the command below again on that machine; the old code no longer works.');
    }

    // --- step 3 -------------------------------------------------------------

    public function savePublicAddress(): void
    {
        $hostname = trim($this->publicHostname);

        AutoProxySettings::setMany([
            'public_hostname' => $hostname,
            'keywords' => trim($this->keywords) !== '' ? trim($this->keywords) : 'proxy,public',
            'stale_after_minutes' => (string) max(1, min(1440, (int) $this->staleAfterMinutes)),
        ]);

        $this->done($hostname === ''
            ? 'Saved. Public allocations will show the VPS IP ' . AutoProxySettings::endpointIp() . '.'
            : 'Saved. Public allocations will show ' . $hostname . '.');
    }

    public function syncNow(): void
    {
        $result = app(SyncService::class)->run(true);

        $result->ok
            ? $this->done($result->summary())
            : $this->fail('Sync failed', (string) $result->error);
    }

    // --- view data ----------------------------------------------------------

    protected function getViewData(): array
    {
        $connected = AutoProxySettings::isConnected();
        $peers = $connected ? $this->peers() : [];
        $peersById = [];

        foreach ($peers as $peer) {
            $peersById[(string) ($peer['id'] ?? '')] = $peer;
        }

        $settings = NodeSetting::map();
        $nodes = [];

        foreach (Node::query()->orderBy('name')->get() as $node) {
            $setting = $settings[$node->id] ?? null;
            $nodes[] = $this->nodeRow($node, $setting, $peersById);
        }

        return [
            'connected' => $connected,
            'apiUrl' => AutoProxySettings::apiUrl(),
            'endpointIp' => AutoProxySettings::endpointIp(),
            'certPath' => AutoProxySettings::certPath(),
            'agentVersion' => AutoProxySettings::agentVersionFromCode(),
            'peers' => $peers,
            'peerError' => $this->peerError,
            'peerOptions' => $this->peerOptions($peers),
            'nodes' => $nodes,
            'siteClients' => $this->siteClients($peers),
            'statusUrl' => AutoProxyStatus::getUrl(),
            'anyProxied' => collect($nodes)->contains(fn (array $row) => $row['proxied']),
            'installCommand' => 'curl -fsSL ' . AutoProxySettings::releaseUrl() . '/install-vps.sh | sudo bash',
            'docs' => [
                'vps' => AutoProxySettings::docsUrl('install-vps.md'),
                'client' => AutoProxySettings::docsUrl('install-client.md'),
                'plugin' => AutoProxySettings::docsUrl('plugin.md'),
            ],
        ];
    }

    protected ?string $peerError = null;

    /** @return array<int, array<string, mixed>> */
    protected function peers(): array
    {
        if ($this->peerCache !== null) {
            return $this->peerCache;
        }

        try {
            return $this->peerCache = app(AgentClient::class)->peers();
        } catch (AutoProxyException $exception) {
            $this->peerError = $exception->getMessage();

            return $this->peerCache = [];
        } catch (Throwable $exception) {
            $this->peerError = $exception->getMessage();

            return $this->peerCache = [];
        }
    }

    /**
     * Clients that serve a LAN rather than a node, with their join command while
     * it is still needed.
     *
     * @param array<int, array<string, mixed>> $peers
     * @return array<int, array<string, mixed>>
     */
    protected function siteClients(array $peers): array
    {
        $usedByNode = [];

        foreach (NodeSetting::map() as $setting) {
            if (filled($setting->peer_id)) {
                $usedByNode[(string) $setting->peer_id] = true;
            }
        }

        $codes = AutoProxySettings::joinCodes();
        $clients = [];

        foreach ($peers as $peer) {
            $id = (string) ($peer['id'] ?? '');

            if ($id === '' || isset($usedByNode[$id])) {
                continue;
            }

            $joinCode = $codes[$id] ?? null;
            $age = $peer['handshake_age_s'] ?? null;

            $clients[] = [
                'id' => $id,
                'name' => (string) ($peer['name'] ?? $id),
                'tunnel_ip' => (string) ($peer['tunnel_ip'] ?? '?'),
                'mode' => (string) ($peer['mode'] ?? 'site'),
                'lan_cidrs' => implode(', ', array_map('strval', (array) ($peer['lan_cidrs'] ?? []))),
                'status' => $age === null
                    ? ['text' => 'Waiting for the first handshake', 'tone' => 'warning']
                    : ((int) $age <= self::STALE_HANDSHAKE_S
                        ? ['text' => 'Connected, last seen ' . (int) $age . 's ago', 'tone' => 'success']
                        : ['text' => 'Stale: last seen ' . $this->humanAge((int) $age) . ' ago', 'tone' => 'danger']),
                'join_command' => $joinCode === null ? null : AutoProxySettings::joinCommand($joinCode),
                'compose' => $joinCode === null ? null : $this->composeSnippet($joinCode),
                'client' => $this->clientInfo($peer),
            ];
        }

        return $clients;
    }

    /**
     * Site-mode clients only. This picker fills a rule's `via_peer`, and the VPS
     * refuses a via_peer that is in real-IP mode ("peer %q is in real-IP mode:
     * forward to it with target_peer, not target_ip"). Offering a real-IP client
     * here would only produce a rejection at push time, on a page that cannot
     * explain it.
     *
     * @param array<int, array<string, mixed>> $peers
     * @return array<string, string> peer id => label for the site-mode picker
     */
    protected function peerOptions(array $peers): array
    {
        $options = [];

        foreach ($peers as $peer) {
            $id = (string) ($peer['id'] ?? '');
            if ($id === '' || (string) ($peer['mode'] ?? '') !== NodeSetting::MODE_SITE) {
                continue;
            }

            $options[$id] = trim(sprintf('%s (%s)', (string) ($peer['name'] ?? $id), (string) ($peer['tunnel_ip'] ?? '?')));
        }

        return $options;
    }

    /**
     * @param array<string, array<string, mixed>> $peersById
     * @return array<string, mixed>
     */
    protected function nodeRow(Node $node, ?NodeSetting $setting, array $peersById): array
    {
        $mode = $this->nodeMode[$node->id] ?? $setting?->mode ?? NodeSetting::MODE_REAL;
        $peerId = (string) ($setting?->peer_id ?? '');
        $viaPeerId = (string) ($this->nodeViaPeer[$node->id] ?? $setting?->via_peer_id ?? '');
        $watched = $mode === NodeSetting::MODE_SITE ? $viaPeerId : $peerId;
        $joinCode = $setting?->joinCode();

        return [
            'id' => $node->id,
            'name' => $node->name,
            'proxied' => (bool) ($setting?->proxied ?? false),
            'mode' => $mode,
            'peer_id' => $peerId,
            'via_peer_id' => $viaPeerId,
            'lan_ip' => (string) ($setting?->lan_ip ?? ''),
            'join_code' => $joinCode,
            'status' => $this->nodeStatus($setting, $mode, $watched, $peersById),
            'join_command' => $joinCode === null ? null : AutoProxySettings::joinCommand($joinCode),
            'compose' => $joinCode === null ? null : $this->composeSnippet($joinCode),
            // Only a node with its own client has a client version to show; a
            // site-mode node's client is listed under "other machines".
            'client' => ($setting?->proxied && $mode === NodeSetting::MODE_REAL && isset($peersById[$peerId]))
                ? $this->clientInfo($peersById[$peerId])
                : null,
        ];
    }

    /** @var array{latest: string|null, allowed: array<string, bool>}|null */
    protected ?array $clientContext = null;

    /**
     * The client version line for step 2. Updating is done on the Status page;
     * this only says where things stand, so the admin sees it where they look.
     *
     * @param array<string, mixed> $peer
     * @return array<string, mixed>
     */
    protected function clientInfo(array $peer): array
    {
        $this->clientContext ??= [
            'latest' => LatestRelease::version(),
            'allowed' => ClientUpdate::allowedMap(),
        ];

        return ClientVersion::describe(
            $peer,
            $this->clientContext['latest'],
            $this->clientContext['allowed'][(string) ($peer['id'] ?? '')] ?? false,
            AutoProxySettings::releaseUrl(),
            time(),
        );
    }

    /**
     * One sentence per node, because "is it working" is the only question this
     * table exists to answer.
     *
     * @param array<string, array<string, mixed>> $peersById
     * @return array{text: string, tone: string}
     */
    protected function nodeStatus(?NodeSetting $setting, string $mode, string $peerId, array $peersById): array
    {
        if (!($setting?->proxied ?? false)) {
            return ['text' => 'Not proxied', 'tone' => 'muted'];
        }

        if ($peerId === '') {
            return $mode === NodeSetting::MODE_SITE
                ? ['text' => 'No tunnel client chosen', 'tone' => 'danger']
                : ['text' => 'No tunnel client yet', 'tone' => 'danger'];
        }

        $peer = $peersById[$peerId] ?? null;

        if ($peer === null) {
            return ['text' => 'The VPS does not know this client', 'tone' => 'danger'];
        }

        $age = $peer['handshake_age_s'] ?? null;

        if ($age === null) {
            return ['text' => 'Waiting for the first handshake', 'tone' => 'warning'];
        }

        $age = (int) $age;

        return $age <= self::STALE_HANDSHAKE_S
            ? ['text' => 'Connected, last seen ' . $age . 's ago', 'tone' => 'success']
            : ['text' => 'Stale: last seen ' . $this->humanAge($age) . ' ago', 'tone' => 'danger'];
    }

    public function humanAge(int $seconds): string
    {
        if ($seconds < 120) {
            return $seconds . 's';
        }

        if ($seconds < 7200) {
            return (int) round($seconds / 60) . ' minutes';
        }

        return (int) round($seconds / 3600) . ' hours';
    }

    protected function composeSnippet(string $joinCode): string
    {
        return implode("\n", [
            'services:',
            '  autoproxy-client:',
            '    image: ' . (string) config('autoproxy.docker_image'),
            '    network_mode: host',
            '    cap_add: [NET_ADMIN]',
            '    restart: unless-stopped',
            '    environment:',
            '      AUTOPROXY_JOIN_CODE: "' . $joinCode . '"',
        ]);
    }

    protected function done(string $body): void
    {
        Notification::make()->title('Done')->body($body)->success()->send();
    }

    protected function fail(string $title, string $body): void
    {
        Notification::make()->title($title)->body($body)->danger()->persistent()->send();
    }
}
