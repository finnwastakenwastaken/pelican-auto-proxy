<?php

/**
 * Drive the plugin's REAL AgentClient against the REAL agent binary and print
 * every request/response pair.
 *
 * Why this exists: fake-agent.py is a test double written by hand, so "the
 * plugin works against the fake agent" proves the two agree with each other,
 * not that either agrees with the Go code that ships. This runs the class the
 * panel actually uses - same Laravel HTTP client, same Guzzle, same curl
 * options, same TLS verification against the agent's own certificate - against
 * the agent compiled from agent/.
 *
 * Runs inside a php:8.4-cli container that shares the agent container's network
 * namespace. Everything Laravel it needs is the HTTP client; the panel, its
 * database and Filament are not involved, so AutoProxySettings is seeded
 * through its own static cache instead of a settings table. That means the one
 * thing this does NOT exercise is persistence: rotateToken()'s write back to
 * the settings table is stubbed out and reported as such.
 *
 * Usage (inside the container):
 *   php probe.php <path to vps-code> <path to agent.pem>
 */

declare(strict_types=1);

use Arrowtje\AutoProxy\Exceptions\AutoProxyException;
use Arrowtje\AutoProxy\Services\AgentClient;
use Arrowtje\AutoProxy\Support\AutoProxySettings;
use Illuminate\Config\Repository;
use Illuminate\Container\Container;
use Illuminate\Http\Client\Factory;
use Illuminate\Support\Facades\Facade;

require '/vendor/autoload.php';

$repo = '/repo';
$storage = '/tmp/storage';

// --- the few framework helpers the plugin's support classes reach for -------

// config(), app() and storage_path() live in illuminate/foundation, which the
// panel has and this harness does not. They are three lines each; pulling the
// whole framework in to get them would drag in a database and a filesystem the
// contract test has no use for.
if (!function_exists('storage_path')) {
    function storage_path(string $path = ''): string
    {
        return rtrim('/tmp/storage/' . ltrim($path, '/'), '/');
    }
}

if (!function_exists('app')) {
    function app(?string $abstract = null): mixed
    {
        $container = Container::getInstance();

        return $abstract === null ? $container : $container->make($abstract);
    }
}

if (!function_exists('config')) {
    function config(?string $key = null, mixed $default = null): mixed
    {
        /** @var Repository $repository */
        $repository = Container::getInstance()->make('config');

        return $key === null ? $repository : $repository->get($key, $default);
    }
}

// --- autoload the plugin, PSR-4, no composer ------------------------------

spl_autoload_register(static function (string $class) use ($repo): void {
    $prefix = 'Arrowtje\\AutoProxy\\';

    if (!str_starts_with($class, $prefix)) {
        return;
    }

    $relative = str_replace('\\', '/', substr($class, strlen($prefix)));
    $file = $repo . '/plugin/autoproxy/src/' . $relative . '.php';

    if (is_file($file)) {
        require $file;
    }
});

// --- minimal Laravel: container, config, the Http facade -------------------

$app = new Container();
Container::setInstance($app);
$app->instance('config', new Repository(['autoproxy' => require $repo . '/plugin/autoproxy/config/autoproxy.php']));
$app->singleton(Factory::class, static fn () => new Factory());
Facade::setFacadeApplication($app);

// --- seed the settings the Setup page would have stored --------------------

/**
 * AutoProxySettings reads a database table. Here there is none, so this
 * subclass fills the same static cache the real class reads from. Nothing
 * about AgentClient changes: it still calls AutoProxySettings::apiUrl() and
 * friends.
 */
final class SeededSettings extends AutoProxySettings
{
    /** @param array<string, mixed> $values */
    public static function seed(array $values): void
    {
        static::$cache = $values;
    }
}

$codeFile = $argv[1] ?? '/shared/vps-code';
$pemFile = $argv[2] ?? '/shared/agent.pem';

