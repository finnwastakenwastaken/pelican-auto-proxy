<?php

/**
 * Defaults only. Every live value is written by the Setup page into the
 * autoproxy_settings table; nothing here points at anyone's infrastructure.
 *
 * The panel loads this file itself (PluginService: config()->set('autoproxy', require ...)),
 * so config('autoproxy.*') resolves even before this plugin's provider boots.
 */
return [
    // Filled in by Setup step 1 from the VPS code. Empty means "not connected yet".
    'api_url' => '',

    // Optional. Empty means the alias rewrite uses the VPS IP from the VPS code.
    'public_hostname' => '',

    // An allocation whose alias is exactly one of these becomes public.
    'keywords' => 'proxy,public',

    // Warn when the last successful contact with the agent is older than this.
    'stale_after_minutes' => 5,

    // Push the full set at least this often even when nothing changed, so a
    // restarted agent is refilled without waiting for an edit.
    'heartbeat_minutes' => 10,

    // HTTP timeouts in seconds. Status probes are short: they run on page load.
    'connect_timeout' => 2,
    'push_timeout' => 5,
    'status_timeout' => 2,

    // Send CURLOPT_PINNEDPUBLICKEY as well as verifying against the stored
    // certificate, when the VPS code carried an SPKI hash and curl supports it.
    'pin_spki' => true,

    // Where the generated install commands and the in-page docs links point.
    'release_url' => 'https://github.com/finnwastakenwastaken/pelican-auto-proxy/releases/latest/download',
    'docs_url' => 'https://github.com/finnwastakenwastaken/pelican-auto-proxy/blob/main/docs',
    'docker_image' => 'ghcr.io/finnwastakenwastaken/autoproxy-client:latest',
];
