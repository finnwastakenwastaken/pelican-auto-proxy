{{--
    A command the admin has to run somewhere else. navigator.clipboard only
    exists on https (or localhost), and plenty of panels are reached over plain
    http on a LAN, so there is a textarea fallback: without it the button would
    silently do nothing, which is the worst kind of failure.
--}}
@php($blockId = 'autoproxy-copy-' . \Illuminate\Support\Str::random(8))
<div class="mt-2" x-data="{ copied: false, failed: false, copy(text) {
        const done = () => { this.copied = true; this.failed = false; setTimeout(() => this.copied = false, 2000); };
        if (navigator.clipboard && window.isSecureContext) { navigator.clipboard.writeText(text).then(done).catch(() => this.fallback(text, done)); }
        else { this.fallback(text, done); }
    }, fallback(text, done) {
        const area = document.createElement('textarea');
        area.value = text; area.setAttribute('readonly', ''); area.style.position = 'fixed'; area.style.top = '0'; area.style.left = '0'; area.style.opacity = '0';
        document.body.appendChild(area); area.focus(); area.select(); area.setSelectionRange(0, text.length);
        let ok = false;
        try { ok = document.execCommand('copy'); } catch (e) { ok = false; }
        document.body.removeChild(area);
        if (ok) { done(); } else { this.failed = true; this.selectBlock(); }
    }, selectBlock() {
        {{-- Last resort: select the text so Ctrl+C / Cmd+C copies exactly the command, nothing more. --}}
        const el = this.$refs.block; const range = document.createRange(); range.selectNodeContents(el);
        const sel = window.getSelection(); sel.removeAllRanges(); sel.addRange(range);
    } }">
    {{-- The button lives above the block, never over it: the block scrolls sideways
         for long commands, and a button floating on top hid the end of the text. --}}
    <div class="flex items-center justify-between gap-2">
        <span class="text-xs text-gray-500 dark:text-gray-400" x-show="!failed">Copy, or click the text to select all of it.</span>
        <span class="text-xs text-warning-600 dark:text-warning-400" x-show="failed" x-cloak>Your browser blocked the copy. The text is selected: press Ctrl+C (Cmd+C on a Mac).</span>
        <x-filament::button size="xs" color="gray" type="button" x-on:click="copy(@js($value))">
            <span x-text="copied ? 'Copied' : 'Copy'">Copy</span>
        </x-filament::button>
    </div>
    <pre id="{{ $blockId }}" x-ref="block" x-on:click="selectBlock()" class="mt-1 cursor-text overflow-x-auto whitespace-pre rounded-lg bg-gray-950/90 p-3 text-xs leading-relaxed text-gray-100 dark:bg-gray-900">{{ $value }}</pre>
    @isset($help)
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ $help }}</p>
    @endisset
</div>
