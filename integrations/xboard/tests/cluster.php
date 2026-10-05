<?php

// Exercise the shipped controllers against deterministic framework boundary fakes.
// Authentication middleware, Laravel validation, Redis locking and queue delivery
// remain Xboard's responsibilities and are not simulated as integration guarantees.
namespace Illuminate\Http {
    class JsonResponse {
        public function __construct(public array $data, public int $status = 200) {}
        public function header($key, $value): self { return $this; }
    }
    class Request {
        public object $attributes;
        public function __construct(private array $data, $node = null) {
            $this->attributes = new class($node) {
                public function __construct(private $node) {}
                public function get($key) { return $key === 'node_info' ? $this->node : null; }
            };
        }
        public function validate($rules): array { return $this->data; }
        public function input($key) { return $this->data[$key] ?? null; }
        public function getSchemeAndHttpHost(): string { return 'https://panel.example.com'; }
    }
}
namespace App\Http\Controllers {
    class PluginController {
        public bool $disabled = false;
        public function beforePluginAction() { return $this->disabled ? ['message' => 'disabled'] : null; }
    }
}
namespace App\Models {
    class Server {
        public int $id = 7;
        public bool $enabled = true;
        public string $host = 'pool.example.com';
        public string $type = 'vless';
        public int $machine_id = 0;
        public array $protocol_settings = [];
        public static function findOrFail($id) { return $GLOBALS['testNode']; }
        public static function normalizeType($type) { return $type; }
        public static function query() {
            return new class {
                public function whereIn($field, $ids) { return $this; }
                public function get() { return new \TestCollection([$GLOBALS['testNode']]); }
            };
        }
    }
}
namespace App\Utils {
    class CacheKey { public static function get($name, $id) { return "$name:$id"; } }
}
namespace Illuminate\Support\Facades {
    class Cache {
        public static function store($name) { return $GLOBALS['testStore']; }
        public static function put($key, $data, $ttl): void { self::store('redis')->put($key, $data, $ttl); }
    }
    class Route {
        public static array $routes = [];
        public static string $guard = '';
        public static function middleware($guard) { self::$guard = $guard; return new self; }
        public static function get($path, $handler) { self::$routes[$path] = ['GET', self::$guard]; }
        public static function post($path, $handler) { self::$routes[$path] = ['POST', self::$guard]; }
        public function group($callback): void { $callback(); }
    }
}
namespace App\Services {
    class ServerService {
        public static array $traffic = [];
        public static array $status = [];
        public static function processTraffic($node, $data): void { self::$traffic[] = $data; }
        public static function processStatus($node, $data): void { self::$status = $data; }
        public static function touchNode($node): void {}
        public static function getAvailableUsers($node) { return new \TestCollection([['id'=>1], ['id'=>2], ['id'=>3]]); }
    }
    class DeviceStateService {
        public array $devices = [];
        public function setDevices($uid, $node, $ips): void { $this->devices[$node][$uid] = $ips; }
        public function getUsersDevices($uids): array {
            $out = [];
            foreach ($uids as $uid) {
                $ips = [];
                foreach ($this->devices as $node) { $ips = array_merge($ips, $node[$uid] ?? []); }
                if ($ips) { $out[$uid] = array_values(array_unique($ips)); }
            }
            return $out;
        }
        public function getAliveList($users): array {
            return array_map('count', $this->getUsersDevices($users->pluck('id')->all()));
        }
    }
}
namespace {
    use App\Services\DeviceStateService;
    use App\Services\ServerService;
    use Illuminate\Http\Request;
    use Illuminate\Support\Facades\Route;
    use Plugin\AriNode\Controllers\ClusterController;
    use Plugin\AriNode\Controllers\ProvisionController;
    use Plugin\AriNode\Services\ClusterState;

    final class TestCollection {
        public function __construct(private array $rows) {}
        public function where(...$args): self { return $this; }
        public function keyBy($key): self { return $this; }
        public function count(): int { return count($this->rows); }
        public function get($id) { return $this->rows[0]; }
        public function pluck($key): self { return new self(array_map(fn($row) => (array) $row, $this->rows)); }
        public function all(): array { return array_column($this->rows, 'id'); }
    }
    final class TestStore {
        public array $data = [];
        public function get($key, $default = null) { return $this->data[$key] ?? $default; }
        public function has($key): bool { return array_key_exists($key, $this->data); }
        public function put($key, $data, $ttl): void { $this->data[$key] = $data; }
        public function lock($key, $ttl) {
            return new class { public function block($wait, $callback) { return $callback(); } };
        }
    }
    function response() { return new class { public function json($data, $code = 200) { return new \Illuminate\Http\JsonResponse($data, $code); } }; }
    function abort_if(bool $value, $code, $message): void { if ($value) { throw new \RuntimeException($message, $code); } }
    function abort_unless(bool $value, $code, $message): void { abort_if(!$value, $code, $message); }
    function admin_setting($key, $default = null) { return ['server_token'=>'test-token', 'app_url'=>'https://panel.example.com'][$key] ?? $default; }
    function app($class) { return $GLOBALS['testDevices']; }
    function check($value, $message): void { if (!$value) { throw new \RuntimeException($message); } }
    function rejects($callback, $code): void {
        try { $callback(); } catch (\RuntimeException $error) { check($error->getCode() === $code, $error->getMessage()); return; }
        throw new \RuntimeException('Expected rejection');
    }

