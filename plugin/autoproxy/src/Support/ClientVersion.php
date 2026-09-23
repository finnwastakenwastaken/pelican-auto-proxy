<?php

namespace Arrowtje\AutoProxy\Support;

/**
 * What the panel says about one tunnel client's version: which version it runs,
 * whether a newer release exists, the command that updates it, whether the
 * one-click update can be offered, and how far a requested update has got.
 *
 * Everything comes from one entry of the agent's GET /v1/peers (its "client"
 * object, added in agent 0.3.0) plus the latest release number. The panel can
 * never reach a node host itself, so it only ever knows what the client last
 * told the VPS over the tunnel.
 *
 * Pure: no Laravel, no database, no HTTP, so test/rules-test.php can require it.
 */
final class ClientVersion
{
    /** The first client release that reports its version and can update itself. */
    public const SELF_UPDATE_SINCE = '0.3.0';

    /** A report older than this is shown with its age: the client may be down. */
    public const STALE_REPORT_S = 900;

    /** X.Y.Z from "X.Y.Z" or "vX.Y.Z", or null for anything else ("dev", "0.3.0-rc1"). */
    public static function release(?string $version): ?string
    {
        if ($version === null) {
            return null;
        }

        return preg_match('/^v?(\d{1,4})\.(\d{1,4})\.(\d{1,4})$/', trim($version), $m) === 1
            ? $m[1] . '.' . $m[2] . '.' . $m[3]
            : null;
    }

    /**
     * -1, 0 or 1, or null when either side is not a plain release number. The
     * agent's twin is agent/internal/clients.Compare; the client's is
     * version_cmp in client/autoproxy-client.
     */
    public static function compare(?string $a, ?string $b): ?int
    {
        $a = self::release($a);
        $b = self::release($b);

        if ($a === null || $b === null) {
            return null;
        }

        return version_compare($a, $b);
    }

    /**
     * The one-liner that updates this client by hand, for the copy block.
     *
     * @return array{command: string, help: string}
     */
    public static function command(?string $version, ?string $flavour, string $releaseUrl): array
    {
        if ($flavour === 'docker') {
            return [
                'command' => 'docker compose pull && docker compose up -d',
                'help' => 'Run it in the folder with this client\'s compose.yml. The Docker image updates by pulling a new image; the container cannot replace itself. The join code stays in the compose file, so nothing else changes.',
            ];
        }

        $cmp = self::compare($version, self::SELF_UPDATE_SINCE);

        if ($cmp !== null && $cmp >= 0) {
            return [
                'command' => 'sudo autoproxy-client update',
                'help' => 'Run it on that machine. It downloads the latest release from GitHub, checks it against the release\'s SHA256SUMS, and restarts the client without dropping players. No join code needed.',
            ];
        }

        return [
            'command' => 'curl -fsSL ' . rtrim($releaseUrl, '/') . '/install-client.sh | sudo bash',
            'help' => 'Run it on that machine. Clients older than ' . self::SELF_UPDATE_SINCE . ' have no update command yet; the installer without a join code updates them in place and keeps their configuration and keys. After that, updates can be one click.',
        ];
    }

    /**
     * @param array<string, mixed> $peer one entry of GET /v1/peers
     * @param string|null $latest the latest release ("0.3.1"), or null when unknown
     * @param bool $remoteAllowed the admin's per-client "allow remote updates" switch
     * @return array{
     *     agent_supports: bool,
     *     version: string|null,
     *     flavour: string|null,
     *     label: string,
     *     tone: string,
     *     update_available: bool,
     *     latest: string|null,
     *     show_command: bool,
     *     command: string,
     *     command_help: string,
     *     can_remote: bool,
     *     remote_note: string|null,
     *     pending: bool,
 *     desired: string|null,
     *     progress: array{text: string, tone: string}|null,
     * }
     */
    public static function describe(array $peer, ?string $latest, bool $remoteAllowed, string $releaseUrl, int $now): array
    {
        $agentSupports = array_key_exists('client', $peer) && is_array($peer['client']);
        $client = $agentSupports ? $peer['client'] : [];

        $version = self::str($client['version'] ?? null);
        $flavour = self::str($client['flavour'] ?? null);
        $reportedAt = self::time($client['reported_at'] ?? null);
        $latest = self::release($latest);
        $cmp = self::compare($version, $latest);
        $updateAvailable = $cmp !== null && $cmp < 0;

        [$label, $tone] = match (true) {
            !$agentSupports => ['unknown: the VPS agent is older than ' . self::SELF_UPDATE_SINCE . ' and does not collect client versions', 'muted'],
            $version === null => ['not reported: the client is older than ' . self::SELF_UPDATE_SINCE . ', or has not connected since the VPS agent was updated', 'warning'],
            self::release($version) === null => [$version . ' (a development build)', 'muted'],
            $latest === null => [$version . ' (latest release unknown right now)', 'muted'],
            $updateAvailable => [$version . ', update available: ' . $latest, 'warning'],
            $cmp === 0 => [$version . ', up to date', 'success'],
            default => [$version . ', newer than the latest release (' . $latest . ')', 'success'],
        };

        if ($reportedAt !== null && $now - $reportedAt > self::STALE_REPORT_S) {
            $label .= ' (last reported ' . self::ago($now - $reportedAt) . ' ago)';
        }

        $command = self::command($version, $flavour, $releaseUrl);

        $note = match (true) {
            !$agentSupports => 'Update the VPS agent to ' . self::SELF_UPDATE_SINCE . ' or newer first; until then it cannot pass an update on.',
            $version === null => 'This client does not report a version yet. Update it once with the command below; after that it can be updated from here.',
            $flavour === 'docker' => 'Docker clients update by pulling a new image, so there is no one-click update for them.',
            (self::compare($version, self::SELF_UPDATE_SINCE) ?? 1) < 0 => 'This client is older than ' . self::SELF_UPDATE_SINCE . ' and cannot update itself. Run the command below once.',
            ($client['remote_updates'] ?? null) === false => 'Remote updates are switched off on that machine. Its owner can allow them there with: sudo autoproxy-client remote-updates on',
            !$remoteAllowed => null,
            $latest === null => 'The latest release could not be looked up, so there is nothing to update to right now.',
            !$updateAvailable => null,
            default => null,
        };

        $canRemote = $agentSupports && $remoteAllowed && $updateAvailable && $version !== null
            && $flavour !== 'docker' && ($client['remote_updates'] ?? true) !== false
            && (self::compare($version, self::SELF_UPDATE_SINCE) ?? -1) >= 0;

        $desired = self::str($client['desired_version'] ?? null);

        return [
            'agent_supports' => $agentSupports,
            'version' => $version,
            'flavour' => $flavour,
            'label' => $label,
            'tone' => $tone,
            'update_available' => $updateAvailable,
            'latest' => $latest,
            'show_command' => $updateAvailable || ($agentSupports && $version === null),
            'command' => $command['command'],
            'command_help' => $command['help'],
            'can_remote' => $canRemote,
            'remote_note' => $note,
            'pending' => $desired !== null,
            'desired' => $desired,
            'progress' => self::progress($client, $now),
        ];
    }

