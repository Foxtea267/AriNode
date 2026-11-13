<?php

namespace Plugin\AriNode\Controllers;

use App\Http\Controllers\PluginController;
use App\Models\Plugin;
use Illuminate\Http\Request;
use Symfony\Component\HttpFoundation\Response;

/** Fixed public assets; never embeds server records or deployment credentials. */
class NativeAssetsController extends PluginController
{
    public static function version(): string
    {
        $content = @file_get_contents(dirname(__DIR__) . '/config.json');
        $config = is_string($content) ? json_decode($content, true) : null;
        $version = is_array($config) ? ($config['version'] ?? '') : '';
        return is_string($version) && preg_match('/\A\d+\.\d+\.\d+\z/D', $version) ? $version : '0';
    }

    public function script(Request $request): Response
    {
        return $this->asset($request, 'js', 'application/javascript; charset=UTF-8');
    }

    public function stylesheet(Request $request): Response
    {
        return $this->asset($request, 'css', 'text/css; charset=UTF-8');
    }

    private function asset(Request $request, string $extension, string $mime): Response
    {
        // XBoard caches beforePluginAction's enabled flag on the controller.
        // A route/controller may survive across Octane requests, so query the
        // current state rather than retaining that object's previous result.
        abort_if(!Plugin::query()->where('code', 'ari_node')->where('is_enabled', true)->exists(), 403, 'AriNode plugin is disabled');
        $content = @file_get_contents(dirname(__DIR__) . '/resources/assets/native-table.' . $extension);
        abort_if(!is_string($content), 404, 'AriNode asset is unavailable');
        $response = new Response($content, 200, [
            'Content-Type' => $mime,
            'Cache-Control' => 'public, max-age=300, must-revalidate',
            'X-Content-Type-Options' => 'nosniff',
            'Referrer-Policy' => 'no-referrer',
        ]);
        $response->setEtag(hash('sha256', $content));
        $response->isNotModified($request);
        return $response;
    }
}
