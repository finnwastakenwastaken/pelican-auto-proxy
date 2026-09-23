{{-- The interval is literal on purpose: Blade cannot interpolate into an
     attribute name. AutoProxyStatus::POLL_SECONDS must match it, and the line
     below shows the admin which one is in force. --}}
<x-filament-panels::page wire:poll.30s>
    <p class="text-sm text-gray-500 dark:text-gray-400">
        Refreshed at {{ $refreshedAt->format('H:i:s') }}, and again every {{ $pollSeconds }} seconds.
    </p>

    <div class="grid gap-6 lg:grid-cols-2">

        @if (count($unhealthyPeers))
            <div class="lg:col-span-2">
                <x-filament::section>
                    <x-slot name="heading">
                        <span class="text-danger-600 dark:text-danger-400">Tunnel clients that are not connected ({{ count($unhealthyPeers) }})</span>
                    </x-slot>
                    <x-slot name="description">These nodes are proxied, so their ports are published on the VPS, but nothing is on the other end of the tunnel. Players get a timeout.</x-slot>

                    <ul class="list-disc space-y-1 ps-5 text-sm text-gray-700 dark:text-gray-300">
                        @foreach ($unhealthyPeers as $row)
                            <li>{{ $row['message'] }}</li>
                        @endforeach
                    </ul>
                </x-filament::section>
            </div>
        @endif

        <x-filament::section>
            <x-slot name="heading">VPS</x-slot>
            <x-slot name="description">Live, {{ config('autoproxy.status_timeout', 2) }} second timeout. {{ $apiUrl ?: 'no VPS connected' }}</x-slot>

            @if (! $connected)
                <p class="text-sm text-gray-600 dark:text-gray-400">
                    No VPS is connected yet, so nothing is forwarded.
                    <a class="text-primary-600 hover:underline dark:text-primary-400" href="{{ $setupUrl }}">Open Setup</a> and paste the VPS code.
                </p>
            @elseif ($agentError)
                <p class="text-sm font-medium text-danger-600 dark:text-danger-400">Unreachable</p>
                <p class="mt-1 whitespace-pre-line text-sm text-gray-600 dark:text-gray-400">{{ $agentError }}</p>
            @else
                <dl class="grid grid-cols-2 gap-x-4 gap-y-2 text-sm">
                    <dt class="text-gray-500 dark:text-gray-400">Version</dt>
                    <dd>{{ $agentStatus['version'] ?? '-' }}</dd>

                    <dt class="text-gray-500 dark:text-gray-400">Uptime</dt>
                    <dd>{{ isset($agentStatus['uptime_s']) ? gmdate('H:i:s', (int) $agentStatus['uptime_s']) : '-' }}</dd>

                    <dt class="text-gray-500 dark:text-gray-400">Applied forwards</dt>
                    <dd>{{ $agentStatus['applied']['rules'] ?? '-' }}
                        (tcp {{ $agentStatus['applied']['tcp'] ?? '-' }}, udp {{ $agentStatus['applied']['udp'] ?? '-' }})</dd>

                    <dt class="text-gray-500 dark:text-gray-400">Applied at</dt>
                    <dd>{{ $agentStatus['applied_at'] ?? '-' }}</dd>

                    <dt class="text-gray-500 dark:text-gray-400">Tunnel clients</dt>
                    <dd>{{ $agentStatus['peers']['healthy'] ?? '-' }} healthy of {{ $agentStatus['peers']['total'] ?? '-' }}</dd>

                    <dt class="text-gray-500 dark:text-gray-400">VPS last error</dt>
                    <dd>{{ ($agentStatus['last_error'] ?? '') ?: 'none' }}</dd>
                </dl>
            @endif
        </x-filament::section>

        <x-filament::section>
            <x-slot name="heading">Last sync</x-slot>
            <x-slot name="description">The panel pushes the full set every minute, and always right after a change.</x-slot>

            <dl class="grid grid-cols-2 gap-x-4 gap-y-2 text-sm">
                <dt class="text-gray-500 dark:text-gray-400">Last run</dt>
                <dd>{{ $state->last_run_at?->diffForHumans() ?? 'never' }}</dd>

                <dt class="text-gray-500 dark:text-gray-400">VPS last answered</dt>
                <dd>{{ $state->last_success_at?->diffForHumans() ?? 'never' }}</dd>

                <dt class="text-gray-500 dark:text-gray-400">Forwards last pushed</dt>
                <dd>{{ $state->last_pushed_at?->diffForHumans() ?? 'never' }}</dd>

                <dt class="text-gray-500 dark:text-gray-400">Forwards pushed</dt>
                <dd>{{ $state->rule_count }}
                    ({{ $state->allocation_rule_count }} from allocations, {{ $state->manual_rule_count }} manual)</dd>

                <dt class="text-gray-500 dark:text-gray-400">Public allocations</dt>
                <dd>{{ $publicAllocationCount }} total, {{ $state->allocation_rule_count }} forwarded,
                    {{ count($unassigned) }} unassigned, {{ count($notProxied) }} on unproxied nodes</dd>

                <dt class="text-gray-500 dark:text-gray-400">Public address</dt>
                <dd>{{ $publicAddress ?: '-' }}</dd>

                <dt class="text-gray-500 dark:text-gray-400">Alias keywords</dt>
                <dd>{{ implode(', ', $keywords) ?: '-' }}</dd>

                <dt class="text-gray-500 dark:text-gray-400">Warn after</dt>
                <dd>{{ $staleAfterMinutes }} minutes without a successful sync</dd>
            </dl>

            @if (filled($state->last_error))
                <p class="mt-4 text-sm font-medium text-danger-600 dark:text-danger-400">Last error</p>
                <p class="mt-1 whitespace-pre-line text-sm text-gray-600 dark:text-gray-400">{{ $state->last_error }}</p>
            @endif

            @unless ($state->last_run_at)
                <p class="mt-4 text-sm text-danger-600 dark:text-danger-400">
                    The panel scheduler has not run autoproxy:sync yet.
                </p>
            @endunless
        </x-filament::section>

        <x-filament::section class="lg:col-span-2">
            <x-slot name="heading">Tunnel clients ({{ count($peers) }})</x-slot>
            <x-slot name="description">Read live from the VPS. A client is one machine; the node column says which node it serves.</x-slot>

            @if ($peerError)
                <p class="whitespace-pre-line text-sm text-danger-600 dark:text-danger-400">{{ $peerError }}</p>
            @elseif (count($peers) === 0)
                <p class="text-sm text-gray-500 dark:text-gray-400">
                    The VPS knows no tunnel clients yet. Switch a node on in <a class="text-primary-600 hover:underline dark:text-primary-400" href="{{ $setupUrl }}">Setup</a>.
                </p>
            @else
                <div class="overflow-x-auto">
                    <table class="w-full text-sm">
                        <thead class="text-gray-500 dark:text-gray-400">
                            <tr class="text-left">
                                <th class="py-2 pe-4 font-medium">Client</th>
                                <th class="py-2 pe-4 font-medium">Node</th>
                                <th class="py-2 pe-4 font-medium">Tunnel IP</th>
                                <th class="py-2 pe-4 font-medium">Mode</th>
                                <th class="py-2 pe-4 font-medium">Last handshake</th>
                                <th class="py-2 pe-4 font-medium">Traffic</th>
                                <th class="py-2 pe-4 font-medium">Client version</th>
                            </tr>
                        </thead>
                        <tbody>
                            @foreach ($peers as $peer)
                                @php($age = $peer['handshake_age_s'] ?? null)
                                <tr class="border-t border-gray-200 dark:border-gray-700">
                                    <td class="py-2 pe-4">{{ $peer['name'] ?? $peer['id'] ?? '?' }}</td>
                                    <td class="py-2 pe-4">{{ $peerNodeNames[(string) ($peer['id'] ?? '')] ?? 'not linked to a node' }}</td>
                                    <td class="py-2 pe-4">{{ $peer['tunnel_ip'] ?? '-' }}</td>
                                    <td class="py-2 pe-4">{{ ($peer['mode'] ?? '') === 'site' ? 'site (shared IP)' : 'real player IPs' }}</td>
                                    <td class="py-2 pe-4">
                                        @if ($age === null)
                                            <span class="text-warning-600 dark:text-warning-400">never connected</span>
                                        @elseif ((int) $age <= 180)
                                            <span class="text-success-600 dark:text-success-400">{{ (int) $age }}s ago</span>
                                        @else
                                            <span class="text-danger-600 dark:text-danger-400">{{ (int) $age }}s ago</span>
                                        @endif
                                    </td>
                                    <td class="py-2 pe-4">
                                        {{ isset($peer['rx']) ? number_format((int) $peer['rx'] / 1048576, 1) . ' MiB in' : '-' }},
                                        {{ isset($peer['tx']) ? number_format((int) $peer['tx'] / 1048576, 1) . ' MiB out' : '-' }}
                                    </td>
                                    @php($clientInfo = collect($clientRows)->firstWhere('id', (string) ($peer['id'] ?? ''))['info'] ?? null)
                                    <td class="py-2 pe-4">
                                        @if ($clientInfo)
                                            <span @class([
                                                'text-gray-500 dark:text-gray-400' => $clientInfo['tone'] === 'muted',
                                                'text-success-600 dark:text-success-400' => $clientInfo['tone'] === 'success',
                                                'text-warning-600 dark:text-warning-400' => $clientInfo['tone'] === 'warning',
                                            ])>{{ $clientInfo['version'] ?? 'not reported' }}{{ $clientInfo['update_available'] ? ' (update: ' . $clientInfo['latest'] . ')' : '' }}</span>
                                        @else
                                            -
                                        @endif
                                    </td>
                                </tr>
                            @endforeach
                        </tbody>
                    </table>
                </div>
            @endif
        </x-filament::section>

        @if ($connected && ! $peerError && count($clientRows))
            <x-filament::section class="lg:col-span-2">
                <x-slot name="heading">Tunnel client updates</x-slot>
                <x-slot name="description">
                    Latest release: {{ $latestRelease ?? 'unknown' }}{{ $latestError ? ' (' . $latestError . ')' : '' }}.
                    Each client reports its version to the VPS over the tunnel every two minutes. Remote updates are off
                    until you allow them per client; an update only ever installs an official release, checked against
                    its published checksums, and never a downgrade.
                </x-slot>

                <div class="mb-3">
                    <x-filament::button size="xs" color="gray" icon="tabler-refresh" wire:click="checkLatestRelease">Check for a new release</x-filament::button>
                </div>

                <div class="space-y-4">
                    @foreach ($clientRows as $row)
                        @php($info = $row['info'])
                        <div class="rounded-xl border border-gray-200 p-4 dark:border-gray-700" wire:key="client-update-{{ $row['id'] }}">
                            <div class="flex flex-wrap items-start justify-between gap-3">
                                <div>
                                    <p class="font-medium">{{ $row['name'] }}
                                        <span class="text-sm font-normal text-gray-500 dark:text-gray-400">&mdash; {{ $row['node'] ?? 'not linked to a node' }}{{ $info['flavour'] ? ', ' . ($info['flavour'] === 'docker' ? 'Docker' : 'system service') : '' }}</span>
                                    </p>
                                    <p @class([
                                        'text-sm',
                                        'text-gray-500 dark:text-gray-400' => $info['tone'] === 'muted',
                                        'text-success-600 dark:text-success-400' => $info['tone'] === 'success',
                                        'text-warning-600 dark:text-warning-400' => $info['tone'] === 'warning',
                                    ])>Client {{ $info['label'] }}</p>
                                    @if ($info['progress'])
                                        <p @class([
                                            'mt-1 text-sm',
                                            'text-success-600 dark:text-success-400' => $info['progress']['tone'] === 'success',
                                            'text-warning-600 dark:text-warning-400' => $info['progress']['tone'] === 'warning',
                                            'text-danger-600 dark:text-danger-400' => $info['progress']['tone'] === 'danger',
                                        ])>{{ $info['progress']['text'] }}</p>
                                    @endif
                                </div>

                                <div class="flex flex-wrap items-center gap-2">
                                    @if ($row['allowed'])
                                        <x-filament::button size="sm" color="success" icon="tabler-cloud-download"
                                            wire:click="disallowRemoteUpdates('{{ $row['id'] }}')"
                                            wire:confirm="Stop allowing remote updates for {{ $row['name'] }}? A pending request is withdrawn.">
                                            Remote updates allowed
                                        </x-filament::button>
                                    @else
                                        <x-filament::button size="sm" color="gray" icon="tabler-cloud-off"
                                            wire:click="allowRemoteUpdates('{{ $row['id'] }}')"
                                            wire:confirm="Allow remote updates for {{ $row['name'] }}? You can then press Update to have that machine install a newer official release by itself. Anyone holding this panel's VPS API token could also ask it to, but only for an official, checksum-verified, newer release. The machine's owner can refuse with: autoproxy-client remote-updates off">
                                            Allow remote updates
                                        </x-filament::button>
                                    @endif

                                    @if ($info['pending'])
                                        <x-filament::button size="sm" color="gray" icon="tabler-x"
                                            wire:click="withdrawClientUpdate('{{ $row['id'] }}')">
                                            Withdraw request
                                        </x-filament::button>
                                    @endif

                                    @if ($row['allowed'] && $info['update_available'])
                                        <x-filament::button size="sm" color="primary" icon="tabler-download"
                                            :disabled="! $info['can_remote']"
                                            wire:click="requestClientUpdate('{{ $row['id'] }}')"
                                            wire:confirm="Update {{ $row['name'] }} to {{ $info['latest'] }}? That machine downloads the release from GitHub, verifies its checksum, and restarts its client without dropping players.">
                                            {{ $info['pending'] && $info['desired'] === $info['latest'] ? 'Update to ' . $info['latest'] . ' again' : 'Update to ' . $info['latest'] }}
                                        </x-filament::button>
                                    @endif
                                </div>
                            </div>

                            @if ($info['remote_note'])
                                <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">{{ $info['remote_note'] }}</p>
                            @endif

                            @if ($info['show_command'])
                                <div class="mt-3">
                                    <p class="text-sm font-medium">Update it by hand</p>
                                    @include('autoproxy::partials.copy-block', [
                                        'value' => $info['command'],
                                        'help' => $info['command_help'],
                                    ])
                                </div>
                            @endif
                        </div>
                    @endforeach
                </div>
            </x-filament::section>
        @endif

        <x-filament::section class="lg:col-span-2">
            <x-slot name="heading">Withheld conflicts ({{ count($conflicts) }})</x-slot>
            <x-slot name="description">Two forwards claiming the same public port. Both are withheld until one is changed.</x-slot>

            @forelse ($conflicts as $conflict)
                <div class="border-b border-gray-200 py-2 text-sm last:border-0 dark:border-gray-700">
                    <span class="font-medium">{{ $conflict['a'] }}</span> ({{ $conflict['a_note'] ?? '' }} &rarr; {{ $conflict['a_target'] ?? '' }})
                    vs
                    <span class="font-medium">{{ $conflict['b'] }}</span> ({{ $conflict['b_note'] ?? '' }} &rarr; {{ $conflict['b_target'] ?? '' }})
                    <span class="text-gray-500 dark:text-gray-400">
                        &mdash; ports {{ $conflict['ports'] ?? '' }}, {{ $conflict['proto'] ?? '' }}: {{ $conflict['reason'] ?? '' }}
                    </span>
                </div>
            @empty
                <p class="text-sm text-gray-500 dark:text-gray-400">None.</p>
            @endforelse
        </x-filament::section>

        <x-filament::section class="lg:col-span-2">
            <x-slot name="heading">Not forwarded ({{ count($nodeNotConfigured) + count($notProxied) + count($unassigned) }})</x-slot>
            <x-slot name="description">
                Allocations that ask to be published but are not. Each says the first thing to fix.
            </x-slot>

            @php($skipped = array_merge($notProxied, $nodeNotConfigured, $unassigned))

            @forelse ($skipped as $entry)
                <div class="border-b border-gray-200 py-2 text-sm last:border-0 dark:border-gray-700">
                    {{ $entry['node'] ?? 'node ' . ($entry['node_id'] ?? '?') }}
                    &mdash; allocation {{ $entry['allocation_id'] ?? '?' }} on {{ $entry['ip'] ?? '?' }}:{{ $entry['port'] ?? '?' }}
                    <span class="text-gray-500 dark:text-gray-400">&mdash; {{ $entry['reason'] ?? 'unknown reason' }}</span>
                </div>
            @empty
                <p class="text-sm text-gray-500 dark:text-gray-400">None: everything that asks to be published is published.</p>
            @endforelse
        </x-filament::section>
    </div>
</x-filament-panels::page>