    /**
     * Requested, then updating, then updated or failed with the client's own
     * reason. The agent clears the request when the client reports the version
     * it asked for, and keeps an "updated" record, so both ends of the story
     * survive a page reload.
     *
     * @param array<string, mixed> $client
     * @return array{text: string, tone: string}|null
     */
    public static function progress(array $client, int $now): ?array
    {
        $desired = self::str($client['desired_version'] ?? null);
        $requestId = self::str($client['request_id'] ?? null);
        $update = is_array($client['update'] ?? null) ? $client['update'] : null;
        $state = self::str($update['state'] ?? null);
        $sameRequest = $update !== null && $requestId !== null && self::str($update['request_id'] ?? null) === $requestId;
        $at = self::time($update['at'] ?? null);
        $when = $at === null ? '' : ' ' . self::ago(max(0, $now - $at)) . ' ago';

        if ($desired !== null) {
            if ($sameRequest && $state === 'failed') {
                return [
                    'text' => 'Update to ' . $desired . ' failed' . $when . ': ' . self::sentence(self::str($update['error'] ?? null))
                        . ' Nothing was changed on that machine. To retry, press Update again on the Status page.',
                    'tone' => 'danger',
                ];
            }

            if ($sameRequest && $state === 'updating') {
                return ['text' => 'Updating to ' . $desired . ' (started' . $when . ')...', 'tone' => 'warning'];
            }

            $requestedAt = self::time($client['requested_at'] ?? null);

            return [
                'text' => 'Update to ' . $desired . ' requested' . ($requestedAt === null ? '' : ' ' . self::ago(max(0, $now - $requestedAt)) . ' ago')
                    . '. The client picks it up at its next check-in, within about three minutes.',
                'tone' => 'warning',
            ];
        }

        // Only while it still runs that version: a client moved by hand since then
        // (update --force, the installer) would otherwise read "updated to X"
        // under a version line that says something else.
        if ($state === 'updated' && self::str($update['version'] ?? null) === self::str($client['version'] ?? null)) {
            return ['text' => 'Updated to ' . (self::str($update['version'] ?? null) ?? '?') . $when . '.', 'tone' => 'success'];
        }

        if ($state === 'failed') {
            return [
                'text' => 'The last update attempt (' . (self::str($update['version'] ?? null) ?? '?') . ') failed' . $when . ': ' . self::sentence(self::str($update['error'] ?? null)),
                'tone' => 'danger',
            ];
        }

        return null;
    }

    /** The client's reason as one sentence, ending in exactly one full stop. */
    private static function sentence(?string $text): string
    {
        $text = $text ?? 'no reason given';

        return preg_match('/[.!?]$/', $text) === 1 ? $text : $text . '.';
    }

    public static function ago(int $seconds): string
    {
        return match (true) {
            $seconds < 120 => $seconds . 's',
            $seconds < 7200 => (int) round($seconds / 60) . ' minutes',
            $seconds < 172800 => (int) round($seconds / 3600) . ' hours',
            default => (int) round($seconds / 86400) . ' days',
        };
    }

    private static function str(mixed $value): ?string
    {
        if (!is_string($value)) {
            return null;
        }

        $value = trim($value);

        return $value === '' ? null : $value;
    }

    private static function time(mixed $value): ?int
    {
        if (!is_string($value) || $value === '') {
            return null;
        }

        $ts = strtotime($value);

        return $ts === false ? null : $ts;
    }
}
