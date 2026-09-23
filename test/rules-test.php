<?php

/**
 * Standalone checks for the pure parts of the plugin: how an allocation becomes
 * a forwarding rule, conflict detection, the canonical hash, the wire shape and
 * the RFC1918 test. No Laravel, no database, no VPS.
 *
 *   docker run --rm -v "$PWD":/w -w /w php:8.4-cli php test/rules-test.php
 *
 * Every address here is from a documentation range (RFC 5737) or RFC1918.
 *
 * To watch a gate fail on purpose, flip the ownership test in
 * RuleSetBuilder::partitionAllocationRows() from
 *   if (($row['server_id'] ?? null) === null) {   to   !== null
 * and re-run: the "unassigned" checks below must go red.
 */

// Stand-ins for the panel classes RuleSetBuilder touches at runtime. PHP resolves
// these at call time, so the pure logic runs without Laravel.
namespace App\Models {
    class Allocation
    {
        public function __construct(public ?string $ip = null, public ?int $node_id = null) {}
    }
}

namespace Arrowtje\AutoProxy\Models {
    /** Only the mode constants are reached from the pure code path. */
    class NodeSetting
    {
        public const MODE_REAL = 'real';

        public const MODE_SITE = 'site';
    }

    /** ForwardRuleInput reads the protocol list off the model and nothing else. */
    class ForwardRule
    {
        public const PROTOCOLS = ['tcp' => 'TCP', 'udp' => 'UDP', 'both' => 'TCP + UDP'];
    }
}

// The two input helpers use Laravel's blank()/filled(). Both are one-liners on
// null and string, which is all they are ever handed here.
namespace {
    if (!function_exists('blank')) {
        function blank(mixed $value): bool
        {
            return $value === null || (is_string($value) && trim($value) === '');
        }
    }

    if (!function_exists('filled')) {
        function filled(mixed $value): bool
        {
            return !blank($value);
        }
    }
}

