<?php

namespace Arrowtje\AutoProxy\Support;

use Arrowtje\AutoProxy\Exceptions\AutoProxyException;

/**
 * The "VPS code": one base64url blob printed once by install-vps.sh, pasted into
 * Setup step 1. It carries everything the panel needs to reach the agent safely,
 * so the admin never handles a key, a token or a certificate by hand.
 *
 * Decoded shape (v1):
 *   {v:1, endpoint_ip, api_port, api_ca_pem (base64 PEM), api_spki_sha256,
 *    token, wg_pubkey, wg_port, tunnel_subnet, vps_tunnel_ip, version}
 *
 * Every failure here is something the admin can act on, so every message says
 * what is wrong with the thing they pasted - never "invalid input".
 */
class VpsCode
{
    /**
     * Decode and validate. Returns the normalised fields; throws with a plain
     * sentence when the code is not usable.
     *
     * @return array<string, mixed>
     *
     * @throws AutoProxyException
     */
    public static function decode(string $raw): array
    {
        $trimmed = preg_replace('/\s+/', '', trim($raw)) ?? '';

        if ($trimmed === '') {
            throw new AutoProxyException('Paste the VPS code that the install command printed on your VPS.');
        }

        $json = static::base64UrlDecode($trimmed);

        if ($json === null) {
            throw new AutoProxyException('That does not look like a VPS code: it is not valid base64url. Copy the whole line the installer printed, without the surrounding box.');
        }

        $data = json_decode($json, true);

        if (!is_array($data)) {
            throw new AutoProxyException('That code decoded to something that is not a VPS code. Make sure you copied the code itself and not the command around it.');
        }

        $version = (int) ($data['v'] ?? 0);
        if ($version !== 1) {
            throw new AutoProxyException(sprintf(
                'This VPS code is version %s, and this plugin understands version 1. Update the plugin, or re-run the installer on the VPS.',
                $version !== 0 ? (string) $version : 'unknown',
            ));
        }

        $endpointIp = trim((string) ($data['endpoint_ip'] ?? ''));
        if (filter_var($endpointIp, FILTER_VALIDATE_IP, FILTER_FLAG_IPV4) === false) {
            throw new AutoProxyException('The VPS code has no usable IPv4 address for the VPS. Re-run the installer on the VPS and copy the new code.');
        }

        $apiPort = (int) ($data['api_port'] ?? 0);
        if ($apiPort < 1 || $apiPort > 65535) {
            throw new AutoProxyException('The VPS code has no usable API port. Re-run the installer on the VPS and copy the new code.');
        }

        $token = trim((string) ($data['token'] ?? ''));
        if ($token === '') {
            throw new AutoProxyException('The VPS code carries no API token, so the panel could not authenticate. Re-run the installer on the VPS.');
        }

        $pem = static::decodePem((string) ($data['api_ca_pem'] ?? ''));

        return [
            'api_url' => 'https://' . $endpointIp . ':' . $apiPort,
            'endpoint_ip' => $endpointIp,
            'api_port' => (string) $apiPort,
            'api_token' => $token,
            'api_ca_pem' => $pem,
            'api_spki_sha256' => trim((string) ($data['api_spki_sha256'] ?? '')),
            'wg_pubkey' => trim((string) ($data['wg_pubkey'] ?? '')),
            'wg_port' => (string) (int) ($data['wg_port'] ?? 0),
            'tunnel_subnet' => trim((string) ($data['tunnel_subnet'] ?? '')),
            'vps_tunnel_ip' => trim((string) ($data['vps_tunnel_ip'] ?? '')),
            'agent_version' => trim((string) ($data['version'] ?? '')),
        ];
    }

    /**
     * Store a decoded code: the certificate to a 0600 file, the token encrypted,
     * the rest as plain settings. Written certificate first - a stored token that
     * points at a VPS we cannot verify is worse than an incomplete save.
     *
     * @param array<string, mixed> $code
     *
     * @throws AutoProxyException
     */
    public static function store(array $code): void
    {
        static::writeCertificate((string) $code['api_ca_pem']);

        AutoProxySettings::setMany([
            'api_url' => $code['api_url'],
            'endpoint_ip' => $code['endpoint_ip'],
            'api_port' => $code['api_port'],
            'api_token' => $code['api_token'],
            'api_spki_sha256' => $code['api_spki_sha256'],
            'wg_pubkey' => $code['wg_pubkey'],
            'wg_port' => $code['wg_port'],
            'tunnel_subnet' => $code['tunnel_subnet'],
            'vps_tunnel_ip' => $code['vps_tunnel_ip'],
            'agent_version' => $code['agent_version'],
            // The file above is what curl reads; this copy is what survives a
            // panel container re-create (see AutoProxySettings::ensureCertificate).
            'api_ca_pem' => (string) $code['api_ca_pem'],
        ]);
    }

    /**
     * The agent's self-signed certificate, trusted as its own CA. Guzzle reads
     * this path on every request, so it must survive on disk and stay private.
     *
     * @throws AutoProxyException
     */
    public static function writeCertificate(string $pem): void
    {
        $path = AutoProxySettings::certPath();
        $directory = dirname($path);

        if (!is_dir($directory) && !@mkdir($directory, 0700, true) && !is_dir($directory)) {
            throw new AutoProxyException('Could not create ' . $directory . ' to store the VPS certificate. Check the panel\'s storage directory is writable.');
        }

        if (@file_put_contents($path, $pem) === false) {
            throw new AutoProxyException('Could not write the VPS certificate to ' . $path . '. Check the panel\'s storage directory is writable.');
        }

        @chmod($path, 0600);
    }

    /**
     * @throws AutoProxyException
     */
    protected static function decodePem(string $encoded): string
    {
        $encoded = preg_replace('/\s+/', '', $encoded) ?? '';

        if ($encoded === '') {
            throw new AutoProxyException('The VPS code carries no certificate, so the panel cannot verify it is talking to your VPS. Re-run the installer on the VPS.');
        }

        $pem = static::base64UrlDecode($encoded);

        if ($pem === null || !str_contains($pem, '-----BEGIN CERTIFICATE-----')) {
            throw new AutoProxyException('The certificate inside the VPS code is not readable. Copy the code again, in one piece.');
        }

        return rtrim($pem) . "\n";
    }

    /** Accepts base64url and plain base64, padded or not. */
    protected static function base64UrlDecode(string $value): ?string
    {
        $value = strtr($value, '-_', '+/');
        $padding = strlen($value) % 4;

        if ($padding !== 0) {
            $value .= str_repeat('=', 4 - $padding);
        }

        $decoded = base64_decode($value, true);

        return $decoded === false ? null : $decoded;
    }
}
