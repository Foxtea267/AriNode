<?php

namespace Plugin\AriNode\Controllers;

use App\Http\Controllers\PluginController;
use App\Models\ServerMachine;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Cache;
use Plugin\AriNode\Services\Deployment;

class BackendUpdateController extends PluginController
{
    private const ONLINE_SECONDS = 120;
    private const QUEUE_SECONDS = 1800;
    private const RUN_SECONDS = 900;
    private const CACHE_SECONDS = 86400;

    public function status(Request $request): JsonResponse
    {
        return $this->admin($request, false);
    }

    public function queue(Request $request): JsonResponse
    {
        return $this->admin($request, true);
    }

    public function machines(Request $request): JsonResponse
    {
        $this->enabled();
        $params = $request->validate([
            'current' => 'sometimes|integer|min:1',
            'pageSize' => 'sometimes|integer|min:1|max:100',
        ]);
        // Enumerate all pages without minting deployment credentials or loading
        // node bindings. Search on the normal table does not restrict "update all".
        $query = ServerMachine::query()->orderBy('id');
        $total = (clone $query)->count();
        $size = (int) ($params['pageSize'] ?? 100);
        $data = $query->skip(((int) ($params['current'] ?? 1) - 1) * $size)->take($size)
            ->get(['id', 'name', 'is_active'])->map(static fn(ServerMachine $machine): array => [
            'id' => (int) $machine->id,
            'name' => (string) $machine->name,
            'is_active' => (bool) $machine->is_active,
        ])->values();
        return response()->json(['data' => $data, 'total' => $total])
            ->header('Cache-Control', 'no-store, private');
    }

    private function admin(Request $request, bool $queue): JsonResponse
    {
        $this->enabled();
        $params = $request->validate(['id' => 'required|integer|min:1']);
        $machine = ServerMachine::query()->findOrFail($params['id']);
        return $this->withMachine($machine, function (array $state, int $now) use ($machine, $queue) {
            $agents = $state['agents'] ?? [];
            $agent = count($agents) === 1 ? array_key_first($agents) : null;
            $ready = $machine->is_active && $agent !== null;
            $job = $state['job'] ?? null;
            if ($queue && !in_array($job['state'] ?? '', ['pending', 'running'], true)) {
                abort_if(count($agents) > 1, 409, '同一机器记录被多台服务器使用，请给各 VPS 使用独立机器记录。');
                abort_unless($ready, 409, '节点未上报更新能力，请先在 VPS 执行 sudo anctl upgrade，或等待节点重新上线。');
                $job = ['id' => bin2hex(random_bytes(16)), 'state' => 'pending',
                    'agent' => $agent, 'created_at' => $now, 'deadline' => $now + self::QUEUE_SECONDS];
                $state['job'] = $job;
            }
            $publicJob = $job;
            if ($publicJob !== null) {
                unset($publicJob['agent']);
            }
            return [$state, ['ready' => (bool) $ready, 'active' => (bool) $machine->is_active, 'conflict' => count($agents) > 1,
                'version' => $agents[$agent]['version'] ?? null, 'job' => $job ? $publicJob : null]];
        });
    }

    public function agent(Request $request): JsonResponse
    {
        $this->enabled();
        // server.v2 accepts both machine and global server tokens. Require the
        // authenticated machine attribute even when it currently has no nodes.
        $machine = $request->attributes->get('machine_info');
        abort_unless($machine instanceof ServerMachine && $machine->is_active, 422, 'Machine authentication is required');
        $params = $request->validate([
            'action' => 'required|in:poll,claim,finish',
            'version' => ['required', 'string', 'regex:/\A[0-9]{8}-[a-f0-9]{8,12}\z/'],
            'agent' => ['required', 'string', 'regex:/\A[a-f0-9]{32}\z/'],
            'job' => ['required_unless:action,poll', 'string', 'regex:/\A[a-f0-9]{32}\z/'],
            'result' => 'required_if:action,finish|in:succeeded,current,failed',
        ]);
        return $this->withMachine($machine, function (array $state, int $now) use ($params) {
            $job = $state['job'] ?? null;
            $agent = $params['agent'];
            if ($params['action'] === 'poll') {
                $state['agents'][$agent] = ['seen_at' => $now, 'version' => $params['version']];
                $state['agents'] = array_slice($state['agents'], -16, 16, true);
                $pending = ($job['state'] ?? '') === 'pending' && $job['agent'] === $agent && count($state['agents']) === 1 ? $job['id'] : null;
                return [$state, ['job' => $pending]];
            }
            abort_unless($job && hash_equals($job['id'], $params['job']) && $job['agent'] === $agent, 409, 'Update job is no longer available');
            if ($params['action'] === 'claim') {
                abort_unless($job['state'] === 'pending' && count($state['agents']) === 1 && isset($state['agents'][$agent]),
                    409, 'Update job is already claimed, expired or shared by multiple servers');
                $job['state'] = 'running';
                $job['started_at'] = $now;
                $job['deadline'] = $now + self::RUN_SECONDS;
            } else {
                $result = $params['result'];
                if ($job['state'] === $result) {
                    return [$state, ['accepted' => true]];
                }
                abort_unless($job['state'] === 'running', 409, 'Update job is no longer running');
                $job['state'] = $result;
                $job['finished_at'] = $now;
                $job['version'] = $params['version'];
                $state['agents'][$agent] = ['seen_at' => $now, 'version' => $params['version']];
            }
            $state['job'] = $job;
            return [$state, ['accepted' => true]];
        });
    }

    private function enabled(): void
    {
        abort_if((bool) $this->beforePluginAction(), 403, 'AriNode plugin is disabled');
    }

    private function withMachine(ServerMachine $machine, callable $update): JsonResponse
    {
        $key = 'arinode:backend-update:' . $machine->id . ':' . Deployment::credentialFingerprint($machine->token);
        $cache = Cache::store('redis');
        return $cache->lock($key . ':lock', 10)->block(3, function () use ($cache, $key, $update) {
            $previous = $cache->get($key, []);
            $state = $previous;
            $now = time();
            $state['agents'] = array_filter($state['agents'] ?? [], fn(array $agent) => $agent['seen_at'] > $now - self::ONLINE_SECONDS);
            if (in_array($state['job']['state'] ?? '', ['pending', 'running'], true) && $state['job']['deadline'] <= $now) {
                $state['job']['state'] = 'expired';
                $state['job']['finished_at'] = $now;
            }
            [$state, $data] = $update($state, $now);
            if ($state !== $previous) {
                $cache->put($key, $state, self::CACHE_SECONDS);
            }
            return response()->json(['data' => $data])->header('Cache-Control', 'no-store, private');
        });
    }
}
