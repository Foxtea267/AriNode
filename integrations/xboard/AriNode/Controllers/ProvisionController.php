<?php

namespace Plugin\AriNode\Controllers;

use App\Http\Controllers\PluginController;
use App\Models\Server;
use App\Models\ServerMachine;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;

class ProvisionController extends PluginController
{
    public function provision(Request $request): JsonResponse
    {
        if ($this->beforePluginAction()) {
            return response()->json(['message' => 'AriNode plugin is disabled'], 403);
        }

        $params = $request->validate([
            'node_ids' => 'required|array|min:1|max:100',
            'node_ids.*' => 'required|integer|min:1|distinct',
            'core' => 'sometimes|in:sing,xray',
            'machine_id' => 'sometimes|integer|min:1',
            'cluster_domain' => 'required_with:cluster_members|string|max:253',
            'cluster_members' => 'sometimes|array|min:2|max:128',
            'cluster_members.*' => ['required', 'string', 'distinct', 'regex:/^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/'],
        ]);

        $ids = $params['node_ids'];
        $servers = Server::query()->whereIn('id', $ids)->get()->keyBy('id');
        if ($servers->count() !== count($ids)) {
            return response()->json(['message' => 'One or more nodes do not exist'], 422);
        }

        $machine = null;
        if (!empty($params['machine_id'])) {
            $machine = ServerMachine::find($params['machine_id']);
            if (!$machine || !$machine->is_active) {
                return response()->json(['message' => 'Machine does not exist or is disabled'], 422);
            }
        }

        $serverToken = $machine ? $machine->token : (string) admin_setting('server_token');
        if ($serverToken === '') {
            return response()->json(['message' => 'Xboard server_token is not configured'], 503);
        }

        $panelUrl = rtrim((string) (admin_setting('app_url') ?: $request->getSchemeAndHttpHost()), '/');
        $core = $params['core'] ?? 'sing';
        $nodes = [];
        $clusterDomain = strtolower(rtrim(trim($params['cluster_domain'] ?? ''), '.'));
        foreach ($ids as $id) {
            $server = $servers->get($id);
            $type = Server::normalizeType($server->type);
            if ($type === 'hysteria' && (int) ($server->protocol_settings['version'] ?? 0) === 2) {
                $type = 'hysteria2';
            }
            if (!in_array($type, ['vmess', 'vless', 'trojan', 'shadowsocks', 'hysteria', 'hysteria2', 'tuic', 'anytls', 'mieru'], true)) {
                return response()->json(['message' => "Node {$id} uses unsupported protocol {$type}"], 422);
            }
            if ($core === 'xray' && !in_array($type, ['vmess', 'vless', 'trojan', 'shadowsocks'], true)) {
                return response()->json(['message' => "Protocol {$type} requires the sing core"], 422);
            }
            if (!$server->enabled) {
                return response()->json(['message' => "Node {$id} is disabled"], 422);
            }
            if (!empty($params['cluster_members']) && strtolower(rtrim($server->host, '.')) !== $clusterDomain) {
                return response()->json(['message' => "Node {$id} host must match cluster_domain"], 422);
            }
            if ($machine && (int) $server->machine_id !== (int) $machine->id) {
                return response()->json(['message' => "Node {$id} is not bound to machine {$machine->id}"], 422);
            }
            $nodes[] = [
                'Core' => $core,
                'ApiHost' => $panelUrl,
                'ApiKey' => $serverToken,
                'NodeID' => (int) $id,
                'MachineID' => $machine ? (int) $machine->id : 0,
                'NodeType' => $type,
                'Timeout' => 30,
            ];
        }

        $config = [
            'Log' => ['Level' => 'info'],
            'Cores' => [['Type' => $core]],
            'Nodes' => $nodes,
        ];
        if (!empty($params['cluster_members'])) {
            $replicas = [];
            foreach ($params['cluster_members'] as $member) {
                $replica = $config;
                foreach ($replica['Nodes'] as &$binding) {
                    $binding['Cluster'] = ['Domain' => $clusterDomain, 'MemberID' => $member];
                }
                unset($binding);
                $replicas[] = ['member_id' => $member, 'config' => $replica];
            }
            return response()->json(['domain' => $clusterDomain, 'replicas' => $replicas])
                ->header('Cache-Control', 'no-store');
        }
        return response()->json($config)->header('Cache-Control', 'no-store');
    }
}
