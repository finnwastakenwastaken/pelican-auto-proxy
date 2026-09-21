{{--
    A command the admin has to run somewhere else. navigator.clipboard only
    exists on https (or localhost), and plenty of panels are reached over plain
    http on a LAN, so there is a textarea fallback: without it the button would
    silently do nothing, which is the worst kind of failure.
--}}
@php($blockId = 'autoproxy-copy-' . \Illuminate\Support\Str::random(8))
<div class="mt-2" x-data="{ copied: false, copy(text) {
        const done = () => { this.copied = true; setTimeout(() => this.copied = false, 2000); };
        if (navigator.clipboard && window.isSecureContext) { navigator.clipboard.writeText(text).then(done).catch(() => this.fallback(text, done)); }
        else { this.fallback(text, done); }
    }, fallback(text, done) {
        const area = document.createElement('textarea');
        area.value = text; area.setAttribute('readonly', ''); area.style.position = 'absolute'; area.style.left = '-9999px';
        document.body.appendChild(area); area.select();
        try { document.execCommand('copy'); done(); } catch (e) { window.prompt('Copy this:', text); }
        document.body.removeChild(area);
    } }">
    <div class="relative">
        <pre id="{{ $blockId }}" class="overflow-x-auto rounded-lg bg-gray-950/90 p-3 pe-20 text-xs leading-relaxed text-gray-100 dark:bg-gray-900">{{ $value }}</pre>
        <div class="absolute end-2 top-2">
            <x-filament::button size="xs" color="gray" type="button" x-on:click="copy(@js($value))">
                <span x-text="copied ? 'Copied' : 'Copy'">Copy</span>
            </x-filament::button>
        </div>
    </div>
    @isset($help)
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ $help }}</p>
    @endisset
</div>
