<?php

namespace Plugin\AriNode\Controllers;

use App\Http\Controllers\PluginController;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Plugin\AriNode\Services\PluginUpdate;
use Throwable;

class UpdateController extends PluginController
{
    public function check(PluginUpdate $updater): JsonResponse
    {
        abort_if((bool) $this->beforePluginAction(), 403, 'AriNode plugin is disabled');
        try { return response()->json(['data' => $updater->check()])->header('Cache-Control', 'no-store'); }
        catch (Throwable $e) { return response()->json(['message' => $e->getMessage()], 422)->header('Cache-Control', 'no-store'); }
    }

    public function install(Request $request, PluginUpdate $updater): JsonResponse
    {
        abort_if((bool) $this->beforePluginAction(), 403, 'AriNode plugin is disabled');
        $params = $request->validate(['version' => ['required', 'string', 'regex:/^\d+\.\d+\.\d+$/D'],
            'commit' => ['required', 'string', 'regex:/^[a-f0-9]{40}$/D']]);
        @set_time_limit(120);
        try { return response()->json(['data' => $updater->install($params['version'], $params['commit'])])->header('Cache-Control', 'no-store'); }
        catch (Throwable $e) { return response()->json(['message' => $e->getMessage()], 422)->header('Cache-Control', 'no-store'); }
    }
}
