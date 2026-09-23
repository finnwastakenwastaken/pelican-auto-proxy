<?php

namespace Arrowtje\AutoProxy\Services;

use Arrowtje\AutoProxy\Exceptions\AutoProxyException;
use Arrowtje\AutoProxy\Models\ClientUpdate;
use Arrowtje\AutoProxy\Support\AutoProxySettings;
use Illuminate\Http\Client\ConnectionException;
use Illuminate\Http\Client\PendingRequest;
use Illuminate\Http\Client\Response;
use Illuminate\Support\Facades\Http;

/**
 * The only thing that talks to the VPS agent. No composer packages: Laravel Http.
 *
 * Transport: HTTPS to https://<vps ip>:<api port>, verified against the agent's
 * own self-signed certificate (shipped in the VPS code, stored 0600 by VpsCode).
 * OpenSSL accepts a self-signed certificate as its own trust anchor, and because
 * the certificate carries an iPAddress SAN the IP is checked too - which a bare
 * public-key pin with verification off would not do. When the code also carried
 * an SPKI hash we pin on top, belt and braces.
 *
 * Every error an admin can hit gets its own sentence: a wrong certificate, a
 * refused connection, a rejected token, a lockout and a rejected rule set are
 * five different problems with five different fixes.
 */
class AgentClient
{
    /**
     * @return array<string, mixed>
     *
     * @throws AutoProxyException
     */
    public function status(): array
    {
        return $this->decode($this->send(fn (PendingRequest $http, string $url) => $http->get($url . '/v1/status'), $this->statusTimeout()));
    }

    /**
     * @return array<string, mixed>
     *
     * @throws AutoProxyException
     */
    public function rules(): array
    {
        return $this->decode($this->send(fn (PendingRequest $http, string $url) => $http->get($url . '/v1/rules'), $this->statusTimeout()));
    }

    /**
     * Full replace of the agent's rule set.
     *
     * @param array<int, array<string, mixed>> $rules
     * @return array<string, mixed>
     *
     * @throws AutoProxyException
     */
    public function push(array $rules): array
    {
        return $this->decode($this->send(
            fn (PendingRequest $http, string $url) => $http->put($url . '/v1/rules', ['rules' => array_values($rules)]),
            $this->pushTimeout(),
        ));
    }

    /**
     * Every tunnel client the VPS knows about.
     *
     * @return array<int, array<string, mixed>>
     *
     * @throws AutoProxyException
     */
    public function peers(): array
    {
        $body = $this->decode($this->send(fn (PendingRequest $http, string $url) => $http->get($url . '/v1/peers'), $this->statusTimeout()));

        // The agent answers {"peers": [...]}, matching every other route, which
        // leaves room to add a field beside the list later. A bare list is still
        // accepted because it costs one line and older agents may send one.
        $peers = array_is_list($body) ? $body : ($body['peers'] ?? []);

        return array_values(array_filter(is_array($peers) ? $peers : [], 'is_array'));
    }

    /**
     * Create a peer. The join code comes back exactly once - the caller must
     * store it or it is gone, and the admin has to rotate to get a new one.
     *
     * @param string[] $lanCidrs
     * @return array{peer: array<string, mixed>, join_code: string}
     *
     * @throws AutoProxyException
     */
    public function createPeer(string $name, string $mode, array $lanCidrs = []): array
    {
        $payload = ['name' => $name, 'mode' => $mode];

        if ($lanCidrs !== []) {
            $payload['lan_cidrs'] = array_values($lanCidrs);
        }

        $body = $this->decode($this->send(
            fn (PendingRequest $http, string $url) => $http->post($url . '/v1/peers', $payload),
            $this->pushTimeout(),
        ));

        $joinCode = (string) ($body['join_code'] ?? '');

        if ($joinCode === '') {
            throw new AutoProxyException('The VPS created the tunnel client but returned no join code, so there is no command to run on the node. Remove the client on the Setup page and try again.');
        }

        return [
            'peer' => is_array($body['peer'] ?? null) ? $body['peer'] : [],
            'join_code' => $joinCode,
        ];
    }

    /** @throws AutoProxyException */
    public function deletePeer(string $id): void
    {
        $this->send(
            fn (PendingRequest $http, string $url) => $http->delete($url . '/v1/peers/' . rawurlencode($id)),
            $this->pushTimeout(),
        );

        // The peer is gone, so is its "allow remote updates" switch. Here rather
        // than at each caller: the Setup page and the CLI both delete peers.
        try {
            ClientUpdate::forget($id);
        } catch (\Throwable) {
            // No database (the contract test harness) or not migrated yet.
        }
    }

    /**
     * New keypair for an existing peer: the old join code and the running client
     * stop working, which is exactly what "the code leaked" calls for.
     *
     * @throws AutoProxyException
     */
    public function rotatePeer(string $id): string
    {
        $body = $this->decode($this->send(
            fn (PendingRequest $http, string $url) => $http->post($url . '/v1/peers/' . rawurlencode($id) . '/rotate'),
            $this->pushTimeout(),
        ));

        $joinCode = (string) ($body['join_code'] ?? '');

        if ($joinCode === '') {
            throw new AutoProxyException('The VPS rotated the tunnel client but returned no new join code.');
        }

        return $joinCode;
    }