$raw = trim((string) file_get_contents($codeFile));
$json = base64_decode(strtr($raw, '-_', '+/') . str_repeat('=', (4 - strlen($raw) % 4) % 4), true);
$code = json_decode((string) $json, true);

if (!is_array($code)) {
    fwrite(STDERR, "could not decode the VPS code\n");
    exit(1);
}

@mkdir($storage . '/app/autoproxy', 0700, true);
copy($pemFile, $storage . '/app/autoproxy/agent.pem');

SeededSettings::seed([
    'api_url' => 'https://' . $code['endpoint_ip'] . ':' . $code['api_port'],
    'api_token' => $code['token'],
    'api_spki_sha256' => $code['api_spki_sha256'] ?? '',
    'endpoint_ip' => $code['endpoint_ip'],
]);

// --- reporting -------------------------------------------------------------

$failures = 0;

function show(string $title, string $request, callable $call): void
{
    global $failures;

    echo "\n";
    echo "--------------------------------------------------------------------\n";
    echo $title . "\n";
    echo "REQUEST   " . $request . "\n";

    try {
        $result = $call();
        echo "RESPONSE  " . json_encode($result, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES) . "\n";
    } catch (AutoProxyException $e) {
        echo "REFUSED   " . $e->getMessage() . "\n";
    } catch (Throwable $e) {
        $failures++;
        echo "UNEXPECTED " . get_class($e) . ': ' . $e->getMessage() . "\n";
    }
}

/** Assert and record, so the script's exit status means something. */
function check(string $what, bool $ok): void
{
    global $failures;

    if ($ok) {
        echo "PASS  " . $what . "\n";
    } else {
        $failures++;
        echo "FAIL  " . $what . "\n";
    }
}

$client = new AgentClient();

// 1. status ------------------------------------------------------------------
$status = [];
show('1. GET /v1/status', 'GET {api}/v1/status', function () use ($client, &$status) {
    return $status = $client->status();
});
check('status has version, uptime_s, wg, peers{total,healthy}, applied, applied_at, last_error',
    array_keys($status) !== [] && isset($status['version'], $status['uptime_s'], $status['wg'], $status['applied'])
    && array_key_exists('applied_at', $status) && array_key_exists('last_error', $status)
    && isset($status['peers']['total'], $status['peers']['healthy']));
check('wg block is {iface, peers, handshake_age_s, rx, tx} and carries no listen_port',
    is_array($status['wg'] ?? null) && !array_key_exists('listen_port', $status['wg'])
    && array_key_exists('handshake_age_s', $status['wg']));

// 2. create a real-IP peer ---------------------------------------------------
$real = [];
show('2. POST /v1/peers (real-IP mode)', 'POST {api}/v1/peers {"name":"wings-1","mode":"real"}',
    function () use ($client, &$real) {
        $real = $client->createPeer('wings-1', 'real');

        return ['peer' => $real['peer'], 'join_code' => substr($real['join_code'], 0, 24) . '... (elided)'];
    });
check('created peer has id, name, public_key, tunnel_ip, mode, lan_cidrs, handshake_age_s, rx, tx, created_at',
    isset($real['peer']['id'], $real['peer']['public_key'], $real['peer']['tunnel_ip'], $real['peer']['mode'])
    && array_key_exists('lan_cidrs', $real['peer']) && array_key_exists('handshake_age_s', $real['peer']));
// array_key_exists, not ??: the whole point is that the key is present AND its
// value is null. "?? 'missing'" cannot tell those two apart.
check('a peer that has never handshaked reports handshake_age_s: null',
    array_key_exists('handshake_age_s', $real['peer'] ?? []) && $real['peer']['handshake_age_s'] === null);
check('lan_cidrs on a real-IP peer is [] and not null', ($real['peer']['lan_cidrs'] ?? null) === []);
check('join_code came back', ($real['join_code'] ?? '') !== '');

