<?php

namespace Arrowtje\AutoProxy\Console\Commands;

use App\Models\Node;
use Arrowtje\AutoProxy\Exceptions\AutoProxyException;
use Arrowtje\AutoProxy\Filament\Admin\Resources\ForwardRules\ForwardRuleResource;
use Arrowtje\AutoProxy\Models\ForwardRule;
use Arrowtje\AutoProxy\Models\NodeSetting;
use Arrowtje\AutoProxy\Services\AgentClient;
use Arrowtje\AutoProxy\Support\AutoProxySettings;
use Arrowtje\AutoProxy\Support\ClientInput;
use Arrowtje\AutoProxy\Support\ForwardRuleInput;
use Arrowtje\AutoProxy\Support\VpsCode;
use Illuminate\Console\Command;

/**
 * The Setup page, without a browser.
 *
 * Everything here is also on Auto Proxy -> Setup, and that page stays the
 * documented way to do it. This exists for panels nobody logs into by hand:
 * an unattended install, a scripted rebuild, a headless box where the admin UI
 * is reachable only through a tunnel they would rather not open. It calls the
 * same services the page calls, so the two cannot drift apart in behaviour.
 *
 * Join codes and the VPS code carry secrets, so `store-code` prefers a file
 * over an argument (an argument is visible in `ps` and in shell history), and
 * `join-command` says out loud what it is printing.
 *
 * Every check a value has to pass is shared with the admin UI rather than
 * repeated here: ClientInput for a new site client, ForwardRuleInput for a
 * manual forward, and ForwardRuleResource's peer lookups for the two pickers.
 * A second copy of those rules would drift the first time one of them changed,
 * and the VPS would then refuse something the CLI had just accepted.
 */