namespace Arrowtje\AutoProxy\Tests {

require __DIR__ . '/../plugin/autoproxy/src/Support/Ip.php';
require __DIR__ . '/../plugin/autoproxy/src/Services/RuleSetBuilder.php';
require __DIR__ . '/../plugin/autoproxy/src/Support/PeerHealth.php';
require __DIR__ . '/../plugin/autoproxy/src/Support/ForwardRuleInput.php';
require __DIR__ . '/../plugin/autoproxy/src/Support/ClientInput.php';
require __DIR__ . '/../plugin/autoproxy/src/Support/ClientVersion.php';

use Arrowtje\AutoProxy\Services\RuleSetBuilder;
use Arrowtje\AutoProxy\Support\ClientInput;
use Arrowtje\AutoProxy\Support\ClientVersion;
use Arrowtje\AutoProxy\Support\ForwardRuleInput;
use Arrowtje\AutoProxy\Support\Ip;
use Arrowtje\AutoProxy\Support\PeerHealth;
use PDO;

$failures = 0;
$checks = 0;

function check(string $name, bool $ok): void
{
    global $failures, $checks;
    $checks++;
    if (!$ok) {
        $failures++;
        echo "FAIL  $name\n";

        return;
    }
    echo "ok    $name\n";
}

/** A rule in the plugin's internal shape: one target form filled, the other null. */
function peerRule(string $id, string $proto, int $port, ?int $end, string $peer, ?int $targetPort = null): array
{
    return [
        'id' => $id, 'proto' => $proto, 'public_port' => $port, 'public_port_end' => $end,
        'target_peer' => $peer, 'target_ip' => null, 'via_peer' => null,
        'target_port' => $targetPort, 'note' => $id,
    ];
}

function lanRule(string $id, string $proto, int $port, ?int $end, string $ip, string $via = 'peer-a', ?int $targetPort = null): array
{
    return [
        'id' => $id, 'proto' => $proto, 'public_port' => $port, 'public_port_end' => $end,
        'target_peer' => null, 'target_ip' => $ip, 'via_peer' => $via,
        'target_port' => $targetPort, 'note' => $id,
    ];
}

function ids(array $rules): array
{
    return array_map(fn ($r) => $r['id'], $rules);
}

/** One allocation row as the builder sees it after reading the database. */
function allocRow(array $overrides = []): array
{
    return $overrides + [
        'allocation_id' => 1,
        'server' => 'a server',
        'server_id' => 5,
        'node' => 'node-a',
        'node_id' => 1,
        'ip' => '0.0.0.0',
        'port' => 25565,
        'alias' => 'play.example.com',
        'proxied' => true,
        'mode' => 'real',
        'peer_id' => 'peer-a',
        'via_peer_id' => null,
        'node_lan_ip' => null,
    ];
}

$builder = new RuleSetBuilder();

// --- RFC1918 --------------------------------------------------------------
check('10.x is private', Ip::isPrivateV4('10.0.0.10'));
check('172.16.x is private', Ip::isPrivateV4('172.16.5.5'));
check('172.31.x is private', Ip::isPrivateV4('172.31.255.254'));
check('172.32.x is NOT private', !Ip::isPrivateV4('172.32.0.1'));
check('192.168.x is private', Ip::isPrivateV4('192.168.10.20'));
check('0.0.0.0 is rejected', !Ip::isPrivateV4('0.0.0.0'));
check('a public IP is rejected', !Ip::isPrivateV4('203.0.113.10'));
check('IPv6 is rejected', !Ip::isPrivateV4('fd00::1'));
check('empty is rejected', !Ip::isPrivateV4(''));
check('a hostname is rejected', !Ip::isPrivateV4('node.example.com'));

// --- node modes: what an allocation turns into ----------------------------
// Real mode is the headline feature: the rule points at the node's own tunnel
// client, so the game server sees the player's address, not the VPS's.
[$rules, $notConfigured, $notProxied, $unassigned] = $builder->partitionAllocationRows([
    allocRow(['allocation_id' => 1, 'peer_id' => 'peer-a']),
]);
check('real mode targets the node\'s peer, never an IP',
    count($rules) === 1 && $rules[0]['target_peer'] === 'peer-a' && $rules[0]['target_ip'] === null && $rules[0]['via_peer'] === null);
check('a real-mode rule keeps the allocation id and port',
    $rules[0]['id'] === 'alloc-1' && $rules[0]['public_port'] === 25565 && $rules[0]['proto'] === 'both');

[$rules, $notConfigured] = $builder->partitionAllocationRows([
    allocRow(['allocation_id' => 2, 'peer_id' => null]),
]);
check('real mode without a client yet yields no rule, with a reason',
    $rules === [] && count($notConfigured) === 1 && $notConfigured[0]['reason'] === 'no tunnel client on this node yet');

// Site mode: a LAN address reached through someone else's client.
[$rules] = $builder->partitionAllocationRows([
    allocRow(['allocation_id' => 3, 'mode' => 'site', 'peer_id' => null, 'via_peer_id' => 'peer-b', 'node_lan_ip' => '10.0.0.10']),
]);
check('site mode targets the node LAN IP through the chosen client',
    count($rules) === 1 && $rules[0]['target_ip'] === '10.0.0.10' && $rules[0]['via_peer'] === 'peer-b' && $rules[0]['target_peer'] === null);

[$rules] = $builder->partitionAllocationRows([
    allocRow(['allocation_id' => 4, 'mode' => 'site', 'via_peer_id' => 'peer-b', 'ip' => '10.0.0.11', 'node_lan_ip' => '10.0.0.10']),
]);
check('a concrete private allocation IP beats the node LAN IP',
    count($rules) === 1 && $rules[0]['target_ip'] === '10.0.0.11');

[$rules] = $builder->partitionAllocationRows([
    allocRow(['allocation_id' => 5, 'mode' => 'site', 'via_peer_id' => 'peer-b', 'ip' => '0.0.0.0', 'node_lan_ip' => '10.0.0.10']),
]);
check('0.0.0.0 falls back to the node LAN IP', $rules[0]['target_ip'] === '10.0.0.10');

[$rules] = $builder->partitionAllocationRows([
    allocRow(['allocation_id' => 6, 'mode' => 'site', 'via_peer_id' => 'peer-b', 'ip' => '203.0.113.10', 'node_lan_ip' => '10.0.0.10']),
]);
check('a public allocation IP is never used as a target', $rules[0]['target_ip'] === '10.0.0.10');

[$rules, $notConfigured] = $builder->partitionAllocationRows([
    allocRow(['allocation_id' => 7, 'mode' => 'site', 'via_peer_id' => 'peer-b', 'node_lan_ip' => null]),
]);
check('site mode without a LAN IP is skipped, not guessed',
    $rules === [] && $notConfigured[0]['reason'] === 'site mode without a LAN IP for this node');

[$rules, $notConfigured] = $builder->partitionAllocationRows([
    allocRow(['allocation_id' => 8, 'mode' => 'site', 'via_peer_id' => null, 'node_lan_ip' => '10.0.0.10']),
]);
check('site mode without a client to route through is skipped',
    $rules === [] && $notConfigured[0]['reason'] === 'site mode without a tunnel client to route through');

// --- unproxied nodes are ignored, and said out loud ------------------------
[$rules, $notConfigured, $notProxied, $unassigned] = $builder->partitionAllocationRows([
    allocRow(['allocation_id' => 9, 'proxied' => false, 'node' => 'node-off']),
]);
check('an allocation on an unproxied node gets no rule',
    $rules === [] && count($notProxied) === 1 && $notProxied[0]['reason'] === 'node not proxied');
check('an unproxied node is not reported as half configured',
    $notConfigured === [] && $unassigned === []);
check('the unproxied entry names the node so it can be found',
    $notProxied[0]['node'] === 'node-off' && $notProxied[0]['allocation_id'] === 9);

// An unproxied node that is ALSO missing a peer must read as "not proxied":
// that is the first thing to fix, and the other reason is a consequence.
[$rules, $notConfigured, $notProxied] = $builder->partitionAllocationRows([
    allocRow(['allocation_id' => 10, 'proxied' => false, 'peer_id' => null]),
]);
check('not proxied outranks "no client yet"', count($notProxied) === 1 && $notConfigured === []);

// --- assigned vs unassigned ------------------------------------------------
// The alias rewrite always applies, but a port only opens once a server is
// assigned. A stopped server still counts: server_id stays set.
[$rules, $notConfigured, $notProxied, $unassigned] = $builder->partitionAllocationRows([
    allocRow(['allocation_id' => 11, 'server_id' => 5]),
    allocRow(['allocation_id' => 12, 'server_id' => null]),
    allocRow(['allocation_id' => 13, 'server_id' => 7, 'peer_id' => null]),
]);
check('assigned allocation with a target becomes a rule', count($rules) === 1 && $rules[0]['id'] === 'alloc-11');
check('unassigned allocation gets no rule despite a resolvable target',
    count($unassigned) === 1 && $unassigned[0]['allocation_id'] === 12);
check('unassigned is not reported as half configured',
    count($notConfigured) === 1 && $notConfigured[0]['allocation_id'] === 13);

[$rules] = $builder->partitionAllocationRows([allocRow(['allocation_id' => 14, 'server_id' => 9])]);
check('a stopped (but assigned) server still gets a rule', count($rules) === 1);

// Unassigned outranks everything else: no server means nothing would answer.
[$rules, $notConfigured, $notProxied, $unassigned] = $builder->partitionAllocationRows([
    allocRow(['allocation_id' => 15, 'server_id' => null, 'proxied' => false]),
]);
check('unassigned outranks not-proxied', count($unassigned) === 1 && $notProxied === []);

// --- conflicts, across both target forms ----------------------------------
[$kept, $conflicts] = $builder->resolveConflicts([
    peerRule('alloc-1', 'both', 9445, null, 'peer-a'),
    peerRule('manual-1', 'both', 9445, null, 'peer-b'),
]);
check('same port, different peer: both withheld', ids($kept) === [] && count($conflicts) === 1);

[$kept] = $builder->resolveConflicts([
    peerRule('a', 'both', 9445, null, 'peer-a'),
    peerRule('b', 'both', 9445, null, 'peer-a'),
]);
check('same port, same peer: the first survives', ids($kept) === ['a']);

[$kept] = $builder->resolveConflicts([
    lanRule('a', 'both', 9445, null, '10.0.0.10', 'peer-a'),
    lanRule('b', 'both', 9445, null, '10.0.0.10', 'peer-b'),
]);
check('the same LAN IP behind two different clients is a conflict', count($kept) === 0);

[$kept] = $builder->resolveConflicts([
    peerRule('a', 'both', 9445, null, 'peer-a'),
    lanRule('b', 'both', 9445, null, '10.0.0.10', 'peer-a'),
]);
check('a peer target and a LAN target on one port conflict', count($kept) === 0);

[$kept] = $builder->resolveConflicts([
    peerRule('a', 'tcp', 443, null, 'peer-a'),
    peerRule('b', 'udp', 443, null, 'peer-b'),
]);
check('tcp and udp on one port do not conflict', count($kept) === 2);

[$kept] = $builder->resolveConflicts([
    peerRule('a', 'both', 443, null, 'peer-a'),
    peerRule('b', 'udp', 443, null, 'peer-b'),
]);
check('both overlaps udp', count($kept) === 0);

[$kept] = $builder->resolveConflicts([
    peerRule('range', 'both', 9400, 9500, 'peer-a'),
    peerRule('single', 'both', 9450, null, 'peer-b'),
]);
check('a single port inside a range with another target: both withheld', count($kept) === 0);

[$kept] = $builder->resolveConflicts([
    peerRule('range', 'both', 9400, 9500, 'peer-a'),
    peerRule('after', 'both', 9501, null, 'peer-b'),
]);
check('adjacent range and single do not conflict', count($kept) === 2);

[$kept] = $builder->resolveConflicts([
    lanRule('a', 'both', 8080, null, '10.0.0.10', 'peer-a', 80),
    lanRule('b', 'both', 8080, null, '10.0.0.10', 'peer-a', 8080),
]);
check('the same host on different target ports is a conflict', count($kept) === 0);

[$kept] = $builder->resolveConflicts([
    peerRule('a', 'both', 100, null, 'peer-a'),
    lanRule('b', 'both', 200, null, '10.0.0.10'),
    peerRule('c', 'tcp', 300, null, 'peer-c'),
]);
check('unrelated rules all survive', count($kept) === 3);

// --- the wire shape -------------------------------------------------------
// The agent accepts EITHER target_peer OR target_ip + via_peer. Sending both
// keys with one null would be rejected, so nulls are stripped before the push.
$wire = $builder->wire([peerRule('a', 'both', 100, null, 'peer-a')]);
check('a peer rule goes out with target_peer and no target_ip',
    array_key_exists('target_peer', $wire[0]) && !array_key_exists('target_ip', $wire[0]) && !array_key_exists('via_peer', $wire[0]));
check('a single-port rule carries no public_port_end', !array_key_exists('public_port_end', $wire[0]));

$wire = $builder->wire([lanRule('b', 'both', 100, null, '10.0.0.10', 'peer-a')]);
check('a LAN rule goes out with target_ip and via_peer, and no target_peer',
    $wire[0]['target_ip'] === '10.0.0.10' && $wire[0]['via_peer'] === 'peer-a' && !array_key_exists('target_peer', $wire[0]));
check('a rule without a target port omits the key', !array_key_exists('target_port', $wire[0]));

$wire = $builder->wire([lanRule('c', 'udp', 100, 200, '10.0.0.10', 'peer-a')]);
check('a range keeps public_port_end', $wire[0]['public_port_end'] === 200);

// --- alias matching (the real SQL, against real SQLite) -------------------
// Live aliases are free text: "batch" on fifty allocations, "ark", "Palworld".
// Only an exact, trimmed, case-insensitive match may publish a port, or one
// careless keyword opens fifty of them.
if (extension_loaded('pdo_sqlite')) {
    $db = new PDO('sqlite::memory:');
    $db->setAttribute(PDO::ATTR_ERRMODE, PDO::ERRMODE_EXCEPTION);
    $db->exec('CREATE TABLE allocations (id INTEGER PRIMARY KEY, ip_alias TEXT NULL)');

    $aliases = [
        1 => 'proxy',            // exact
        2 => '  Proxy  ',        // padded and capitalised
        3 => 'PUBLIC',           // other keyword, upper case
        4 => 'batch',            // a real-world alias that must never match
        5 => 'proxy server',     // contains the word: must NOT match
        6 => 'myproxy',          // contains the word: must NOT match
        7 => 'project zomboid',  // real alias, contains no keyword
        8 => null,               // no alias at all
        9 => 'play.example.com', // the public address itself
    ];
    $insert = $db->prepare('INSERT INTO allocations (id, ip_alias) VALUES (?, ?)');
    foreach ($aliases as $id => $alias) {
        $insert->execute([$id, $alias]);
    }

    $match = function (string $wanted) use ($db): array {
        $statement = $db->prepare('SELECT id FROM allocations WHERE ' . RuleSetBuilder::ALIAS_MATCH_SQL . ' ORDER BY id');
        $statement->execute([$wanted]);

        return array_map('intval', $statement->fetchAll(PDO::FETCH_COLUMN));
    };

    check('keyword "proxy" matches the exact and padded aliases only', $match('proxy') === [1, 2]);
    check('keyword "public" matches the upper-case alias only', $match('public') === [3]);
    check('a common free-text alias is never published by a keyword',
        !in_array(4, $match('proxy'), true) && !in_array(4, $match('public'), true));
    check('aliases merely containing the word do not match',
        !in_array(5, $match('proxy'), true) && !in_array(6, $match('proxy'), true));
    check('the public address matches itself', $match('play.example.com') === [9]);
    check('a null alias matches nothing', $match('') === []);
} else {
    echo "skip  alias matching (pdo_sqlite missing)\n";
    $failures++;  // a skipped gate is not a passed gate
}

// --- hash -----------------------------------------------------------------
$one = [peerRule('b', 'both', 2, null, 'peer-b'), peerRule('a', 'tcp', 1, null, 'peer-a')];
$two = [peerRule('a', 'tcp', 1, null, 'peer-a'), peerRule('b', 'both', 2, null, 'peer-b')];
check('hash ignores input order', $builder->hash($one) === $builder->hash($two));
check('hash changes when a peer target changes',
    $builder->hash($one) !== $builder->hash([peerRule('b', 'both', 2, null, 'peer-c'), peerRule('a', 'tcp', 1, null, 'peer-a')]));
check('hash tells a peer target from a LAN target on the same ports',
    $builder->hash([peerRule('a', 'both', 1, null, 'peer-a')]) !== $builder->hash([lanRule('a', 'both', 1, null, '10.0.0.10', 'peer-a')]));
check('hash changes when only the routing client changes',
    $builder->hash([lanRule('a', 'both', 1, null, '10.0.0.10', 'peer-a')]) !== $builder->hash([lanRule('a', 'both', 1, null, '10.0.0.10', 'peer-b')]));
check('hash ignores extra keys not in the canonical shape',
    $builder->hash([peerRule('a', 'tcp', 1, null, 'peer-a') + ['extra' => 'x']]) === $builder->hash([peerRule('a', 'tcp', 1, null, 'peer-a')]));
check('empty set hashes deterministically', $builder->hash([]) === $builder->hash([]));

// --- tunnel client health -------------------------------------------------
// A stopped client is the failure the banner used to miss entirely: the VPS
// still answers and the sync still says "ok" while every port for that node is
// a black hole. To watch this gate fail, flip the <= to < in
// PeerHealth::unhealthy() and re-run: "a handshake exactly at the warn window
// is still healthy" goes red; drop the "+ \$drift" and "the age of the snapshot
// itself counts" goes red.
$realNode = ['name' => 'node-a', 'mode' => 'real', 'peer_id' => 'peer-a', 'via_peer_id' => null];
$siteNode = ['name' => 'node-b', 'mode' => 'site', 'peer_id' => null, 'via_peer_id' => 'peer-b'];
$snap = static fn (?int $age): array => ['peer-a' => ['name' => 'client-a', 'handshake_age_s' => $age]];

check('a fresh handshake is healthy',
    PeerHealth::unhealthy($snap(30), [1 => $realNode], 0, 5) === []);
check('a handshake exactly at the warn window is still healthy',
    PeerHealth::unhealthy($snap(5 * 60), [1 => $realNode], 0, 5) === []);
check('one second past the warn window is reported',
    count(PeerHealth::unhealthy($snap(5 * 60 + 1), [1 => $realNode], 0, 5)) === 1);
check('a handshake older than the warn window is reported',
    count(PeerHealth::unhealthy($snap(20 * 60), [1 => $realNode], 0, 5)) === 1);
check('the age of the snapshot itself counts',
    count(PeerHealth::unhealthy($snap(60), [1 => $realNode], 20 * 60, 5)) === 1);
check('minutes reported include the snapshot age',
    PeerHealth::unhealthy($snap(60), [1 => $realNode], 20 * 60, 5)[0]['minutes'] === 21);
check('a client that never handshaked is reported as never connected',
    PeerHealth::unhealthy($snap(null), [1 => $realNode], 0, 5)[0]['minutes'] === null);
check('a peer the VPS no longer knows is reported as unknown',
    PeerHealth::unhealthy(['peer-z' => ['handshake_age_s' => 5]], [1 => $realNode], 0, 5)[0]['known'] === false);
check('a site node is judged on the client it is reached through',
    PeerHealth::unhealthy(['peer-b' => ['handshake_age_s' => 9999]], [2 => $siteNode], 0, 5)[0]['node'] === 'node-b');
check('a site node ignores its own peer_id',
    PeerHealth::peerIdFor($siteNode) === 'peer-b' && PeerHealth::peerIdFor($realNode) === 'peer-a');
check('a node with no client at all is left to the half-set-up message',
    PeerHealth::unhealthy($snap(30), [3 => ['name' => 'node-c', 'mode' => 'real', 'peer_id' => null, 'via_peer_id' => null]], 0, 5) === []);
check('nothing is reported before the first snapshot exists',
    PeerHealth::unhealthy([], [1 => $realNode], 0, 5) === []);
check('never-connected and stopped get different wordings',
    str_contains(PeerHealth::message(['node' => 'n', 'minutes' => null, 'known' => true]), 'never connected')
    && str_contains(PeerHealth::message(['node' => 'n', 'minutes' => 7, 'known' => true]), '7 minute'));

// --- the input helpers the Forwards form and `autoproxy:setup` now share ----
// These rules used to live inside Filament form closures and inside the Setup
// page's addSiteClient(), where only a browser could reach them. They are the
// reason the CLI cannot accept something the VPS then refuses, so they are
// tested here rather than only exercised by hand.
// To watch this gate fail, delete the `$cidrs !== []` branch from
// ForwardRuleInput::targetIpError() and re-run: "a target outside the client's
// ranges is refused" goes red.

check('a private LAN target is accepted',
    ForwardRuleInput::targetIpError('10.0.0.10', []) === null);
check('a public target is refused',
    ForwardRuleInput::targetIpError('203.0.113.10', []) !== null);
check('an empty target is refused',
    ForwardRuleInput::targetIpError(null, []) !== null);
check('a target inside the client\'s ranges is accepted',
    ForwardRuleInput::targetIpError('10.0.0.10', ['10.0.0.0/24']) === null);
check('a target outside the client\'s ranges is refused',
    ForwardRuleInput::targetIpError('192.168.5.10', ['10.0.0.0/24']) !== null);
check('unknown ranges skip the range test rather than guessing',
    ForwardRuleInput::targetIpError('192.168.5.10', []) === null);

check('a port range cannot be remapped',
    ForwardRuleInput::targetPortError('25565', '25570') !== null);
check('a single port can be remapped',
    ForwardRuleInput::targetPortError('25565', null) === null);
check('no target port is always fine',
    ForwardRuleInput::targetPortError(null, '25570') === null);
check('a target port outside 1-65535 is refused',
    ForwardRuleInput::targetPortError('70000', null) !== null);

check('a range end below its start is refused',
    ForwardRuleInput::publicPortEndError('25560', '25565') !== null);
check('a range end equal to its start is accepted',
    ForwardRuleInput::publicPortEndError('25565', '25565') === null);
check('no range end is accepted',
    ForwardRuleInput::publicPortEndError(null, '25565') === null);
check('port 0 and port 65536 are refused',
    ForwardRuleInput::publicPortError('0') !== null && ForwardRuleInput::publicPortError('65536') !== null);
check('a port that is not a number is refused',
    ForwardRuleInput::publicPortError('25565e0') !== null);
check('an unknown protocol is refused',
    ForwardRuleInput::protocolError('sctp') !== null && ForwardRuleInput::protocolError('both') === null);
check('a nameless forward is refused',
    ForwardRuleInput::nameError('   ') !== null && ForwardRuleInput::nameError('panel') === null);
check('an over-long name is refused',
    ForwardRuleInput::nameError(str_repeat('a', ForwardRuleInput::NAME_MAX + 1)) !== null);

check('a client needs a name',
    ClientInput::parse('', '10.0.0.0/24')['error'] !== null);
check('a client needs at least one LAN range',
    ClientInput::parse('office', '  ')['error'] !== null);
check('a LAN range without a prefix is refused',
    ClientInput::parse('office', '10.0.0.0')['error'] !== null);
check('several LAN ranges are split and trimmed',
    ClientInput::parse(' office ', '10.0.0.0/24, 192.168.4.0/24') === ['name' => 'office', 'cidrs' => ['10.0.0.0/24', '192.168.4.0/24'], 'error' => null]);

// --- client versions and updates (0.3.0) ------------------------------------
//
// To watch this gate fail on purpose, drop "&& $updateAvailable" from $canRemote
// in ClientVersion::describe(): the "no button when up to date" check goes red.

$now = strtotime('2026-09-23T12:00:00Z');
$url = 'https://example.invalid/releases/latest/download';
$peer = static fn (array $client): array => ['id' => 'p1', 'name' => 'wings-1', 'client' => $client + [
    'version' => null, 'flavour' => null, 'remote_updates' => null, 'reported_at' => null,
    'desired_version' => null, 'request_id' => null, 'requested_at' => null, 'update' => null,
]];

check('compare: 0.2.7 < 0.3.0', ClientVersion::compare('0.2.7', '0.3.0') === -1);
check('compare: v0.10.0 > 0.9.9', ClientVersion::compare('v0.10.0', '0.9.9') === 1);
check('compare: a development build does not compare', ClientVersion::compare('dev', '0.3.0') === null);

$old = ClientVersion::describe(['id' => 'p1'], '0.3.0', true, $url, $now);
check('an agent older than 0.3.0 (no "client" key) is named as the reason',
    !$old['agent_supports'] && !$old['can_remote'] && str_contains($old['label'], 'VPS agent is older'));

$unreported = ClientVersion::describe($peer([]), '0.3.0', true, $url, $now);
check('a client that never reported gets the installer command without a join code',
    $unreported['show_command'] && str_ends_with($unreported['command'], '/install-client.sh | sudo bash') && !$unreported['can_remote']);

$outdated = ClientVersion::describe($peer(['version' => '0.3.0', 'flavour' => 'systemd', 'remote_updates' => true, 'reported_at' => '2026-09-23T11:59:00Z']), '0.3.1', true, $url, $now);
check('an outdated 0.3.0 client: update available, update command, one-click allowed',
    $outdated['update_available'] && $outdated['command'] === 'sudo autoproxy-client update' && $outdated['can_remote']);

$notAllowed = ClientVersion::describe($peer(['version' => '0.3.0', 'flavour' => 'systemd']), '0.3.1', false, $url, $now);
check('no one-click update while the admin has not allowed remote updates', !$notAllowed['can_remote']);

$current = ClientVersion::describe($peer(['version' => '0.3.1', 'flavour' => 'systemd']), '0.3.1', true, $url, $now);
check('no button and no command when up to date', !$current['can_remote'] && !$current['show_command'] && $current['tone'] === 'success');

$docker = ClientVersion::describe($peer(['version' => '0.3.0', 'flavour' => 'docker']), '0.3.1', true, $url, $now);
check('Docker: pull the image, never a one-click update',
    !$docker['can_remote'] && str_contains($docker['command'], 'docker compose pull'));

$refusing = ClientVersion::describe($peer(['version' => '0.3.0', 'flavour' => 'systemd', 'remote_updates' => false]), '0.3.1', true, $url, $now);
check('a node that switched remote updates off says so and gets no button',
    !$refusing['can_remote'] && str_contains((string) $refusing['remote_note'], 'remote-updates on'));

$stale = ClientVersion::describe($peer(['version' => '0.3.0', 'reported_at' => '2026-09-23T10:00:00Z']), '0.3.0', true, $url, $now);
check('an old report shows its age', str_contains($stale['label'], 'last reported 2 hours ago'));

$requested = ClientVersion::progress(['desired_version' => '0.3.1', 'request_id' => 'r2', 'requested_at' => '2026-09-23T11:59:00Z',
    'update' => ['state' => 'failed', 'version' => '0.3.1', 'error' => 'old failure', 'request_id' => 'r1', 'at' => '2026-09-23T11:00:00Z']], $now);
check('progress: a new request is "requested", not the previous request\'s failure',
    $requested !== null && str_starts_with($requested['text'], 'Update to 0.3.1 requested') && $requested['tone'] === 'warning');

$failed = ClientVersion::progress(['desired_version' => '0.3.1', 'request_id' => 'r2',
    'update' => ['state' => 'failed', 'version' => '0.3.1', 'error' => 'CHECKSUM MISMATCH', 'request_id' => 'r2', 'at' => '2026-09-23T11:58:00Z']], $now);
check('progress: a failure for this request shows the client\'s own reason',
    $failed !== null && $failed['tone'] === 'danger' && str_contains($failed['text'], 'CHECKSUM MISMATCH'));

$updating = ClientVersion::progress(['desired_version' => '0.3.1', 'request_id' => 'r2',
    'update' => ['state' => 'updating', 'version' => '0.3.1', 'error' => '', 'request_id' => 'r2', 'at' => '2026-09-23T11:59:30Z']], $now);
check('progress: updating', $updating !== null && str_starts_with($updating['text'], 'Updating to 0.3.1'));

$done = ClientVersion::progress(['version' => '0.3.1', 'update' => ['state' => 'updated', 'version' => '0.3.1', 'error' => '', 'request_id' => 'r2', 'at' => '2026-09-23T11:59:50Z']], $now);
check('progress: updated', $done !== null && $done['tone'] === 'success' && str_contains($done['text'], 'Updated to 0.3.1'));

check('progress: nothing to say', ClientVersion::progress([], $now) === null);

check('progress: "updated" is dropped once the client runs another version (moved by hand since)',
    ClientVersion::progress(['version' => '0.3.0', 'update' => ['state' => 'updated', 'version' => '0.3.4', 'error' => '', 'request_id' => 'r3', 'at' => '2026-09-23T11:00:00Z']], $now) === null);

echo "\n$checks checks, $failures failure(s)\n";
exit($failures === 0 ? 0 : 1);
}