// 3. create a site peer ------------------------------------------------------
$site = [];
show('3. POST /v1/peers (site mode)', 'POST {api}/v1/peers {"name":"lan-box","mode":"site","lan_cidrs":["10.0.0.0/24"]}',
    function () use ($client, &$site) {
        $site = $client->createPeer('lan-box', 'site', ['10.0.0.0/24']);

        return ['peer' => $site['peer'], 'join_code' => substr($site['join_code'], 0, 24) . '... (elided)'];
    });
check('site peer keeps its lan_cidrs', ($site['peer']['lan_cidrs'] ?? []) === ['10.0.0.0/24']);

// 4. peers list --------------------------------------------------------------
$peers = [];
show('4. GET /v1/peers', 'GET {api}/v1/peers', function () use ($client, &$peers) {
    return $peers = $client->peers();
});
// Count, not identity: the harness mints two more peers up front so the client
// decoder has real join codes to chew on, so an absolute count here would only
// measure the harness.
$ids = array_column($peers, 'id');
check('peers() unwraps the agent\'s {"peers": [...]} body into a plain list',
    is_array($peers) && array_is_list($peers) && $peers !== []);
check('both peers just created are in the list',
    in_array($real['peer']['id'] ?? '', $ids, true) && in_array($site['peer']['id'] ?? '', $ids, true));
check('every entry carries the peer fields the plugin reads',
    $peers !== [] && !array_filter($peers, fn ($p) => !isset($p['id'], $p['name'], $p['tunnel_ip'], $p['mode'])
        || !array_key_exists('lan_cidrs', $p) || !array_key_exists('handshake_age_s', $p)));

// 5. push a real rule and a site rule ----------------------------------------
$realId = (string) ($real['peer']['id'] ?? '');
$siteId = (string) ($site['peer']['id'] ?? '');

$rules = [
    ['id' => 'alloc-1', 'proto' => 'both', 'public_port' => 9445, 'target_peer' => $realId, 'note' => 'real player IPs'],
    ['id' => 'alloc-2', 'proto' => 'udp', 'public_port' => 27015, 'target_ip' => '10.0.0.10', 'via_peer' => $siteId, 'note' => 'site mode'],
];

$applied = [];
show('5. PUT /v1/rules (one real-IP rule, one site rule)',
    'PUT {api}/v1/rules ' . json_encode(['rules' => $rules], JSON_UNESCAPED_SLASHES),
    function () use ($client, $rules, &$applied) {
        return $applied = $client->push($rules);
    });
check('push returns applied{tcp,udp,rules} and applied_at',
    isset($applied['applied']['rules'], $applied['applied']['tcp'], $applied['applied']['udp'], $applied['applied_at']));
check('both rules counted', ($applied['applied']['rules'] ?? 0) === 2);

// 6. the 422 that a rule set gets: {"rejected": [{id, reason}]} ---------------
show('6. PUT /v1/rules with a reserved port and a site target outside the peer\'s LAN (expect 422)',
    'PUT {api}/v1/rules ' . json_encode(['rules' => [
        ['id' => 'bad-reserved', 'proto' => 'tcp', 'public_port' => 22, 'target_peer' => $realId],
        ['id' => 'bad-outside', 'proto' => 'tcp', 'public_port' => 30000, 'target_ip' => '192.168.5.5', 'via_peer' => $siteId],
    ]], JSON_UNESCAPED_SLASHES),
    function () use ($client, $realId, $siteId) {
        return $client->push([
            ['id' => 'bad-reserved', 'proto' => 'tcp', 'public_port' => 22, 'target_peer' => $realId],
            ['id' => 'bad-outside', 'proto' => 'tcp', 'public_port' => 30000, 'target_ip' => '192.168.5.5', 'via_peer' => $siteId],
        ]);
    });

// 7. the OTHER 422: a bad peer request, which answers {"error": "..."} --------
show('7. POST /v1/peers in site mode with no lan_cidrs (expect 422 with an "error" body)',
    'POST {api}/v1/peers {"name":"no-ranges","mode":"site"}',
    fn () => $client->createPeer('no-ranges', 'site'));

