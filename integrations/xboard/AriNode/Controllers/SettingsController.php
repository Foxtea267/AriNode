<?php

namespace Plugin\AriNode\Controllers;

use App\Http\Controllers\PluginController;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Validation\ValidationException;
use InvalidArgumentException;
use Plugin\AriNode\Services\DeploymentSettings;

class SettingsController extends PluginController
{
    public function show(Request $request): JsonResponse
    {
        abort_if((bool) $this->beforePluginAction(), 403, 'AriNode plugin is disabled');
        $warning = null;
        $effective = '';
        try { $effective = DeploymentSettings::panelUrl($request); }
        catch (InvalidArgumentException $e) { $warning = $e->getMessage(); }
        return response()->json(['data' => ['api_url' => DeploymentSettings::configuredUrl(),
            'effective_url' => $effective, 'warning' => $warning]])->header('Cache-Control', 'no-store, private');
    }

    public function save(Request $request): JsonResponse
    {
        abort_if((bool) $this->beforePluginAction(), 403, 'AriNode plugin is disabled');
        $params = $request->validate(['api_url' => 'present|nullable|string|max:2048']);
        try { DeploymentSettings::save($params['api_url'] ?? ''); }
        catch (InvalidArgumentException $e) { throw ValidationException::withMessages(['api_url' => $e->getMessage()]); }
        return $this->show($request);
    }
}
