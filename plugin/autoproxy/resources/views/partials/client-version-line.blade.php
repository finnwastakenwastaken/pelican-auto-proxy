{{-- One line about a tunnel client's version, and a second while an update is
     in flight. Updating itself happens on the Status page. --}}
@if ($client)
    <p @class([
        'text-xs',
        'text-gray-500 dark:text-gray-400' => $client['tone'] === 'muted',
        'text-success-600 dark:text-success-400' => $client['tone'] === 'success',
        'text-warning-600 dark:text-warning-400' => $client['tone'] === 'warning',
    ])>
        Tunnel client {{ $client['label'] }}
        @if ($client['update_available'] || $client['pending'])
            &middot; <a class="text-primary-600 hover:underline dark:text-primary-400" href="{{ $statusUrl }}">update it on the Status page</a>
        @endif
    </p>
    @if ($client['progress'])
        <p @class([
            'text-xs',
            'text-success-600 dark:text-success-400' => $client['progress']['tone'] === 'success',
            'text-warning-600 dark:text-warning-400' => $client['progress']['tone'] === 'warning',
            'text-danger-600 dark:text-danger-400' => $client['progress']['tone'] === 'danger',
        ])>{{ $client['progress']['text'] }}</p>
    @endif
@endif
