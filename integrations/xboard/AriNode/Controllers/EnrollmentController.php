<?php

namespace Plugin\AriNode\Controllers;

use App\Http\Controllers\PluginController;
use App\Models\ServerMachine;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use InvalidArgumentException;
use Plugin\AriNode\Services\Deployment;

class EnrollmentController extends PluginController
{
    public function enroll(Request $request): JsonResponse
    {
        abort_if((bool) $this->beforePluginAction(), 403, 'AriNode plugin is disabled');
        $params = $request->validate(['code' => 'required|string|max:1024']);
        try {
            $ticket = Deployment::verify($params['code']);
        } catch (InvalidArgumentException $e) {
            return response()->json(['message' => $e->getMessage()], 403)->header('Cache-Control', 'no-store, private');
        }
        $machine = ServerMachine::query()->find($ticket['id']);
        if (!$machine || !$machine->is_active || !hash_equals(Deployment::credentialFingerprint($machine->token), $ticket['key'])) {
            return response()->json(['message' => 'Machine credential changed or the machine is unavailable'], 403)
                ->header('Cache-Control', 'no-store, private');
        }
        return response()->json([
            'machine_id' => $machine->id, 'token' => $machine->token, 'core' => $ticket['core'],
            'multi_node' => $ticket['multi_node'], 'multi_panel' => $ticket['multi_panel'],
        ])->header('Cache-Control', 'no-store, private');
    }
}
