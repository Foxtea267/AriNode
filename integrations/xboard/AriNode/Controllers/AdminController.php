<?php

namespace Plugin\AriNode\Controllers;

use App\Http\Controllers\PluginController;
use App\Models\Server;
use App\Models\ServerMachine;
use App\Services\NodeSyncService;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\DB;
use Illuminate\Validation\ValidationException;
use InvalidArgumentException;
use Plugin\AriNode\Services\Deployment;

class AdminController extends PluginController
{
    private function enabled(): void
    {
        abort_if((bool) $this->beforePluginAction(), 403, 'AriNode plugin is disabled');
    }

    private function panelUrl(Request $request): string
    {
        return rtrim((string) (admin_setting('app_url') ?: $request->getSchemeAndHttpHost()), '/');
    }

    public function page()
    {
        $this->enabled();
        // The HTML shell contains no credentials. Every data/mutation route uses
        // XBoard's admin middleware; browser navigation cannot send a Bearer header.
        return response()->view('AriNode::admin')->header('Cache-Control', 'no-store')
            ->header('Referrer-Policy', 'no-referrer')
            ->header('X-Content-Type-Options', 'nosniff')
            ->header('Content-Security-Policy', "default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'self'; frame-ancestors 'self'");
    }

    public function nodes(Request $request): JsonResponse
    {
        $this->enabled();
        $params = $request->validate(['current' => 'sometimes|integer|min:1', 'pageSize' => 'sometimes|integer|min:1|max:100']);
        $query = Server::query()->orderBy('id');
        $total = (clone $query)->count();
        $size = (int) ($params['pageSize'] ?? 100);
        $nodes = $query->skip(((int) ($params['current'] ?? 1) - 1) * $size)->take($size)->get()->map(function (Server $node) {
            $type = Deployment::nodeType($node);
            $supported = true;
            try { Deployment::validateProtocol($type); } catch (InvalidArgumentException $e) { $supported = false; }
            return ['id' => $node->id, 'name' => $node->name, 'type' => $type, 'host' => $node->host,
                'enabled' => (bool) $node->enabled, 'machine_id' => $node->machine_id, 'supported' => $supported];
        })->values();
        return response()->json(['data' => $nodes, 'total' => $total])->header('Cache-Control', 'no-store');
    }

    public function servers(Request $request): JsonResponse
    {
        $this->enabled();
        $params = $request->validate(['current' => 'sometimes|integer|min:1', 'pageSize' => 'sometimes|integer|min:1|max:100', 'search' => 'sometimes|nullable|string|max:255']);
        $query = ServerMachine::query()->with('servers')->orderBy('id');
        if (!empty($params['search'])) {
            $query->where('name', 'like', '%' . $params['search'] . '%');
        }
        $total = (clone $query)->count();
        $page = (int) ($params['current'] ?? 1);
        $size = (int) ($params['pageSize'] ?? 20);
        $rows = $query->skip(($page - 1) * $size)->take($size)->get()->map(function (ServerMachine $machine) use ($request) {
            $ids = $machine->servers->pluck('id')->all();
            $command = '';
            $error = '';
            try {
                if (!$machine->is_active) { throw new InvalidArgumentException('Server is disabled'); }
                $command = Deployment::command($this->panelUrl($request), $machine->token, $machine->id, $machine->servers->where('enabled', true));
            } catch (InvalidArgumentException $e) { $error = $e->getMessage(); }
            return ['id' => $machine->id, 'name' => $machine->name, 'notes' => $machine->notes,
                'is_active' => (bool) $machine->is_active, 'last_seen_at' => $machine->last_seen_at,
                'node_ids' => $ids, 'node_ids_text' => implode(',', $ids), 'nodes_count' => count($ids),
                'install_command' => $command, 'deploy_error' => $error];
        })->values();
        return response()->json(['data' => $rows, 'total' => $total])->header('Cache-Control', 'no-store');
    }

