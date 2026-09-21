<?php

namespace Arrowtje\AutoProxy\Support;

/**
 * What a new site-mode tunnel client needs before the VPS is asked for one.
 *
 * WHAT WAS EXTRACTED AND WHY
 * The name check and the LAN-range parsing were written inline in
 * Setup::addSiteClient(), the Livewire method behind the Setup page's "add a
 * client" box. `php artisan autoproxy:setup add-client` has to accept and refuse
 * exactly the same input, so the parsing moved here and the page now calls it.
 * The error comes back as a title and a body because that is what the page's
 * notification needs; the command prints the two on consecutive lines.
 *
 * This is the panel-side shape check only. The agent has the final word - it
 * also refuses a range that covers its own public IP or the tunnel subnet, and
 * that refusal is shown to the admin verbatim rather than being second-guessed
 * here, where this code does not know either value.
 */
class ClientInput
{
    /**
     * @return array{name: string, cidrs: string[], error: array{title: string, body: string}|null}
     */
    public static function parse(string $name, string $rawCidrs): array
    {
        $name = trim($name);

        if ($name === '') {
            return self::error('Give the client a name', 'Something you will recognise later, like the machine it runs on.');
        }

        $cidrs = [];

        foreach (explode(',', $rawCidrs) as $cidr) {
            $cidr = trim($cidr);

            if ($cidr === '') {
                continue;
            }

            if (!preg_match('~^\d{1,3}(\.\d{1,3}){3}/\d{1,2}$~', $cidr)) {
                return self::error('That is not a LAN range', 'Use address/prefix, for example 10.0.0.0/24. Separate several with commas.');
            }

            $cidrs[] = $cidr;
        }

        if ($cidrs === []) {
            return self::error('Give the LAN range', 'The VPS only routes the ranges you list here through this client, for example 10.0.0.0/24.');
        }

        return ['name' => $name, 'cidrs' => $cidrs, 'error' => null];
    }

    /** @return array{name: string, cidrs: string[], error: array{title: string, body: string}} */
    protected static function error(string $title, string $body): array
    {
        return ['name' => '', 'cidrs' => [], 'error' => ['title' => $title, 'body' => $body]];
    }
}
