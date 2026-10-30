<?php

namespace Plugin\AriNode\Controllers;

use App\Http\Controllers\PluginController;
use App\Models\Server;
use App\Services\ServerService;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;

class ConfigController extends PluginController
{
    public function download(Request $request): JsonResponse
    {
        abort_if((bool) $this->beforePluginAction(), 403, 'AriNode plugin is disabled');
        // server.v2 authenticates the machine token and checks node ownership.
        // Query again instead of serving a cached config or WebSocket payload.
        $authorized = $request->attributes->get('node_info');
        abort_unless($authorized instanceof Server, 422, 'Authenticated node is required');
        $node = Server::query()->find($authorized->id);
        abort_unless($node && $node->enabled && (int) $node->machine_id === (int) $authorized->machine_id,
            409, 'Node binding changed; synchronize machine bindings');
        $payload = ServerService::buildNodeConfig($node);
        // Some XBoard releases store these fields but omit them from the native API.
        foreach (['custom_outbounds', 'custom_routes', 'custom_route_rules'] as $field) {
            $value = $node->getAttribute($field);
            if (!empty($value)) { $payload[$field] = $value; }
        }
        $payload['base_config'] = [
            'pull_interval' => max(1, min(30, (int) admin_setting('server_pull_interval', 30))),
            'push_interval' => max(1, (int) admin_setting('server_push_interval', 60)),
        ];
        $payload = $this->stable($payload);
        $encoded = json_encode($payload, JSON_THROW_ON_ERROR | JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE);
        // Always read/build the current config, even when a proxy supplies an old
        // If-None-Match. The agent compares its normalized runtime model itself.
        return response()->json($payload)->header('Cache-Control', 'no-store, private')
            ->header('X-AriNode-Config', '1')->header('X-AriNode-Config-Version', hash('sha256', $encoded));
    }

    private function stable(array $value): array
    {
        foreach ($value as $key => $item) {
            if ($item instanceof \JsonSerializable) { $item = $item->jsonSerialize(); }
            if (is_array($item)) { $value[$key] = $this->stable($item); }
        }
        if (!array_is_list($value)) { ksort($value, SORT_STRING); }
        return $value;
    }
}