class SetupCommand extends Command
{
    protected $signature = 'autoproxy:setup
        {action : store-code, test, nodes, enable-node, disable-node, join-command, clients, add-client, remove-client, client-join-command, forwards, add-forward or remove-forward}
        {target? : the node id or name, the tunnel client (peer) id, or the forward id, depending on the action}
        {--code= : the VPS code (prefer --code-file; an argument is visible in ps)}
        {--code-file= : read the VPS code from this file}
        {--mode=real : real (tunnel client on that node\'s machine) or site (via another client)}
        {--via-peer= : site mode: the peer id whose client reaches this node}
        {--lan-ip= : site mode: this node\'s address on that client\'s LAN}
        {--name= : add-client, add-forward: the name to give it}
        {--lan-cidrs= : add-client: the LAN ranges this client serves, comma separated, e.g. 10.0.0.0/24}
        {--proto=both : add-forward: tcp, udp or both}
        {--public-port= : add-forward: the port on the VPS}
        {--public-port-end= : add-forward: the last port of a 1:1 range}
        {--target-ip= : add-forward: the private LAN address to send it to}
        {--via= : add-forward: the site client that sits on that LAN}
        {--target-port= : add-forward: a different port on the target (not allowed on a range)}
        {--notes= : add-forward: free text kept in the panel}
        {--disabled : add-forward: create it switched off}
        {--yes : confirm an action that invalidates a running client\'s key}';

    protected $description = 'Drive the Auto Proxy setup from the command line, without the admin UI';

    public function handle(): int
    {
        return match ((string) $this->argument('action')) {
            'store-code' => $this->storeCode(),
            'test' => $this->test(),
            'nodes' => $this->nodes(),
            'enable-node' => $this->enableNode(),
            'disable-node' => $this->disableNode(),
            'join-command' => $this->joinCommand(),
            'clients' => $this->clients(),
            'add-client' => $this->addClient(),
            'remove-client' => $this->removeClient(),
            'client-join-command' => $this->clientJoinCommand(),
            'forwards' => $this->forwards(),
            'add-forward' => $this->addForward(),
            'remove-forward' => $this->removeForward(),
            default => $this->unknownAction(),
        };
    }

    protected function unknownAction(): int
    {
        $this->error('Unknown action "' . $this->argument('action') . '".');
        $this->line('Use one of: store-code, test, nodes, enable-node, disable-node, join-command,');
        $this->line('clients, add-client, remove-client, client-join-command, forwards, add-forward, remove-forward.');

        return self::INVALID;
    }

    /** Step 1: store the code the VPS installer printed, then prove it works. */
    protected function storeCode(): int
    {
        $file = (string) $this->option('code-file');
        $raw = (string) $this->option('code');

        if ($file !== '') {
            if (!is_readable($file)) {
                $this->error('Cannot read ' . $file . '.');

                return self::FAILURE;
            }

            $raw = (string) file_get_contents($file);
        }

        $raw = trim($raw);

        if ($raw === '') {
            $this->error('Give the VPS code with --code-file=<path> (preferred) or --code=<code>.');
            $this->line('The VPS installer printed it, and `autoproxy-agent show-code` reprints it on the VPS.');

            return self::INVALID;
        }

        try {
            VpsCode::store(VpsCode::decode($raw));
        } catch (AutoProxyException $exception) {
            $this->error('That VPS code was not accepted: ' . $exception->getMessage());

            return self::FAILURE;
        }

        $this->info('VPS code stored. The token is encrypted in the database; the certificate is at ' . AutoProxySettings::certPath() . '.');

        return $this->test();
    }

    /** Ask the VPS for its status, the same call the page's Test connection makes. */
    protected function test(): int
    {
        if (!$this->requireConnected()) {
            return self::FAILURE;
        }

        try {
            $status = app(AgentClient::class)->status();
        } catch (AutoProxyException $exception) {
            $this->error('The VPS did not answer: ' . $exception->getMessage());

            return self::FAILURE;
        }

        $peers = $status['peers'] ?? [];

        $this->info(sprintf(
            'Connected to %s. Agent %s, %d tunnel client(s), %d healthy, %d forward(s) applied.',
            AutoProxySettings::apiUrl(),
            (string) ($status['version'] ?? 'unknown'),
            (int) ($peers['total'] ?? 0),
            (int) ($peers['healthy'] ?? 0),
            (int) ($status['applied']['rules'] ?? 0),
        ));

        if (filled($status['last_error'] ?? null)) {
            $this->warn('The VPS reports: ' . $status['last_error']);
        }

        return self::SUCCESS;
    }

    /** What the node table on the Setup page shows, as text. */
    protected function nodes(): int
    {
        $settings = NodeSetting::map();
        $rows = [];

        foreach (Node::query()->orderBy('name')->get() as $node) {
            $setting = $settings[$node->id] ?? null;

            $rows[] = [
                $node->id,
                $node->name,
                ($setting?->proxied ?? false) ? 'yes' : 'no',
                $setting?->mode ?? NodeSetting::MODE_REAL,
                (string) ($setting?->peer_id ?? ''),
                $setting?->joinCode() === null ? '' : 'stored',
            ];
        }

        if ($rows === []) {
            $this->line('This panel has no nodes yet.');

            return self::SUCCESS;
        }

        $this->table(['id', 'name', 'proxied', 'mode', 'peer id', 'join code'], $rows);

        return self::SUCCESS;
    }

    /**
     * Step 2: switch a node on. In real mode this creates its tunnel client on
     * the VPS and keeps the join code until that client first connects.
     */
    protected function enableNode(): int
    {
        $node = $this->node();

        if ($node === null) {
            return self::FAILURE;
        }

        $mode = (string) $this->option('mode') === NodeSetting::MODE_SITE
            ? NodeSetting::MODE_SITE
            : NodeSetting::MODE_REAL;

        $setting = NodeSetting::forNode((int) $node->id);

        if ($mode === NodeSetting::MODE_SITE) {
            $via = trim((string) $this->option('via-peer'));

            if ($via === '') {
                $this->error('In site mode this node is reached over another machine\'s tunnel client. Pass --via-peer=<peer id>.');

                return self::INVALID;
            }

            $lanIp = trim((string) $this->option('lan-ip'));

            if ($lanIp !== '' && filter_var($lanIp, FILTER_VALIDATE_IP, FILTER_FLAG_IPV4) === false) {
                $this->error('--lan-ip must be an IPv4 address, for example 10.0.0.10.');

                return self::INVALID;
            }

            $setting->fill([
                'proxied' => true,
                'mode' => NodeSetting::MODE_SITE,
                'via_peer_id' => $via,
                'lan_ip' => $lanIp !== '' ? $lanIp : null,
            ])->save();

            $this->info($node->name . ' is now proxied over an existing tunnel client.');

            return self::SUCCESS;
        }

        if (!$this->requireConnected()) {
            return self::FAILURE;
        }

        $setting->fill(['mode' => NodeSetting::MODE_REAL]);

        if (blank($setting->peer_id)) {
            try {
                $created = app(AgentClient::class)->createPeer((string) $node->name, NodeSetting::MODE_REAL);
            } catch (AutoProxyException $exception) {
                $this->error('The VPS could not add a tunnel client: ' . $exception->getMessage());

                return self::FAILURE;
            }

            $peerId = (string) ($created['peer']['id'] ?? '');

            if ($peerId === '') {
                $this->error('The VPS returned no client id. It may exist there without this panel knowing; check Auto Proxy -> Status and remove any stray client on the VPS.');

                return self::FAILURE;
            }

            $setting->peer_id = $peerId;
            $setting->encrypted_join_code = $created['join_code'];
            $setting->join_code_issued_at = now();
        }

        $setting->proxied = true;
        $setting->save();

        $this->info($node->name . ' is proxied. Tunnel client id ' . $setting->peer_id . '.');
        $this->line('Run `php artisan autoproxy:setup join-command ' . $node->id . '` to get the command for that machine.');

        return self::SUCCESS;
    }

    /** Switch a node off: its client is removed and its ports close at once. */
    protected function disableNode(): int
    {
        $node = $this->node();

        if ($node === null) {
            return self::FAILURE;
        }

        $setting = NodeSetting::forNode((int) $node->id);

        if ($setting->isReal() && filled($setting->peer_id)) {
            try {
                app(AgentClient::class)->deletePeer((string) $setting->peer_id);
            } catch (AutoProxyException $exception) {
                $this->error('The VPS would not remove the tunnel client: ' . $exception->getMessage() . ' Nothing was changed here, so you can try again.');

                return self::FAILURE;
            }
        }

        $setting->fill([
            'proxied' => false,
            'peer_id' => null,
            'encrypted_join_code' => null,
            'join_code_issued_at' => null,
        ])->save();

        $this->info($node->name . ' is no longer proxied. Its ports on the VPS are closed.');

        return self::SUCCESS;
    }

    /** Print the join command for a node, exactly as the Setup page shows it. */
    protected function joinCommand(): int
    {
        $node = $this->node();

        if ($node === null) {
            return self::FAILURE;
        }

        $setting = NodeSetting::forNode((int) $node->id);
        $joinCode = $setting->joinCode();

        if ($joinCode === null) {
            $this->error('No join code is stored for ' . $node->name . '.');
            $this->line(blank($setting->peer_id)
                ? 'Switch the node on first: that is what creates its tunnel client.'
                : 'Its client has already connected, so the panel deleted its copy. Issue a new one on the Setup page with Regenerate join code.');

            return self::FAILURE;
        }

        $this->printJoinCommand($joinCode);

        return self::SUCCESS;
    }

    // --- tunnel clients that are not nodes ----------------------------------

    /**
     * The same list the Setup page shows under "clients on a LAN", as text:
     * every client the VPS knows, and which node owns it if any. This is where
     * the peer ids the other actions take come from.
     */
    protected function clients(): int
    {
        if (!$this->requireConnected()) {
            return self::FAILURE;
        }

        try {
            $peers = app(AgentClient::class)->peers();
        } catch (AutoProxyException $exception) {
            $this->error('The VPS did not answer: ' . $exception->getMessage());

            return self::FAILURE;
        }

        $ownedByNode = $this->peerIdsOwnedByNodes();
        $codes = AutoProxySettings::joinCodes();
        $rows = [];

        foreach ($peers as $peer) {
            $id = (string) ($peer['id'] ?? '');
            $age = $peer['handshake_age_s'] ?? null;

            $rows[] = [
                $id,
                (string) ($peer['name'] ?? ''),
                (string) ($peer['mode'] ?? ''),
                (string) ($peer['tunnel_ip'] ?? ''),
                implode(', ', array_map('strval', (array) ($peer['lan_cidrs'] ?? []))),
                $age === null ? 'never joined' : (int) $age . 's ago',
                $ownedByNode[$id] ?? '',
                isset($codes[$id]) ? 'stored' : '',
            ];
        }

        if ($rows === []) {
            $this->line('The VPS knows no tunnel clients yet.');

            return self::SUCCESS;
        }

        $this->table(['peer id', 'name', 'mode', 'tunnel ip', 'lan ranges', 'last handshake', 'node', 'join code'], $rows);

        return self::SUCCESS;
    }

    /**
     * A tunnel client on a machine that is not a Pelican node: how traffic
     * reaches anything else on that LAN (site-mode nodes, manual forwards).
     * Same call, same validation and same join command as the Setup page's
     * "add a client" box.
     */
    protected function addClient(): int
    {
        if (!$this->requireConnected()) {
            return self::FAILURE;
        }

        $parsed = ClientInput::parse((string) $this->option('name'), (string) $this->option('lan-cidrs'));

        if ($parsed['error'] !== null) {
            $this->error($parsed['error']['title'] . '.');
            $this->line($parsed['error']['body']);
            $this->line('Example: php artisan autoproxy:setup add-client --name="office switch" --lan-cidrs=10.0.0.0/24');

            return self::INVALID;
        }

        try {
            $created = app(AgentClient::class)->createPeer($parsed['name'], NodeSetting::MODE_SITE, $parsed['cidrs']);
        } catch (AutoProxyException $exception) {
            // The VPS has the last word on a LAN range: it refuses one that
            // covers its own public IP or the tunnel subnet, and says why in a
            // sentence written for a person. Print that sentence as it came
            // back - the panel does not know either value, so any wording of
            // our own here would be a guess, and a generic "could not add the
            // client" would throw away the only useful part of the answer.
            $this->error('The VPS refused that tunnel client. It said:');

            // AgentClient already prefixes a 422 with "The VPS refused that: "
            // for the admin UI, where the sentence appears on its own with no
            // heading above it. Here it does have one, so the prefix would
            // read twice in two lines. Strip it and print only the VPS's own
            // words.
            $message = preg_replace('/^The VPS refused that:\s*/', '', $exception->getMessage()) ?? $exception->getMessage();

            foreach (preg_split('/\R/', $message) ?: [] as $line) {
                $this->line($line);
            }

            return self::FAILURE;
        }

        $peerId = (string) ($created['peer']['id'] ?? '');

        if ($peerId === '') {
            $this->error('The VPS returned no client id. Check Auto Proxy -> Status for a stray client on the VPS.');

            return self::FAILURE;
        }

        AutoProxySettings::setJoinCode($peerId, $created['join_code']);

        $this->info('Tunnel client created.');
        $this->line('  peer id:    ' . $peerId);
        $this->line('  tunnel ip:  ' . (string) ($created['peer']['tunnel_ip'] ?? '?'));
        $this->line('  lan ranges: ' . implode(', ', array_map('strval', (array) ($created['peer']['lan_cidrs'] ?? $parsed['cidrs']))));
        $this->newLine();
        $this->printJoinCommand($created['join_code']);

        return self::SUCCESS;
    }

    /** Remove a client that is not tied to a node, exactly as the Setup page does. */
    protected function removeClient(): int
    {
        $peerId = $this->peerId();

        if ($peerId === null) {
            return self::FAILURE;
        }

        $owner = $this->peerIdsOwnedByNodes()[$peerId] ?? null;

        if ($owner !== null) {
            $this->error('That client belongs to node ' . $owner . '.');
            $this->line('Remove it with `php artisan autoproxy:setup disable-node ' . $owner . '`, which also clears the panel\'s copy.');

            return self::INVALID;
        }

        try {
            app(AgentClient::class)->deletePeer($peerId);
        } catch (AutoProxyException $exception) {
            $this->error('The VPS would not remove that client: ' . $exception->getMessage() . ' Nothing was changed here.');

            return self::FAILURE;
        }

        AutoProxySettings::forgetJoinCode($peerId);

        // Nodes routed through it now point at nothing. Switching them off says
        // so loudly, instead of leaving a forward that silently fails.
        $orphaned = NodeSetting::query()->where('via_peer_id', $peerId)->update([
            'via_peer_id' => null,
            'proxied' => false,
        ]);

        $this->info('That client is gone and its ports are closed.');

        if ($orphaned > 0) {
            $this->warn($orphaned . ' node(s) reached through it have been switched off; they had nothing left to route over.');
        }

        return self::SUCCESS;
    }

    /**
     * The join command for a client that is not a node.
     *
     * A join code is shown once and then only lives here. If the panel still has
     * it, printing it costs nothing. If it does not, the only way to get a
     * working one is a new keypair - which kicks off whatever client is running
     * on that code right now - so that path says so and needs --yes.
     */
    protected function clientJoinCommand(): int
    {
        $peerId = $this->peerId();

        if ($peerId === null) {
            return self::FAILURE;
        }

        $owner = $this->peerIdsOwnedByNodes()[$peerId] ?? null;

        if ($owner !== null) {
            $this->error('That client belongs to node ' . $owner . '.');
            $this->line('Use `php artisan autoproxy:setup join-command ' . $owner . '` instead; it reads that node\'s stored code.');

            return self::INVALID;
        }

        $stored = AutoProxySettings::joinCodes()[$peerId] ?? null;

        if ($stored !== null) {
            $this->printJoinCommand($stored);

            return self::SUCCESS;
        }

        $this->warn('The panel has no join code for this client, and a join code cannot be read back from the VPS.');
        $this->warn('The only way to get one is a NEW KEYPAIR for this client. That breaks it immediately:');
        $this->warn('any machine currently connected on the old key stops passing traffic until it is re-run with the new command.');

        if (!$this->option('yes')) {
            $this->newLine();
            $this->error('Nothing was changed.');
            $this->line('Re-run with --yes if you accept that, for example:');
            $this->line('  php artisan autoproxy:setup client-join-command ' . $peerId . ' --yes');

            return self::INVALID;
        }

        try {
            $joinCode = app(AgentClient::class)->rotatePeer($peerId);
        } catch (AutoProxyException $exception) {
            $this->error('The VPS would not issue a new join code: ' . $exception->getMessage());

            return self::FAILURE;
        }

        AutoProxySettings::setJoinCode($peerId, $joinCode);

        $this->info('New keypair issued. The old join code no longer works.');
        $this->printJoinCommand($joinCode);

        return self::SUCCESS;
    }

    // --- manual forwards ----------------------------------------------------

    /** The Forwards list, manual rows only: Pelican allocations are never rows here. */
    protected function forwards(): int
    {
        $rows = [];

        foreach (ForwardRule::query()->orderBy('public_port')->get() as $rule) {
            $rows[] = [
                $rule->id,
                $rule->name,
                $rule->protocol,
                $rule->portLabel(),
                $rule->targetLabel(),
                $rule->enabled ? 'yes' : 'no',
            ];
        }

        if ($rows === []) {
            $this->line('No manual forwards. Pelican allocations with the public alias are forwarded automatically.');

            return self::SUCCESS;
        }

        $this->table(['id', 'name', 'protocol', 'public port', 'target', 'enabled'], $rows);

        return self::SUCCESS;
    }

    /**
     * A forward to an address on a LAN, through a site client. Every value is
     * checked with ForwardRuleInput, the same helper the Forwards form uses, so
     * the command refuses what the form refuses, in the same words.
     */
    protected function addForward(): int
    {
        $name = (string) $this->option('name');
        $protocol = strtolower(trim((string) $this->option('proto')));
        $publicPort = trim((string) $this->option('public-port'));
        $publicPortEnd = trim((string) $this->option('public-port-end'));
        $targetIp = trim((string) $this->option('target-ip'));
        $via = trim((string) $this->option('via'));
        $targetPort = trim((string) $this->option('target-port'));

        $errors = array_filter([
            ForwardRuleInput::nameError($name),
            ForwardRuleInput::protocolError($protocol),
            ForwardRuleInput::publicPortError($publicPort),
            ForwardRuleInput::publicPortEndError($publicPortEnd === '' ? null : $publicPortEnd, $publicPort),
            ForwardRuleInput::targetPortError($targetPort === '' ? null : $targetPort, $publicPortEnd === '' ? null : $publicPortEnd),
        ]);

        if ($via === '') {
            $errors[] = 'Say which tunnel client sits on that LAN with --via=<peer id>. `php artisan autoproxy:setup clients` lists them.';
        }

        if ($errors !== []) {
            foreach ($errors as $error) {
                $this->error($error);
            }

            return self::INVALID;
        }

        // The same two lookups the form's pickers use: site clients only for
        // --via (the agent refuses a real-IP peer there), and that client's LAN
        // ranges to check --target-ip against. An unreachable VPS returns an
        // empty list from both, and then neither test can be made - so say that
        // rather than pretending the value passed.
        $siteClients = ForwardRuleResource::peerOptions(NodeSetting::MODE_SITE);

        if ($siteClients === []) {
            $this->error('The VPS listed no site-mode tunnel clients, so --via cannot be checked and the forward would be refused at push time.');
            $this->line('Check `php artisan autoproxy:setup test`, then `php artisan autoproxy:setup clients`.');

            return self::FAILURE;
        }

        if (!array_key_exists($via, $siteClients)) {
            $this->error('"' . $via . '" is not a site-mode tunnel client on this VPS.');
            $this->line('Site clients: ' . implode(', ', array_keys($siteClients)));

            return self::INVALID;
        }

        $cidrs = ForwardRuleResource::peerLanCidrs()[$via] ?? [];
        $ipError = ForwardRuleInput::targetIpError($targetIp === '' ? null : $targetIp, $cidrs);

        if ($ipError !== null) {
            $this->error($ipError);

            return self::INVALID;
        }

        $rule = ForwardRule::create([
            'name' => trim($name),
            'protocol' => $protocol,
            'public_port' => (int) $publicPort,
            'public_port_end' => $publicPortEnd === '' ? null : (int) $publicPortEnd,
            'target_peer' => null,
            'target_ip' => $targetIp,
            'via_peer' => $via,
            'target_port' => $targetPort === '' ? null : (int) $targetPort,
            'enabled' => !$this->option('disabled'),
            'notes' => trim((string) $this->option('notes')) ?: null,
        ]);

        $this->info('Forward #' . $rule->id . ' created: ' . $rule->protocol . ' ' . $rule->portLabel() . ' -> ' . $rule->targetLabel() . ($rule->enabled ? '' : ' (disabled)'));

        return $this->pushNow();
    }

    /** Delete a manual forward. Its port closes on the VPS as soon as the push lands. */
    protected function removeForward(): int
    {
        $target = trim((string) $this->argument('target'));

        if ($target === '' || !ctype_digit($target)) {
            $this->error('Which forward? Pass its id; `php artisan autoproxy:setup forwards` lists them.');

            return self::INVALID;
        }

        $rule = ForwardRule::query()->find((int) $target);

        if ($rule === null) {
            $this->error('There is no manual forward #' . $target . '. `php artisan autoproxy:setup forwards` lists them.');

            return self::FAILURE;
        }

        $label = $rule->protocol . ' ' . $rule->portLabel() . ' -> ' . $rule->targetLabel();
        $rule->delete();

        $this->info('Forward #' . $target . ' deleted (' . $label . ').');

        return $this->pushNow();
    }

    /**
     * Push straight away, the way the Forwards page does after a save, so the
     * result is visible now rather than within a minute. A failed push is not a
     * failed command: the row is saved and the next reconcile retries it.
     */
    protected function pushNow(): int
    {
        ForwardRuleResource::sync();

        $this->line('Pushed to the VPS. `php artisan autoproxy:sync` and Auto Proxy -> Status show what it applied.');

        return self::SUCCESS;
    }

    // --- shared bits --------------------------------------------------------

    /** peer id => node name, for every client this panel created for a node. */
    protected function peerIdsOwnedByNodes(): array
    {
        $names = Node::query()->pluck('name', 'id')->all();
        $owned = [];

        foreach (NodeSetting::map() as $nodeId => $setting) {
            if (filled($setting->peer_id)) {
                $owned[(string) $setting->peer_id] = (string) ($names[$nodeId] ?? $nodeId);
            }
        }

        return $owned;
    }

    protected function peerId(): ?string
    {
        $target = trim((string) $this->argument('target'));

        if ($target === '') {
            $this->error('Which tunnel client? Pass its peer id; `php artisan autoproxy:setup clients` lists them.');

            return null;
        }

        return $target;
    }

    /** One rendering of the install line, shared with the Setup page. */
    protected function printJoinCommand(string $joinCode): void
    {
        $this->warn('The line below contains that client\'s private key. Treat it like a password.');
        $this->newLine();
        $this->line(AutoProxySettings::joinCommand($joinCode));
    }

    protected function node(): ?Node
    {
        $target = trim((string) $this->argument('target'));

        if ($target === '') {
            $this->error('Which node? Pass its id or its name; `php artisan autoproxy:setup nodes` lists them.');

            return null;
        }

        $node = ctype_digit($target)
            ? Node::query()->find((int) $target)
            : Node::query()->where('name', $target)->first();

        if ($node === null) {
            $this->error('This panel has no node "' . $target . '". `php artisan autoproxy:setup nodes` lists them.');
        }

        return $node;
    }

    protected function requireConnected(): bool
    {
        if (AutoProxySettings::isConnected()) {
            return true;
        }

        $this->error('No VPS connected yet. Run `php artisan autoproxy:setup store-code --code-file=<path>` first.');

        return false;
    }
}
