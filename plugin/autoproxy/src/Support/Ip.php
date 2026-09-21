<?php

namespace Arrowtje\AutoProxy\Support;

class Ip
{
    /**
     * RFC1918 IPv4 only. Everything the agent will accept as a DNAT target, and
     * nothing else: 0.0.0.0, public addresses, IPv6 and hostnames are rejected.
     */
    public static function isPrivateV4(?string $ip): bool
    {
        if ($ip === null || $ip === '' || $ip === '0.0.0.0') {
            return false;
        }

        if (filter_var($ip, FILTER_VALIDATE_IP, FILTER_FLAG_IPV4) === false) {
            return false;
        }

        $long = ip2long($ip);
        if ($long === false) {
            return false;
        }

        return ($long >= ip2long('10.0.0.0') && $long <= ip2long('10.255.255.255'))
            || ($long >= ip2long('172.16.0.0') && $long <= ip2long('172.31.255.255'))
            || ($long >= ip2long('192.168.0.0') && $long <= ip2long('192.168.255.255'));
    }

    /**
     * Is this IPv4 address inside any of these CIDR ranges?
     *
     * Mirrors the agent's own check: it refuses a target_ip that falls outside
     * the LAN ranges the chosen site client covers, because nothing on the VPS
     * knows how to reach it. An unparseable range is skipped rather than
     * treated as a match.
     *
     * @param string[] $cidrs
     */
    public static function inAnyCidr(?string $ip, array $cidrs): bool
    {
        if ($ip === null || filter_var($ip, FILTER_VALIDATE_IP, FILTER_FLAG_IPV4) === false) {
            return false;
        }

        $long = ip2long($ip);

        if ($long === false) {
            return false;
        }

        foreach ($cidrs as $cidr) {
            $parts = explode('/', trim((string) $cidr), 2);

            if (count($parts) !== 2 || !ctype_digit(trim($parts[1]))) {
                continue;
            }

            $base = ip2long(trim($parts[0]));
            $bits = (int) trim($parts[1]);

            if ($base === false || $bits > 32) {
                continue;
            }

            // Spelled out rather than a shift of 32, which is undefined here.
            $mask = $bits === 0 ? 0 : (-1 << (32 - $bits));

            if (($long & $mask) === ($base & $mask)) {
                return true;
            }
        }

        return false;
    }
}
