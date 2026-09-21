{{-- Read-only: the alias on the allocation is the source of truth, edited in Pelican itself. --}}
<div class="mt-6">
    <x-filament::section>
        <x-slot name="heading">Public allocations ({{ count($rows) }})</x-slot>
        <x-slot name="description">
            Allocations with alias <span class="font-medium">{{ $address ?: 'not configured' }}</span>
            @if (count($keywords))
                (or one of: {{ implode(', ', $keywords) }}, which is rewritten to it on the next sync)
            @endif
            are forwarded automatically once their node is proxied and a server is assigned to them. The node name links
            to its Allocations tab, where the alias is typed. Remove the alias there to close the port.
        </x-slot>

        @if (count($rows) === 0)
            <p class="text-sm text-gray-500 dark:text-gray-400">
                No allocation carries the public alias yet.
            </p>
        @else
            <div class="overflow-x-auto">
                <table class="w-full text-sm">
                    <thead class="text-gray-500 dark:text-gray-400">
                        <tr class="text-left">
                            <th class="py-2 pe-4 font-medium">Server</th>
                            <th class="py-2 pe-4 font-medium">Node</th>
                            <th class="py-2 pe-4 font-medium">Public port</th>
                            <th class="py-2 pe-4 font-medium">Alias</th>
                            <th class="py-2 pe-4 font-medium">Forwarded to</th>
                        </tr>
                    </thead>
                    <tbody>
                        @foreach ($rows as $row)
                            <tr class="border-t border-gray-200 dark:border-gray-700">
                                <td class="py-2 pe-4">{{ $row['server'] }}</td>
                                <td class="py-2 pe-4">
                                    @if (($nodeUrls[$row['node_id']] ?? null))
                                        <a class="text-primary-600 hover:underline dark:text-primary-400"
                                           href="{{ $nodeUrls[$row['node_id']] }}">{{ $row['node'] }}</a>
                                    @else
                                        {{ $row['node'] }}
                                    @endif
                                </td>
                                <td class="py-2 pe-4">{{ $row['port'] }}</td>
                                <td class="py-2 pe-4">{{ $row['alias'] }}</td>
                                <td class="py-2 pe-4">
                                    @if ($row['server_id'] === null)
                                        <span class="text-gray-500 dark:text-gray-400">unassigned, not forwarded</span>
                                    @elseif (! $row['proxied'])
                                        <span class="text-warning-600 dark:text-warning-400">node not proxied</span>
                                    @elseif ($row['reason'])
                                        <span class="text-danger-600 dark:text-danger-400">{{ $row['reason'] }}</span>
                                    @else
                                        {{ $row['target_label'] }}:{{ $row['port'] }}
                                    @endif
                                </td>
                            </tr>
                        @endforeach
                    </tbody>
                </table>
            </div>
        @endif
    </x-filament::section>
</div>
