{{-- Step 1's input. Shown on its own when no VPS is connected, and behind a
     disclosure when one is, so re-pasting is possible but not the default. --}}
<div class="mt-4">
    <label class="block">
        <span class="text-xs font-medium text-gray-500 dark:text-gray-400">VPS code</span>
        <x-filament::input.wrapper class="mt-1">
            <textarea rows="4" wire:model="vpsCode" spellcheck="false" autocomplete="off"
                class="block w-full border-none bg-transparent px-3 py-1.5 font-mono text-xs text-gray-950 outline-none placeholder:text-gray-400 focus:ring-0 dark:text-white"
                placeholder="Paste the long code the installer printed, in one piece"></textarea>
        </x-filament::input.wrapper>
    </label>
    <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
        The code contains this panel's access token and your VPS's certificate. Treat it like a password; it is stored encrypted and cleared from this box once accepted.
    </p>

    <div class="mt-3">
        <x-filament::button wire:click="connectVps" icon="tabler-plug-connected" wire:loading.attr="disabled">
            Connect
        </x-filament::button>
    </div>
</div>