// 8. the asymmetry the plugin UI has to respect ------------------------------
show('8. PUT /v1/rules pointing target_peer at a SITE peer (expect 422)',
    'PUT {api}/v1/rules [{"id":"wrong-mode","proto":"tcp","public_port":30001,"target_peer":"<site peer>"}]',
    fn () => $client->push([['id' => 'wrong-mode', 'proto' => 'tcp', 'public_port' => 30001, 'target_peer' => $siteId]]));

show('9. PUT /v1/rules pointing via_peer at a REAL-IP peer (expect 422)',
    'PUT {api}/v1/rules [{"id":"wrong-via","proto":"tcp","public_port":30002,"target_ip":"10.0.0.10","via_peer":"<real peer>"}]',
    fn () => $client->push([['id' => 'wrong-via', 'proto' => 'tcp', 'public_port' => 30002, 'target_ip' => '10.0.0.10', 'via_peer' => $realId]]));

// 10. token rotate -----------------------------------------------------------
// Called through the Http client directly rather than AgentClient::rotateToken(),
// because that method's last act is a write to the settings table and there is
// no database here. The request and the response body are the real thing.
echo "\n";
echo "--------------------------------------------------------------------\n";
echo "10. POST /v1/token/rotate\n";
echo "REQUEST   POST {api}/v1/token/rotate (no body)\n";

$rotate = Illuminate\Support\Facades\Http::withToken((string) $code['token'])
    ->acceptJson()
    ->withOptions(['verify' => $storage . '/app/autoproxy/agent.pem'])
    ->timeout(5)
    ->post('https://' . $code['endpoint_ip'] . ':' . $code['api_port'] . '/v1/token/rotate');

$rotateBody = (array) $rotate->json();
echo "RESPONSE  HTTP " . $rotate->status() . ' '
    . json_encode(['token' => '<32 bytes hex, elided>', 'warning' => $rotateBody['warning'] ?? null], JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES) . "\n";
check('token rotate answers 200 with {token, warning}',
    $rotate->status() === 200 && ($rotateBody['token'] ?? '') !== '' && ($rotateBody['warning'] ?? '') !== '');
check('the rotated token is 64 hex characters', (bool) preg_match('/^[0-9a-f]{64}$/', (string) ($rotateBody['token'] ?? '')));

// The old token is dead from this moment, which is exactly what the next block
// needs anyway.
echo "\n";
echo "--------------------------------------------------------------------\n";
echo "11. the OLD token after a rotate, five times, then once more (expect 401 x5, then 429)\n";

SeededSettings::seed([
    'api_url' => 'https://' . $code['endpoint_ip'] . ':' . $code['api_port'],
    'api_token' => (string) $code['token'],
    'api_spki_sha256' => $code['api_spki_sha256'] ?? '',
]);

$messages = [];
for ($i = 1; $i <= 6; $i++) {
    try {
        $client->status();
        $messages[] = "attempt $i: unexpectedly succeeded";
    } catch (AutoProxyException $e) {
        $messages[] = "attempt $i: " . $e->getMessage();
    }
}

foreach ($messages as $line) {
    echo "  " . $line . "\n";
}

check('the first five bad tokens are reported as a rejected token',
    str_contains($messages[0], 'rejected the API token') && str_contains($messages[4], 'rejected the API token'));
check('the sixth is reported as a lockout with a wait', str_contains($messages[5], 'locked this panel out'));
check('the lockout message names how long to wait', (bool) preg_match('/\d+ (seconds|minutes)/', $messages[5]));

echo "\n";
echo "NOT EXERCISED: rotateToken() writes the new token to the settings table,\n";
echo "which needs the panel's database. The HTTP half of it is unverified here.\n";
echo "\n";
echo ($failures === 0 ? "ALL CHECKS PASSED\n" : $failures . " CHECK(S) FAILED\n");

exit($failures === 0 ? 0 : 1);