    public function save(Request $request): JsonResponse
    {
        $this->enabled();
        // Native schema CRUD uses a text field; the built-in page sends an array.
        if (!$request->has('node_ids')) {
            $request->validate(['node_ids_text' => 'sometimes|nullable|string|max:2000']);
            $raw = trim((string) $request->input('node_ids_text', ''));
            $request->merge(['node_ids' => $raw === '' ? [] : preg_split('/[\s,，]+/u', $raw)]);
        }
        $params = $request->validate([
            'id' => 'sometimes|nullable|integer|min:1', 'name' => 'required|string|max:255',
            'notes' => 'sometimes|nullable|string|max:4000', 'is_active' => 'sometimes|boolean',
            'node_ids' => 'required|array|min:1|max:100', 'node_ids.*' => 'required|integer|min:1|distinct',
        ]);
        $id = DB::transaction(function () use ($params) {
            $machine = !empty($params['id']) ? ServerMachine::query()->lockForUpdate()->findOrFail($params['id']) : new ServerMachine();
            $ids = array_map('intval', $params['node_ids']);
            $nodes = Server::query()->whereIn('id', $ids)->orderBy('id')->lockForUpdate()->get();
            if ($nodes->count() !== count($ids)) {
                throw ValidationException::withMessages(['node_ids' => 'One or more nodes do not exist']);
            }
            foreach ($nodes as $node) {
                if ($node->machine_id && (int) $node->machine_id !== (int) $machine->id) {
                    throw ValidationException::withMessages(['node_ids' => "Node {$node->id} already belongs to another server; unbind it there first"]);
                }
                try { Deployment::validateProtocol(Deployment::nodeType($node)); }
                catch (InvalidArgumentException $e) { throw ValidationException::withMessages(['node_ids' => "Node {$node->id}: {$e->getMessage()}"]); }
            }
            $machine->name = trim($params['name']);
            if ($machine->name === '') { throw ValidationException::withMessages(['name' => 'Server name is required']); }
            $machine->notes = $params['notes'] ?? null;
            $machine->is_active = $params['is_active'] ?? ($machine->exists ? $machine->is_active : true);
            if (!$machine->exists) { $machine->token = ServerMachine::generateToken(); }
            $machine->save();
            // Query updates intentionally avoid per-node WS notifications before
            // transaction commit. Publish the final set once, after committing.
            Server::query()->where('machine_id', $machine->id)->whereNotIn('id', $ids)->update(['machine_id' => null]);
            Server::query()->whereIn('id', $ids)->update(['machine_id' => $machine->id]);
            return (int) $machine->id;
        });
        $warning = null;
        try { NodeSyncService::notifyMachineNodesChanged($id); }
        catch (\Throwable $e) {
            // AriNode polls this binding every 30 seconds. Do not report an
            // already-committed save as failed merely because WS/Redis is down.
            $warning = 'Saved. WebSocket notification is unavailable; AriNode will discover bindings on its next poll.';
        }
        return response()->json(['data' => ['id' => $id], 'message' => 'Server saved', 'warning' => $warning])->header('Cache-Control', 'no-store');
    }

    public function command(Request $request): JsonResponse
    {
        $this->enabled();
        $params = $request->validate(['id' => 'required|integer|min:1', 'core' => 'sometimes|in:sing,xray', 'language' => 'sometimes|in:zh,en']);
        $machine = ServerMachine::query()->with('servers')->findOrFail($params['id']);
        abort_unless($machine->is_active, 422, 'Server is disabled');
        try {
            $command = Deployment::command($this->panelUrl($request), $machine->token, $machine->id,
                $machine->servers->where('enabled', true), $params['core'] ?? 'sing', $params['language'] ?? 'zh');
        } catch (InvalidArgumentException $e) {
            throw ValidationException::withMessages(['command' => $e->getMessage()]);
        }
        return response()->json(['data' => ['command' => $command]])->header('Cache-Control', 'no-store');
    }
}
