<?php

namespace Arrowtje\AutoProxy\Support;

use Arrowtje\AutoProxy\Models\ForwardRule;

/**
 * Everything a manual forward has to satisfy before it is worth writing down.
 *
 * WHAT WAS EXTRACTED AND WHY
 * These checks used to live inside closures in ForwardRuleResource::form():
 * the RFC1918 test on target_ip, the "inside the LAN ranges the chosen client
 * covers" test, the "a port range cannot be remapped" test, and the range-order
 * test (a bare `gte:public_port` string rule). Nothing outside a rendered
 * Filament form could reach any of them, so `php artisan autoproxy:setup
 * add-forward` would have needed a second copy of the same sentences - and the
 * first time one of them changed, the CLI and the UI would quietly disagree
 * about what the VPS accepts. They now live here, and both the Filament
 * resource and the command call these methods. Each returns the message to show
 * the admin, or null when the value is fine, so a Filament $fail() closure and a
 * console error print exactly the same sentence.
 *
 * The limits the form expressed declaratively (name length, port bounds) are
 * constants here for the same reason: the form's ->maxLength()/->minValue()
 * now read them instead of repeating the numbers.
 */
class ForwardRuleInput
{
    public const NAME_MAX = 100;

    public const PORT_MIN = 1;

    public const PORT_MAX = 65535;

    public static function nameError(?string $name): ?string
    {
        $name = trim((string) $name);

        if ($name === '') {
            return 'Give the forward a name. It is shown on the VPS as the note for this forward.';
        }

        if (mb_strlen($name) > self::NAME_MAX) {
            return 'That name is longer than ' . self::NAME_MAX . ' characters.';
        }

        return null;
    }

    public static function protocolError(?string $protocol): ?string
    {
        if (!array_key_exists((string) $protocol, ForwardRule::PROTOCOLS)) {
            return 'The protocol must be one of: ' . implode(', ', array_keys(ForwardRule::PROTOCOLS)) . '.';
        }

        return null;
    }

    /** The port on the VPS. The agent refuses its own SSH, WireGuard and API ports on top of this. */
    public static function publicPortError(mixed $port): ?string
    {
        if (!self::isPort($port)) {
            return 'The public port must be a whole number between ' . self::PORT_MIN . ' and ' . self::PORT_MAX . '.';
        }

        return null;
    }

    /** A range is forwarded 1:1, so its end may never sit below its start. */
    public static function publicPortEndError(mixed $end, mixed $start): ?string
    {
        if (blank($end)) {
            return null;
        }

        if (!self::isPort($end)) {
            return 'The range end must be a whole number between ' . self::PORT_MIN . ' and ' . self::PORT_MAX . ', or empty for a single port.';
        }

        if (self::isPort($start) && (int) $end < (int) $start) {
            return 'The range end must be the same as the public port or higher.';
        }

        return null;
    }

    /**
     * A LAN target. $cidrs is the set of ranges the chosen site client covers,
     * empty when no client was chosen or the VPS could not be reached - in which
     * case the range test is skipped rather than guessed at.
     *
     * @param string[] $cidrs
     */
    public static function targetIpError(?string $ip, array $cidrs): ?string
    {
        if (!Ip::isPrivateV4(is_string($ip) ? $ip : null)) {
            return 'The target must be a private LAN address (10.x, 172.16-31.x or 192.168.x).';
        }

        // The agent refuses an address outside the ranges the chosen client
        // covers. Catch it here, where the admin can still see which field is wrong.
        if ($cidrs !== [] && !Ip::inAnyCidr((string) $ip, $cidrs)) {
            return 'That address is outside the ranges this tunnel client covers (' . implode(', ', $cidrs) . '). The VPS would refuse it.';
        }

        return null;
    }

    /** A range is forwarded port for port, so there is no single port to remap to. */
    public static function targetPortError(mixed $targetPort, mixed $publicPortEnd): ?string
    {
        if (blank($targetPort)) {
            return null;
        }

        if (filled($publicPortEnd)) {
            return 'A port range cannot be remapped. Clear the range end or the target port.';
        }

        if (!self::isPort($targetPort)) {
            return 'The target port must be a whole number between ' . self::PORT_MIN . ' and ' . self::PORT_MAX . ', or empty for the same port.';
        }

        return null;
    }

    protected static function isPort(mixed $value): bool
    {
        if (is_int($value)) {
            return $value >= self::PORT_MIN && $value <= self::PORT_MAX;
        }

        if (!is_string($value) || !ctype_digit(trim($value))) {
            return false;
        }

        $port = (int) trim($value);

        return $port >= self::PORT_MIN && $port <= self::PORT_MAX;
    }
}
