<?php

namespace Plugin\AriNode\Controllers;

use App\Http\Controllers\PluginController;
use App\Models\Server;
use App\Services\DeviceStateService;
use App\Services\ServerService;
use App\Utils\CacheKey;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Cache;
use Plugin\AriNode\Services\ClusterState;

class ClusterController extends PluginController
{
    private function node(Request $request): Server
    {
        abort_if((bool) $this->beforePluginAction(), 403, 'AriNode plugin is disabled');
        $node = $request->attributes->get('node_info');
        abort_unless($node && $node->enabled, 422, 'Enabled node is required');
        return $node;
    }

    private function domain(Server $node, string $domain): void
    {
        abort_unless(strtolower(rtrim($node->host, '.')) === strtolower(rtrim($domain, '.')),
            422, 'Cluster domain must match the Xboard node host');
    }

    private function ttl(): int
    {
        return max(180, (int) admin_setting('server_push_interval', 60) * 3);
    }

    public function info(Request $request)
    {
        $node = $this->node($request);
        $request->validate(['domain' => 'required|string|max:253']);
        $this->domain($node, $request->input('domain'));
        // Fail before starting cluster nodes if the shared Redis store is unavailable.
        Cache::store('redis')->get('arinode:cluster:health');
        return response()->json(['version' => 1, 'node_id' => $node->id,
            'domain' => $node->host, 'member_ttl' => $this->ttl()])->header('Cache-Control', 'no-store');
    }

    public function report(Request $request)
    {
        $node = $this->node($request);
        $data = $request->validate([
            'member_id' => ['required', 'string', 'regex:/^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/'],
            'report_id' => ['required', 'string', 'regex:/^[a-f0-9]{32}$/'],
            'domain' => 'required|string|max:253',
            'traffic' => 'present|array|max:100000', 'traffic.*' => 'array|size:2',
            'traffic.*.*' => 'integer|min:0',
            'alive' => 'present|array|max:100000', 'alive.*' => 'array|max:1024',
            'alive.*.*' => 'string|ip',
            'status' => 'sometimes|array', 'status.cpu' => 'required_with:status|numeric|min:0|max:100',
            'status.mem.total' => 'required_with:status|integer|min:0', 'status.mem.used' => 'required_with:status|integer|min:0',
            'status.swap.total' => 'required_with:status|integer|min:0', 'status.swap.used' => 'required_with:status|integer|min:0',
            'status.disk.total' => 'required_with:status|integer|min:0', 'status.disk.used' => 'required_with:status|integer|min:0',
        ]);
        $this->domain($node, $data['domain']);
        foreach ([$data['traffic'], $data['alive']] as $map) {
            foreach (array_keys($map) as $uid) {
                abort_unless(ctype_digit((string) $uid) && (int) $uid > 0, 422, 'Invalid user ID');
            }
        }
        $store = Cache::store('redis');
        $key = 'arinode:cluster:node:' . $node->id;
        return $store->lock($key . ':lock', 60)->block(10, function () use ($store, $key, $node, $data) {
            $receipt = $key . ':report:' . $data['member_id'] . ':' . $data['report_id'];
            if ($store->has($receipt)) {
                return response()->json(['accepted' => true, 'report_id' => $data['report_id'], 'duplicate' => true]);
            }
            $previous = $store->get($key, ['members' => []]);
            $next = ClusterState::record($previous, $data['member_id'], $data['alive'],
                $data['status'] ?? null, time(), $this->ttl());
            // Traffic remains additive through Xboard's native queue/accounting path.
            // Receipts suppress ordinary network retries for 24 hours. Queue delivery
            // and Redis receipts are not a cross-store transaction.
            if ($data['traffic']) {
                ServerService::processTraffic($node, $data['traffic']);
            }
            $this->publish($node, $previous, $next);
            $store->put($key, $next, max(86400, $this->ttl() * 2));
            $store->put($receipt, true, 86400);
            return response()->json(['accepted' => true, 'report_id' => $data['report_id']]);
        });
    }

    private function publish(Server $node, array $previous, array $next): void
    {
        $before = ClusterState::aggregate($previous);
        $after = ClusterState::aggregate($next);
        $devices = app(DeviceStateService::class);
        foreach (array_unique(array_merge(array_keys($before['alive']), array_keys($after['alive']))) as $uid) {
            $devices->setDevices((int) $uid, (int) $node->id, $after['alive'][$uid] ?? []);
        }
        ServerService::processStatus($node, $after['status']);
        // Native processTraffic sets this to the reporting member's user count.
        // Replace it with the union after publishing the complete group snapshot.
        $type = strtoupper($node->type);
        Cache::put(CacheKey::get("SERVER_{$type}_ONLINE_USER", $node->id), count($after['alive']), 3600);
        ServerService::touchNode($node);
    }

    public function alivelist(Request $request)
    {
        $node = $this->node($request);
        $store = Cache::store('redis');
        $key = 'arinode:cluster:node:' . $node->id;
        return $store->lock($key . ':lock', 60)->block(10, function () use ($store, $key, $node) {
            $previous = $store->get($key, ['members' => []]);
            $next = ClusterState::prune($previous, time(), $this->ttl());
            if ($next !== $previous) {
                $this->publish($node, $previous, $next);
                $store->put($key, $next, max(86400, $this->ttl() * 2));
            }
            $users = ServerService::getAvailableUsers($node)->where('device_limit', '>', 0);
            $devices = app(DeviceStateService::class);
            return response()->json(['alive' => (object) $devices->getAliveList($users),
                'ips' => (object) $devices->getUsersDevices($users->pluck('id')->all())]);
        });
    }

    public function members(Request $request)
    {
        abort_if((bool) $this->beforePluginAction(), 403, 'AriNode plugin is disabled');
        $params = $request->validate(['node_id' => 'required|integer|min:1']);
        $node = Server::findOrFail($params['node_id']);
        $state = Cache::store('redis')->get('arinode:cluster:node:' . $node->id, ['members' => []]);
        $state = ClusterState::prune($state, time(), $this->ttl());
        return response()->json(['node_id' => $node->id, 'domain' => $node->host,
            'member_ttl' => $this->ttl(), 'members' => ClusterState::summary($state)])
            ->header('Cache-Control', 'no-store');
    }
}
