{{-- wire:poll sits on the outer element so it keeps running while the banner is
     empty: that is what lets the banner appear, and disappear, without a reload. --}}
<x-filament-widgets::widget wire:poll.60s>
    @if (count($problems))
        <x-filament::section>
            <x-slot name="heading">
                <span class="text-danger-600 dark:text-danger-400">Auto Proxy needs attention</span>
            </x-slot>

            <ul class="list-disc space-y-1 ps-5 text-sm text-gray-700 dark:text-gray-300">
                @foreach ($problems as $problem)
                    <li>{{ $problem }}</li>
                @endforeach
            </ul>

            <div class="mt-4 flex gap-4">
                <x-filament::link :href="$setupUrl">Open Setup</x-filament::link>
                <x-filament::link :href="$statusUrl">Open Status</x-filament::link>
            </div>
        </x-filament::section>
    @endif
</x-filament-widgets::widget>