    require __DIR__ . '/../AriNode/Services/ClusterState.php';
    require __DIR__ . '/../AriNode/Controllers/ClusterController.php';
    require __DIR__ . '/../AriNode/Controllers/ProvisionController.php';
    require __DIR__ . '/../AriNode/routes/api.php';
    check(Route::$routes['/api/v1/arinode/cluster/report'] === ['POST', 'server.v2'], 'Reports need native server authentication');
    check(Route::$routes['/api/v1/arinode/cluster/members'] === ['GET', 'admin'], 'Member inventory must require admin');
    check(Route::$routes['/api/v1/arinode/provision'] === ['POST', 'admin'], 'Provisioning must require admin');

    $testNode = new \App\Models\Server;
    $testStore = new TestStore;
    $testDevices = new DeviceStateService;
    $controller = new ClusterController;
    $status = fn($cpu) => ['cpu'=>$cpu, 'mem'=>['total'=>1000, 'used'=>200], 'swap'=>['total'=>0,'used'=>0], 'disk'=>['total'=>100,'used'=>10]];
    $report = fn($member, $id, $alive, $cpu) => ['member_id'=>$member, 'report_id'=>str_repeat($id, 32), 'domain'=>'pool.example.com',
        'traffic'=>[1=>[10,20]], 'alive'=>$alive, 'status'=>$status($cpu)];
    $a = $report('hk-01', 'a', [1=>['192.0.2.1'], 2=>['192.0.2.2']], 20);
    $b = $report('hk-02', 'b', [1=>['192.0.2.1'], 3=>['2001:db8::3']], 60);
    $controller->report(new Request($a, $testNode));
    $controller->report(new Request($b, $testNode));
    $alive = $controller->alivelist(new Request([], $testNode))->data;
    check((array) $alive['alive'] === [1=>1, 2=>1, 3=>1], 'Members overwrite each other or duplicate a shared client IP');
    check($testStore->get('SERVER_VLESS_ONLINE_USER:7') === 3, 'Panel online users must be the group union');
    check(ServerService::$status['cpu'] === 40.0 && ServerService::$status['mem']['total'] === 2000, 'Incorrect group load aggregation');
    $retry = $controller->report(new Request($a, $testNode))->data;
    check($retry['duplicate'] && count(ServerService::$traffic) === 2, 'A network retry charged the same snapshot twice');
    check((array) $controller->alivelist(new Request([], $testNode))->data['alive'] === [1=>1,2=>1,3=>1], 'Retry rolled back another member');
    $empty = $report('hk-01', 'c', [], 0);
    $empty['traffic'] = [];
    $controller->report(new Request($empty, $testNode));
    check((array) $controller->alivelist(new Request([], $testNode))->data['alive'] === [1=>1,3=>1], 'Empty snapshot failed to remove old users');

    $key = 'arinode:cluster:node:7';
    $testStore->data[$key]['members']['hk-02']['seen_at'] = time() - 181;
    $testDevices->setDevices(1, 8, ['198.51.100.1']); // Preserve a different node's devices.
    $after = $controller->alivelist(new Request([], $testNode))->data;
    check((array) $after['alive'] === [1=>1], 'Expired members persist or another node was cleared');
    check($testStore->get('SERVER_VLESS_ONLINE_USER:7') === 0, 'Expired node users remain in panel metrics');
    rejects(fn() => $controller->info(new Request(['domain'=>'other.example.com'], $testNode)), 422);
    rejects(fn() => $controller->info(new Request(['domain'=>'pool.example.com'])), 422);
    $controller->disabled = true;
    rejects(fn() => $controller->info(new Request(['domain'=>'pool.example.com'], $testNode)), 403);
    $controller->disabled = false;
    $bad = $report('hk-01', 'd', [-1=>['192.0.2.1']], 20);
    rejects(fn() => $controller->report(new Request($bad, $testNode)), 422);

    $provision = new ProvisionController;
    $generated = $provision->provision(new Request(['node_ids'=>[7], 'cluster_domain'=>'POOL.example.com.', 'cluster_members'=>['hk-01','hk-02']]))->data;
    foreach ($generated['replicas'] as $i => $replica) {
        $binding = $replica['config']['Nodes'][0];
        check($binding['NodeID'] === 7 && $binding['Cluster'] === ['Domain'=>'pool.example.com','MemberID'=>['hk-01','hk-02'][$i]], 'Provisioned replica config is incorrect');
    }
    $plain = $provision->provision(new Request(['node_ids'=>[7]]))->data;
    check(!isset($plain['Nodes'][0]['Cluster']), 'Ordinary provisioning unexpectedly enabled cluster mode');
    $mismatch = $provision->provision(new Request(['node_ids'=>[7], 'cluster_domain'=>'other.example.com', 'cluster_members'=>['a','b']]));
    check($mismatch->status === 422, 'Mismatched provision domain was accepted');

    $boundary = ClusterState::record([], 'a', [1=>['192.0.2.1']], null, 100, 180);
    check(count(ClusterState::prune($boundary, 279, 180)['members']) === 1, 'Member expired early');
    check(ClusterState::prune($boundary, 280, 180)['members'] === [], 'Member did not expire at TTL');
    echo "Xboard cluster controller regression tests passed\n";
}
