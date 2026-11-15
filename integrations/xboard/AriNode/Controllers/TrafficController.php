<?php

namespace Plugin\AriNode\Controllers;

use App\Http\Controllers\PluginController;
use App\Models\Server;
use App\Services\ServerService;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Cache;

class TrafficController extends PluginController
{
    private const MAX_AGE = 604800;
    private const RECEIPT_TTL = 691200;

    private function node(Request $request): Server
    {
        abort_if((bool) $this->beforePluginAction(), 403, 'AriNode plugin is disabled');
        $node = $request->attributes->get('node_info');
        abort_unless($node instanceof Server && $node->enabled, 422, 'Enabled node is required');
        return $node;
    }

    public function info(Request $request)
    {
        $node = $this->node($request);
        return response()->json(['version' => 1, 'node_id' => (int) $node->id])->header('Cache-Control', 'no-store');
    }

    public function report(Request $request)
    {
        $node = $this->node($request);
        $data = $request->validate([
            'report_id' => ['required', 'string', 'regex:/\A[0-9]{10}-[a-f0-9]{32}\z/'],
            'traffic' => 'required|array|max:100000', 'traffic.*' => 'array|size:2',
            'traffic.*.*' => 'integer|min:0',
        ]);
        $id = $data['report_id'];
        $created = (int) substr($id, 0, 10);
        $now = time();
        // Reject old IDs even after Redis has expired their receipts.
        abort_unless($created >= $now - self::MAX_AGE && $created <= $now + 300, 422, 'Traffic report timestamp outside accepted window');
        $traffic = [];
        foreach ($data['traffic'] as $uid => $row) {
            abort_unless(is_int($uid) && $uid > 0 && array_is_list($row), 422, 'Invalid traffic row');
            $traffic[$uid] = [(int) $row[0], (int) $row[1]];
        }
        ksort($traffic, SORT_NUMERIC);
        $hash = hash('sha256', json_encode($traffic, JSON_THROW_ON_ERROR));
        $key = 'arinode:traffic:v1:' . $node->id . ':' . $id;
        $store = Cache::store('redis');
        // Redis add is atomic. Leave the marker in place on exceptions or
        // ambiguous writes: native XBoard enqueues several independent jobs.
        if (!$store->add($key, ['hash' => $hash, 'accepted' => false], self::RECEIPT_TTL)) {
            $receipt = $store->get($key);
            abort_unless(is_array($receipt) && $receipt['hash'] === $hash, 409, 'Traffic receipt unavailable or payload changed');
            abort_unless($receipt['accepted'] === true, 409, 'Traffic submission pending or requires reconciliation');
            return response()->json(['accepted' => true, 'report_id' => $id, 'duplicate' => true])->header('Cache-Control', 'no-store');
        }
        ServerService::processTraffic($node, $traffic);
        abort_unless($store->put($key, ['hash' => $hash, 'accepted' => true], self::RECEIPT_TTL), 503, 'Traffic receipt could not be confirmed');
        return response()->json(['accepted' => true, 'report_id' => $id])->header('Cache-Control', 'no-store');
    }
}