    /**
     * Ask a tunnel client to install another client release, or withdraw that
     * request with null. The agent only stores the number; the client decides
     * whether it may act on it (remote updates on there too, newer than what it
     * runs, an official release whose checksum verifies). Needs agent 0.3.0.
     *
     * @return array<string, mixed> the peer's "client" object as the agent now holds it
     *
     * @throws AutoProxyException
     */
    public function setClientVersion(string $peerId, ?string $version): array
    {
        $body = $this->decode($this->send(
            fn (PendingRequest $http, string $url) => $http->put(
                $url . '/v1/peers/' . rawurlencode($peerId) . '/client',
                ['desired_version' => $version],
            ),
            $this->pushTimeout(),
        ));

        return is_array($body['client'] ?? null) ? $body['client'] : [];
    }

    /**
     * Rotate the API token. Stored here on purpose: the old token stops working
     * the moment the agent answers, so losing the new one on the way back to the
     * caller would lock the panel out of its own VPS.
     *
     * @throws AutoProxyException
     */
    public function rotateToken(): string
    {
        $body = $this->decode($this->send(
            fn (PendingRequest $http, string $url) => $http->post($url . '/v1/token/rotate'),
            $this->pushTimeout(),
        ));

        $token = (string) ($body['token'] ?? '');

        if ($token === '') {
            throw new AutoProxyException('The VPS did not return a new token, so nothing was changed.');
        }

        AutoProxySettings::set('api_token', $token);

        return $token;
    }

    // --- transport ----------------------------------------------------------

    /**
     * @param callable(PendingRequest, string): Response $call
     *
     * @throws AutoProxyException
     */
    protected function send(callable $call, int $timeout): Response
    {
        $url = AutoProxySettings::apiUrl();
        $token = AutoProxySettings::apiToken();

        if ($url === '') {
            throw new AutoProxyException('No VPS is connected yet. Open Auto Proxy -> Setup and paste the VPS code from your VPS.');
        }

        if ($token === '') {
            throw new AutoProxyException('No API token is stored. Open Auto Proxy -> Setup and paste the VPS code again.');
        }

        try {
            $response = $call($this->http($token, $timeout), $url);
        } catch (ConnectionException $exception) {
            throw new AutoProxyException($this->connectionMessage($url, $exception->getMessage()));
        }

        if ($response->failed()) {
            throw new AutoProxyException($this->httpMessage($url, $response));
        }

        return $response;
    }

    /**
     * @throws AutoProxyException
     */
    protected function http(string $token, int $timeout): PendingRequest
    {
        $options = ['verify' => $this->verifyOption()];

        $pin = AutoProxySettings::spkiPin();

        // Pinning needs curl; on any other handler the option is ignored, so the
        // stored certificate stays the thing that actually protects the request.
        if ($pin !== '' && config('autoproxy.pin_spki', true) && defined('CURLOPT_PINNEDPUBLICKEY')) {
            $options['curl'] = [CURLOPT_PINNEDPUBLICKEY => 'sha256//' . $pin];
        }

        return Http::withToken($token)
            ->acceptJson()
            ->withOptions($options)
            ->connectTimeout((int) config('autoproxy.connect_timeout', 5))
            ->timeout($timeout)
            // Retry only when the connection itself failed (timeout, reset,
            // handshake cut short), never on an HTTP answer: a 4xx/5xx is a
            // real reply and repeating it would only hide it.
            ->retry(
                1 + max(0, (int) config('autoproxy.retries', 1)),
                (int) config('autoproxy.retry_sleep_ms', 500),
                fn (\Throwable $exception): bool => $exception instanceof ConnectionException,
                throw: false,
            );
    }

    /**
     * The stored certificate, or a hard failure. Never `false`, and never a
     * silent fall back to the system trust store: a self-signed agent
     * certificate would never be in there, so "verify with what we have or do
     * not talk at all" is the only safe pair of options.
     *
     * @throws AutoProxyException
     */
    protected function verifyOption(): string
    {
        $path = AutoProxySettings::certPath();

        if (!is_file($path)) {
            throw new AutoProxyException(
                'The VPS certificate is missing from ' . $path . ', so the panel cannot prove it is talking to your VPS. '
                . 'Open Auto Proxy -> Setup and paste the VPS code again.'
            );
        }

        return $path;
    }

