<?php

namespace Plugin\AriNode\Middleware;

use App\Models\Plugin;
use Closure;
use Illuminate\Http\Request;
use Plugin\AriNode\Controllers\NativeAssetsController;
use Symfony\Component\HttpFoundation\BinaryFileResponse;
use Symfony\Component\HttpFoundation\Response;
use Symfony\Component\HttpFoundation\StreamedResponse;
use Throwable;

/** Enhance XBoard's existing admin shell without changing upstream files. */
final class AdminPageAssets
{
    public function handle(Request $request, Closure $next): Response
    {
        $response = $next($request);
        if (!$request->isMethod('GET') || !$this->isAdminPage($request)
            || $response->getStatusCode() !== 200
            || $response instanceof StreamedResponse || $response instanceof BinaryFileResponse
            || $response->headers->has('Content-Encoding')
            || !preg_match('~^text/html(?:\s*;|$)~i', (string) $response->headers->get('Content-Type'))) {
            return $response;
        }
        $content = $response->getContent();
        if (!is_string($content) || $content === '' || str_contains($content, 'data-arinode-native-assets')) {
            return $response;
        }
        $position = strripos($content, '</body>');
        if ($position === false || !$this->isEnabled()) {
            return $response;
        }
        $version = NativeAssetsController::version();
        // External same-origin resources respect the site's existing CSP. Never
        // loosen its policy merely to enable the optional table enhancement.
        $assets = '<link rel="stylesheet" href="/arinode/native-table.css?v=' . $version . '" data-arinode-native-assets>'
            . '<script defer src="/arinode/native-table.js?v=' . $version . '"></script>';
        $response->setContent(substr_replace($content, $assets, $position, 0));
        foreach (['Content-Length', 'ETag', 'Last-Modified', 'Content-MD5', 'Digest', 'Content-Digest'] as $header) {
            $response->headers->remove($header);
        }
        return $response;
    }

    private function isAdminPage(Request $request): bool
    {
        try {
            $path = admin_setting('secure_path', admin_setting('frontend_admin_path', hash('crc32b', config('app.key'))));
            return is_string($path) && trim($path, '/') !== ''
                && $request->getPathInfo() === '/' . trim($path, '/');
        } catch (Throwable $e) {
            return false;
        }
    }

    private function isEnabled(): bool
    {
        try {
            // Read each request: Octane can retain a previously registered web
            // middleware after the administrator disables this plugin.
            return Plugin::query()->where('code', 'ari_node')->where('is_enabled', true)->exists();
        } catch (Throwable $e) {
            // Optional styling must not break the administrator's login page.
            return false;
        }
    }
}
