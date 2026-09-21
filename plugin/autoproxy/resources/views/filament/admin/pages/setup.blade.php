<x-filament-panels::page>
    <div class="space-y-6">

        {{-- ------------------------------------------------------------ 1 --}}
        <x-filament::section>
            <x-slot name="heading">Step 1 &mdash; Connect the VPS</x-slot>
            <x-slot name="description">
                Run one command on your VPS; it prints a code that tells this panel where your VPS is and how to talk to it safely.
                <a class="text-primary-600 hover:underline dark:text-primary-400" href="{{ $docs['vps'] }}" target="_blank" rel="noopener">Read the VPS page</a>
            </x-slot>

            @if ($connected)
{{-- The callout component renders heading/description only: content put in
                     its default slot is silently dropped, so the details live below it. --}}
                <x-filament::callout color="success" icon="tabler-check"
                    heading="A VPS is connected"
                    :description="$apiUrl" />

                <dl class="mt-3 grid grid-cols-1 gap-x-4 gap-y-1 text-sm sm:grid-cols-2">
                    <dt class="text-gray-500 dark:text-gray-400">API address</dt>
                    <dd>{{ $apiUrl }}</dd>
                    <dt class="text-gray-500 dark:text-gray-400">Agent version (from the code)</dt>
                    <dd>{{ $agentVersion ?: 'unknown' }}</dd>
                    <dt class="text-gray-500 dark:text-gray-400">Certificate</dt>
                    <dd class="break-all">{{ $certPath }}</dd>
                </dl>

                <div class="mt-4 flex flex-wrap gap-2">
                    <x-filament::button wire:click="testConnection" icon="tabler-plug" wire:loading.attr="disabled">
                        Test connection
                    </x-filament::button>
                </div>

                <details class="mt-4">
                    <summary class="cursor-pointer text-sm text-gray-500 dark:text-gray-400">Paste a new VPS code (after reinstalling the VPS, or when its IP changed)</summary>
                    @include('autoproxy::partials.vps-code-form', ['installCommand' => $installCommand])
                </details>
            @else
                <x-filament::callout color="warning" icon="tabler-alert-triangle"
                    heading="No VPS connected yet"
                    description="Nothing is forwarded until this step is done." />

                <p class="mt-4 text-sm text-gray-600 dark:text-gray-400">Run this on your VPS as root:</p>
                @include('autoproxy::partials.copy-block', ['value' => $installCommand])

                @include('autoproxy::partials.vps-code-form', ['installCommand' => $installCommand])
            @endif
        </x-filament::section>

        {{-- ------------------------------------------------------------ 2 --}}
        <x-filament::section>
            <x-slot name="heading">Step 2 &mdash; Nodes</x-slot>
            <x-slot name="description">
                Switch on the nodes whose ports you want to publish, then run the join command on each node's machine.
                <a class="text-primary-600 hover:underline dark:text-primary-400" href="{{ $docs['client'] }}" target="_blank" rel="noopener">Read the tunnel client page</a>
            </x-slot>

            @if (! $connected)
                <p class="text-sm text-gray-500 dark:text-gray-400">Connect a VPS in step 1 first.</p>
            @else
                @if ($peerError)
                    <x-filament::callout color="danger" icon="tabler-alert-triangle"
                        heading="Could not read the tunnel clients from the VPS"
                        :description="$peerError" />
                @endif

                @if (count($nodes) === 0)
                    <p class="text-sm text-gray-500 dark:text-gray-400">This panel has no nodes yet.</p>
                @else
                    <div class="space-y-4">
                        @foreach ($nodes as $node)
                            <div class="rounded-xl border border-gray-200 p-4 dark:border-gray-700">
                                <div class="flex flex-wrap items-center justify-between gap-3">
                                    <div>
                                        <p class="font-medium">{{ $node['name'] }}</p>
                                        <p class="text-sm
                                            @class([
                                                'text-gray-500 dark:text-gray-400' => $node['status']['tone'] === 'muted',
                                                'text-success-600 dark:text-success-400' => $node['status']['tone'] === 'success',
                                                'text-warning-600 dark:text-warning-400' => $node['status']['tone'] === 'warning',
                                                'text-danger-600 dark:text-danger-400' => $node['status']['tone'] === 'danger',
                                            ])">{{ $node['status']['text'] }}</p>
                                    </div>

                                    <div class="flex items-center gap-2">
                                        @if ($node['proxied'])
                                            <x-filament::button color="danger" size="sm" icon="tabler-plug-off"
                                                wire:click="disableProxy({{ $node['id'] }})"
                                                wire:confirm="Stop proxying {{ $node['name'] }}? Its tunnel client is removed from the VPS and its public ports close immediately. Players connected through them are disconnected.">
                                                Proxied
                                            </x-filament::button>
                                        @else
                                            <x-filament::button color="gray" size="sm" icon="tabler-plug"
                                                wire:click="enableProxy({{ $node['id'] }})">
                                                Not proxied
                                            </x-filament::button>
                                        @endif
                                    </div>
                                </div>

                                <div class="mt-4 grid gap-3 sm:grid-cols-3">
                                    <label class="block">
                                        <span class="text-xs font-medium text-gray-500 dark:text-gray-400">Mode</span>
                                        <x-filament::input.wrapper class="mt-1">
                                            <x-filament::input.select wire:model.live="nodeMode.{{ $node['id'] }}">
                                                <option value="real">Tunnel client on this node's machine (real player IPs)</option>
                                                <option value="site">Via another client + LAN IP (shared IP)</option>
                                            </x-filament::input.select>
                                        </x-filament::input.wrapper>
                                    </label>

                                    @if (($node['mode'] ?? 'real') === 'site')
                                        <label class="block">
                                            <span class="text-xs font-medium text-gray-500 dark:text-gray-400">Reached through</span>
                                            <x-filament::input.wrapper class="mt-1">
                                                <x-filament::input.select wire:model="nodeViaPeer.{{ $node['id'] }}">
                                                    <option value="">Choose a tunnel client&hellip;</option>
                                                    @foreach ($peerOptions as $peerId => $label)
                                                        <option value="{{ $peerId }}">{{ $label }}</option>
                                                    @endforeach
                                                </x-filament::input.select>
                                            </x-filament::input.wrapper>
                                            @if (empty($peerOptions))
                                                <span class="mt-1 block text-xs text-gray-500 dark:text-gray-400">
                                                    No LAN tunnel client yet. Add one under &ldquo;tunnel clients on a LAN&rdquo; below: only a client in site mode can carry a node this way.
                                                </span>
                                            @endif
                                        </label>

                                        <label class="block">
                                            <span class="text-xs font-medium text-gray-500 dark:text-gray-400">This node's LAN IP</span>
                                            <x-filament::input.wrapper class="mt-1">
                                                <x-filament::input type="text" placeholder="10.0.0.10" wire:model="nodeLanIp.{{ $node['id'] }}" />
                                            </x-filament::input.wrapper>
                                        </label>
                                    @endif
                                </div>

                                <div class="mt-3 flex flex-wrap gap-2">
                                    <x-filament::button size="sm" color="gray" wire:click="saveNode({{ $node['id'] }})">Save node</x-filament::button>

                                    @if ($node['proxied'] && $node['mode'] === 'real' && $node['peer_id'])
                                        <x-filament::button size="sm" color="gray" icon="tabler-refresh"
                                            wire:click="regenerateJoinCode({{ $node['id'] }})"
                                            wire:confirm="Issue a new join code for {{ $node['name'] }}? The current client stops working until you run the new command on its machine.">
                                            Regenerate join code
                                        </x-filament::button>
                                    @endif
                                </div>

                                @if ($node['join_command'])
                                    <div class="mt-4" x-data="{ tab: 'systemd' }">
                                        <p class="text-sm font-medium">Join command for {{ $node['name'] }}'s machine</p>
                                        <p class="text-xs text-gray-500 dark:text-gray-400">
                                            Shown until this client connects for the first time, then it is deleted from the panel. Lost it? Regenerate above.
                                        </p>

                                        <div class="mt-2 flex gap-2">
                                            <x-filament::button size="xs" x-on:click="tab = 'systemd'" x-bind:color="tab === 'systemd' ? 'primary' : 'gray'" color="gray">System service</x-filament::button>
                                            <x-filament::button size="xs" x-on:click="tab = 'docker'" x-bind:color="tab === 'docker' ? 'primary' : 'gray'" color="gray">Docker Compose</x-filament::button>
                                        </div>

                                        <div x-show="tab === 'systemd'">
                                            @include('autoproxy::partials.copy-block', [
                                                'value' => $node['join_command'],
                                                'help' => 'Run as root on the machine that runs this node\'s Wings.',
                                            ])
                                        </div>
                                        <div x-show="tab === 'docker'" x-cloak>
                                            @include('autoproxy::partials.copy-block', [
                                                'value' => $node['compose'],
                                                'help' => 'Save as compose.yml on that machine and run: docker compose up -d',
                                            ])
                                        </div>
                                    </div>
                                @endif
                            </div>
                        @endforeach
                    </div>
                @endif

                <div class="mt-6 rounded-xl border border-dashed border-gray-300 p-4 dark:border-gray-600">
                    <p class="font-medium">Tunnel clients on other machines</p>
                    <p class="text-xs text-gray-500 dark:text-gray-400">
                        Needed only for "via another client": a client on a LAN machine that forwards to other addresses on
                        that LAN. Those targets share one IP, so use this for panels, SFTP and services, not for game ports
                        where you want real player IPs.
                    </p>

                    @if (count($siteClients))
                        <div class="mt-3 space-y-3">
                            @foreach ($siteClients as $client)
                                <div class="rounded-lg bg-gray-50 p-3 dark:bg-gray-800/50">
                                    <div class="flex flex-wrap items-center justify-between gap-2">
                                        <div>
                                            <p class="text-sm font-medium">{{ $client['name'] }}
                                                <span class="text-gray-500 dark:text-gray-400">&mdash; {{ $client['tunnel_ip'] }}{{ $client['lan_cidrs'] ? ', serves ' . $client['lan_cidrs'] : '' }}</span>
                                            </p>
                                            <p class="text-xs
                                                @class([
                                                    'text-success-600 dark:text-success-400' => $client['status']['tone'] === 'success',
                                                    'text-warning-600 dark:text-warning-400' => $client['status']['tone'] === 'warning',
                                                    'text-danger-600 dark:text-danger-400' => $client['status']['tone'] === 'danger',
                                                ])">{{ $client['status']['text'] }}</p>
                                        </div>
                                        <div class="flex gap-2">
                                            <x-filament::button size="xs" color="gray" icon="tabler-refresh"
                                                wire:click="regenerateClientCode('{{ $client['id'] }}')"
                                                wire:confirm="Issue a new join code for {{ $client['name'] }}? That client stops working until you run the new command on its machine.">
                                                New join code
                                            </x-filament::button>
                                            <x-filament::button size="xs" color="danger" icon="tabler-trash"
                                                wire:click="removeClient('{{ $client['id'] }}')"
                                                wire:confirm="Remove {{ $client['name'] }}? Every forward through it closes immediately, and nodes routed through it are switched off.">
                                                Remove
                                            </x-filament::button>
                                        </div>
                                    </div>

                                    @if ($client['join_command'])
                                        <div x-data="{ tab: 'systemd' }" class="mt-2">
                                            <div class="flex gap-2">
                                                <x-filament::button size="xs" color="gray" x-on:click="tab = 'systemd'">System service</x-filament::button>
                                                <x-filament::button size="xs" color="gray" x-on:click="tab = 'docker'">Docker Compose</x-filament::button>
                                            </div>
                                            <div x-show="tab === 'systemd'">
                                                @include('autoproxy::partials.copy-block', ['value' => $client['join_command']])
                                            </div>
                                            <div x-show="tab === 'docker'" x-cloak>
                                                @include('autoproxy::partials.copy-block', ['value' => $client['compose']])
                                            </div>
                                        </div>
                                    @endif
                                </div>
                            @endforeach
                        </div>
                    @endif

                    <div class="mt-3 grid gap-3 sm:grid-cols-3">
                        <label class="block">
                            <span class="text-xs font-medium text-gray-500 dark:text-gray-400">Name</span>
                            <x-filament::input.wrapper class="mt-1">
                                <x-filament::input type="text" wire:model="newClientName" placeholder="office-nas" />
                            </x-filament::input.wrapper>
                        </label>
                        <label class="block">
                            <span class="text-xs font-medium text-gray-500 dark:text-gray-400">LAN range(s) it serves</span>
                            <x-filament::input.wrapper class="mt-1">
                                <x-filament::input type="text" wire:model="newClientCidrs" placeholder="10.0.0.0/24" />
                            </x-filament::input.wrapper>
                        </label>
                        <div class="flex items-end">
                            <x-filament::button color="gray" wire:click="addSiteClient">Add client</x-filament::button>
                        </div>
                    </div>
                </div>

                @unless ($anyProxied)
                    <p class="mt-4 text-sm text-warning-600 dark:text-warning-400">
                        No node is proxied yet, so nothing is being forwarded. Switch one on above.
                    </p>
                @endunless
            @endif
        </x-filament::section>

        {{-- ------------------------------------------------------------ 3 --}}
        <x-filament::section>
            <x-slot name="heading">Step 3 &mdash; Public address</x-slot>
            <x-slot name="description">
                What players see: give an allocation one of the keywords as its alias and it is published under this address.
                <a class="text-primary-600 hover:underline dark:text-primary-400" href="{{ $docs['plugin'] }}" target="_blank" rel="noopener">Read the plugin page</a>
            </x-slot>

            <div class="grid gap-4 sm:grid-cols-3">
                <label class="block">
                    <span class="text-xs font-medium text-gray-500 dark:text-gray-400">Public hostname (optional)</span>
                    <x-filament::input.wrapper class="mt-1">
                        <x-filament::input type="text" wire:model="publicHostname" placeholder="{{ $endpointIp ?: 'play.example.com' }}" />
                    </x-filament::input.wrapper>
                    <span class="text-xs text-gray-500 dark:text-gray-400">
                        Empty means players are shown the VPS IP{{ $endpointIp ? ' (' . $endpointIp . ')' : '' }}. Point the name at that IP first.
                    </span>
                </label>

                <label class="block">
                    <span class="text-xs font-medium text-gray-500 dark:text-gray-400">Alias keywords</span>
                    <x-filament::input.wrapper class="mt-1">
                        <x-filament::input type="text" wire:model="keywords" placeholder="proxy,public" />
                    </x-filament::input.wrapper>
                    <span class="text-xs text-gray-500 dark:text-gray-400">Comma separated. The match is exact, so "proxy server" does not count.</span>
                </label>

                <label class="block">
                    <span class="text-xs font-medium text-gray-500 dark:text-gray-400">Warn after (minutes)</span>
                    <x-filament::input.wrapper class="mt-1">
                        <x-filament::input type="number" min="1" max="1440" wire:model="staleAfterMinutes" />
                    </x-filament::input.wrapper>
                    <span class="text-xs text-gray-500 dark:text-gray-400">How long without a successful sync before the dashboard warns you.</span>
                </label>
            </div>

            <div class="mt-4 flex flex-wrap gap-2">
                <x-filament::button wire:click="savePublicAddress">Save</x-filament::button>
                <x-filament::button color="gray" icon="tabler-refresh" wire:click="syncNow">Sync now</x-filament::button>
            </div>
        </x-filament::section>
    </div>
</x-filament-panels::page>