    protected function connectionMessage(string $url, string $error): string
    {
        $lower = strtolower($error);

        // curl's wording changes between releases ("Connection refused" became
        // "Could not connect to server"), so classify on the error NUMBER first
        // and keep the prose match only as a fallback. Getting this wrong is not
        // cosmetic: it is the difference between "your VPS is down" and "someone
        // is intercepting this connection".
        $code = preg_match('/curl error (\\d+)/i', $error, $matches) ? (int) $matches[1] : 0;

        // 60 cert verify, 77 cannot read the CA file, 35/58/59/83 TLS handshake,
        // 51 name mismatch. 90 is a pinned-key mismatch and is handled first.
        $tlsCodes = [35, 51, 58, 59, 60, 66, 77, 80, 83];

        if ($code === 90 || str_contains($lower, 'pinned public key') || str_contains($lower, 'pinnedpublickey')) {
            return 'The VPS answered with a different key than the one in your VPS code (certificate pin mismatch) at ' . $url . '. '
                . 'Either the agent\'s certificate was regenerated - re-run the installer and paste the new VPS code - or something is intercepting the connection.';
        }

        if (in_array($code, $tlsCodes, true) || str_contains($lower, 'certificate') || str_contains($lower, 'ssl') || str_contains($lower, 'tls') || str_contains($lower, 'trust anchor')) {
            return 'The panel could not verify the VPS certificate at ' . $url . ': ' . $error . '. '
                . 'This happens when the VPS IP changed, the agent regenerated its certificate, or the stored certificate is damaged. '
                . 'Re-run the installer on the VPS and paste the new VPS code into Setup.';
        }

        if ($code === 7 || str_contains($lower, 'connection refused') || str_contains($lower, 'could not connect') || str_contains($lower, 'failed to connect')) {
            return 'Nothing is listening at ' . $url . '. The agent is probably stopped: run `systemctl status autoproxy-agent` on the VPS.';
        }

        if ($code === 28 || str_contains($lower, 'timed out') || str_contains($lower, 'timeout')) {
            return 'The VPS did not answer in time at ' . $url . '. Check the VPS is up and that its firewall lets your panel reach the API port.';
        }

        return 'Cannot reach the VPS at ' . $url . ': ' . $error;
    }

    protected function httpMessage(string $url, Response $response): string
    {
        $status = $response->status();

        if ($status === 401 || $status === 403) {
            return 'The VPS rejected the API token (HTTP ' . $status . '). The token was rotated on the VPS, or the code you pasted belongs to another VPS. '
                . 'Re-run `autoproxy-agent show-code` on the VPS and paste the code into Setup again.';
        }

        if ($status === 429) {
            $retryAfter = (int) $response->header('Retry-After');

            return 'The VPS has locked this panel out after too many failed token attempts (HTTP 429)'
                . ($retryAfter > 0 ? ', and will accept requests again in ' . $this->humanSeconds($retryAfter) . '.' : '.')
                . ' Fix the token in Setup first, otherwise the lockout starts again on the next try.';
        }

        if ($status === 422) {
            // Two different 422 bodies, because two different things can be
            // wrong. A rejected rule set comes back as {"rejected": [{id, reason}]}
            // from PUT /v1/rules; a bad peer request comes back as {"error": "..."}
            // from POST /v1/peers and the rotate route. Showing the raw JSON of
            // the second one to an admin helps nobody.
            $rejected = $response->json('rejected');

            if (is_array($rejected) && $rejected !== []) {
                $lines = [];
                foreach ($rejected as $entry) {
                    if (!is_array($entry)) {
                        continue;
                    }
                    $id = trim((string) ($entry['id'] ?? ''));
                    $lines[] = '- ' . ($id === '' ? 'the whole set' : $id) . ': ' . (string) ($entry['reason'] ?? 'no reason given');
                }

                return 'The VPS refused ' . count($lines) . ' of the forwards, so nothing was changed:' . "\n" . implode("\n", $lines);
            }

            $error = trim((string) ($response->json('error') ?? ''));

            if ($error !== '') {
                return 'The VPS refused that: ' . $error;
            }

            return 'The VPS refused the request: ' . $this->body($response);
        }

        if ($status === 404 && str_ends_with((string) parse_url((string) $response->effectiveUri(), PHP_URL_PATH), '/client')) {
            return 'The VPS agent does not know client updates (HTTP 404). Update the agent on the VPS to 0.3.0 or newer first.';
        }

        if ($status >= 500) {
            return 'The VPS agent hit an internal error (HTTP ' . $status . '): ' . $this->body($response) . '. Check `journalctl -u autoproxy-agent` on the VPS.';
        }

        return 'The VPS answered HTTP ' . $status . ' at ' . $url . ': ' . $this->body($response);
    }

    protected function humanSeconds(int $seconds): string
    {
        if ($seconds < 120) {
            return $seconds . ' seconds';
        }

        return (int) ceil($seconds / 60) . ' minutes';
    }

    protected function statusTimeout(): int
    {
        return (int) config('autoproxy.status_timeout', 2);
    }

    protected function pushTimeout(): int
    {
        return (int) config('autoproxy.push_timeout', 5);
    }

    /** @return array<string, mixed> */
    protected function decode(Response $response): array
    {
        $decoded = $response->json();

        return is_array($decoded) ? $decoded : [];
    }

    protected function body(Response $response): string
    {
        $body = trim($response->body());

        return $body === '' ? '(empty response)' : mb_substr($body, 0, 1000);
    }
}
