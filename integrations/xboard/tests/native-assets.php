<?php

// Real Illuminate router/middleware, SQLite plugin state and HTTP responses.
// XBoard's base plugin/controller are boundary fixtures, not production boot.
namespace { require __DIR__ . '/vendor/autoload.php'; }
namespace App\Http\Controllers {
    class PluginController {
        public bool $disabled = false;
        protected function beforePluginAction(): ?array { return $this->disabled ? ['disabled'] : null; }
    }
}
namespace App\Services\Plugin {
    abstract class AbstractPlugin {
        public function __construct(string $code) {}
        abstract public function boot(): void;
    }
}
namespace App\Models {
    class Plugin extends \Illuminate\Database\Eloquent\Model {
        protected $table = 'v2_plugins'; protected $guarded = []; public $timestamps = false;
    }
}
namespace {
    use App\Models\Plugin as PluginModel;
    use Illuminate\Container\Container;
    use Illuminate\Database\Capsule\Manager as Capsule;
    use Illuminate\Events\Dispatcher;
    use Illuminate\Http\Request;
    use Illuminate\Routing\Router;
    use Illuminate\Support\Facades\Facade;
    use Plugin\AriNode\Controllers\NativeAssetsController;
    use Plugin\AriNode\Middleware\AdminPageAssets;
    use Plugin\AriNode\Plugin;
    use Symfony\Component\HttpFoundation\BinaryFileResponse;
    use Symfony\Component\HttpFoundation\Response;
    use Symfony\Component\HttpFoundation\StreamedResponse;
    use Symfony\Component\HttpKernel\Exception\HttpException;

    function check(bool $ok, string $message): void { if (!$ok) { throw new \RuntimeException($message); } }
    function config($key) { return 'private-application-signing-key'; }
    function admin_setting($key, $default = null) { return $GLOBALS['adminSettings'][$key] ?? $default; }
    function app($key = null) { return $key ? $GLOBALS['container']->make($key) : $GLOBALS['container']; }
    function abort_if($condition, $code, $message): void { if ($condition) { throw new HttpException($code, $message); } }
    function rejectStatus(callable $action, int $status): void {
        try { $action(); } catch (HttpException $e) { check($e->getStatusCode() === $status, 'Unexpected rejection status'); return; }
        throw new \RuntimeException('Expected HTTP rejection');
    }
    function dispatch(Request $request): Response {
        $GLOBALS['container']->instance('request', $request);
        $GLOBALS['container']->instance(Request::class, $request);
        return $GLOBALS['router']->dispatch($request);
    }
    $container = new Container(); Container::setInstance($container); Facade::setFacadeApplication($container);
    $events = new Dispatcher($container); $router = new Router($events, $container);
    $container->instance('router', $router);
    $container->bind(\Illuminate\Routing\Contracts\CallableDispatcher::class, \Illuminate\Routing\CallableDispatcher::class);
    $capsule = new Capsule($container);
    $capsule->addConnection(['driver' => 'sqlite', 'database' => ':memory:']);
    $capsule->setEventDispatcher($events); $capsule->setAsGlobal(); $capsule->bootEloquent();
    $container->instance('db', $capsule->getDatabaseManager());
    Capsule::schema()->create('v2_plugins', function ($table) {
        $table->increments('id'); $table->string('code'); $table->boolean('is_enabled'); $table->text('config');
    });
    PluginModel::create(['code' => 'ari_node', 'is_enabled' => true, 'config' => '{"token":"private-machine-credential"}']);
    require __DIR__ . '/../AriNode/Controllers/NativeAssetsController.php';
    require __DIR__ . '/../AriNode/Middleware/AdminPageAssets.php';
    require __DIR__ . '/../AriNode/Plugin.php';
    $adminSettings = ['secure_path' => 'private-admin'];
    $plugin = new Plugin('ari_node');
    $plugin->boot(); $plugin->boot(); $plugin->boot();
    check($router->getMiddlewareGroups()['web'] === [AdminPageAssets::class], 'Repeated plugin boot duplicated middleware');
    $router->middleware('web')->group(function () use ($router) {
        require __DIR__ . '/../AriNode/routes/web.php';
        $router->get('/private-admin', fn() => new Response('<html><body>Admin shell</body></html>', 200, ['Content-Type' => 'text/html']));
    });
    $routes = [];
    foreach ($router->getRoutes() as $route) { $routes['/' . $route->uri()] = $route; }
    foreach (['/arinode/native-table.js', '/arinode/native-table.css'] as $path) {
        check(isset($routes[$path]) && $routes[$path]->methods() === ['GET', 'HEAD'], 'Missing fixed asset route');
        check(in_array('web', $routes[$path]->gatherMiddleware(), true), 'Asset route lost upstream web group');
    }

    $middleware = new AdminPageAssets();
    $html = '<!doctype html><html><head></head><body>Admin shell</body></html>';
    $version = json_decode(file_get_contents(__DIR__ . '/../AriNode/config.json'), true)['version'];
    check(NativeAssetsController::version() === $version, 'Asset version differs from plugin manifest');
    $response = new Response($html, 200, ['Content-Type' => 'text/html; charset=UTF-8', 'Content-Length' => (string) strlen($html),
        'ETag' => '"old"', 'Last-Modified' => 'Wed, 21 Oct 2015 07:28:00 GMT', 'Content-MD5' => 'old', 'Digest' => 'old', 'Content-Digest' => 'old',
        'Content-Security-Policy' => "default-src 'none'; script-src 'nonce-existing'; style-src 'nonce-existing'", 'Cache-Control' => 'private, no-store']);
    $request = Request::create('https://panel.example/private-admin?next=anything', 'GET');
    $result = $middleware->handle($request, fn() => $response);
    check($result === $response && str_contains($result->getContent(), '/arinode/native-table.js?v=' . $version), 'Admin HTML did not get versioned script');
    check(str_contains($result->getContent(), '/arinode/native-table.css?v=' . $version), 'Admin HTML did not get versioned stylesheet');
    check(str_ends_with($result->getContent(), '</script></body></html>'), 'Assets were not inserted before closing body');
    check($result->headers->get('Content-Security-Policy') === "default-src 'none'; script-src 'nonce-existing'; style-src 'nonce-existing'", 'Existing CSP was weakened');
    check(str_contains($result->headers->get('Cache-Control'), 'no-store'), 'Existing cache policy was changed');
    foreach (['Content-Length', 'ETag', 'Last-Modified', 'Content-MD5', 'Digest', 'Content-Digest'] as $header) {
        check(!$result->headers->has($header), 'Stale response validator remained: ' . $header);
    }
    $once = $result->getContent();
    $middleware->handle($request, fn() => $result);
    check($result->getContent() === $once, 'Assets were injected twice');

    foreach ([['/', 'GET'], ['/private-admin/child', 'GET'], ['/private-admin/', 'GET'], ['/arinode/admin', 'GET'],
        ['/api/v2/private-admin/plugin/ari-node/servers/fetch', 'GET'], ['/private-admin', 'POST'], ['/private-admin', 'HEAD']] as [$path, $method]) {
        $plain = new Response($html, 200, ['Content-Type' => 'text/html', 'ETag' => '"unchanged"']);
        $middleware->handle(Request::create($path, $method), fn() => $plain);
        check($plain->getContent() === $html && $plain->headers->get('ETag') === '"unchanged"', 'Non-admin response changed: ' . $method . ' ' . $path);
    }
    foreach ([['', 200, 'text/html'], ['<html>no body', 200, 'text/html'], [$html, 403, 'text/html'], [$html, 302, 'text/html'],
        [$html, 200, 'application/json'], [$html, 200, 'application/xhtml+xml'], [$html, 200, '']] as [$body, $status, $mime]) {
        $plain = new Response($body, $status, $mime === '' ? [] : ['Content-Type' => $mime]);
        $middleware->handle($request, fn() => $plain);
        check($plain->getContent() === $body, 'Unsafe response type/status/content was changed');
    }
    $streamRead = false;
    $stream = new StreamedResponse(function () use (&$streamRead) { $streamRead = true; echo 'stream'; }, 200, ['Content-Type' => 'text/html']);
    check($middleware->handle($request, fn() => $stream) === $stream && !$streamRead, 'Stream was inspected or replaced');
    $binaryPath = tempnam(sys_get_temp_dir(), 'arinode-native-');
    file_put_contents($binaryPath, $html);
    try {
        $binary = new BinaryFileResponse($binaryPath, 200, ['Content-Type' => 'text/html']);
        check($middleware->handle($request, fn() => $binary) === $binary, 'Binary response was replaced');
    } finally { unlink($binaryPath); }
    $compressed = new Response(gzencode($html), 200, ['Content-Type' => 'text/html', 'Content-Encoding' => 'gzip']);
    $beforeCompressed = $compressed->getContent();
    $middleware->handle($request, fn() => $compressed);
    check($compressed->getContent() === $beforeCompressed, 'Compressed response was modified');

    foreach ([['frontend_admin_path' => 'legacy-admin'], []] as $settings) {
        $adminSettings = $settings;
        $path = $settings['frontend_admin_path'] ?? hash('crc32b', config('app.key'));
        $plain = new Response(str_replace('</body>', '</BODY>', $html), 200, ['Content-Type' => 'TEXT/HTML; charset=UTF-8']);
        $middleware->handle(Request::create('/' . $path), fn() => $plain);
        check(str_contains($plain->getContent(), 'data-arinode-native-assets'), 'Legacy admin path/case-insensitive HTML failed');
    }
    $adminSettings = ['secure_path' => 'nested/private-admin'];
    $plain = new Response($html, 200, ['Content-Type' => 'text/html']);
    $middleware->handle(Request::create('/nested/private-admin'), fn() => $plain);
    check(str_contains($plain->getContent(), 'data-arinode-native-assets'), 'Nested secure path did not match');
    $adminSettings = ['secure_path' => ''];
    $plain = new Response($html, 200, ['Content-Type' => 'text/html']);
    $middleware->handle(Request::create('/'), fn() => $plain);
    check($plain->getContent() === $html, 'Empty secure path enhanced the public homepage');
    $adminSettings = ['secure_path' => 'private-admin'];
    $dispatched = dispatch(Request::create('/private-admin'));
    check(str_contains($dispatched->getContent(), 'data-arinode-native-assets'), 'Registered web middleware did not run through actual router');
    PluginModel::where('code', 'ari_node')->update(['is_enabled' => false]);
    $disabled = dispatch(Request::create('/private-admin'));
    check(!str_contains($disabled->getContent(), 'data-arinode-native-assets'), 'Long-lived router ignored plugin disable');
    PluginModel::where('code', 'ari_node')->update(['is_enabled' => true]);
    check(str_contains(dispatch(Request::create('/private-admin'))->getContent(), 'data-arinode-native-assets'), 'Plugin re-enable did not take effect');

    $controller = new NativeAssetsController();
    foreach (['js' => 'application/javascript; charset=UTF-8', 'css' => 'text/css; charset=UTF-8'] as $extension => $mime) {
        $path = '/arinode/native-table.' . $extension;
        $asset = dispatch(Request::create($path . '?v=' . $version));
        $expected = file_get_contents(__DIR__ . '/../AriNode/resources/assets/native-table.' . $extension);
        check($asset->getStatusCode() === 200 && $asset->getContent() === $expected, 'Asset body is not the fixed packaged resource');
        check($asset->headers->get('Content-Type') === $mime, 'Wrong asset MIME type');
        check($asset->headers->get('X-Content-Type-Options') === 'nosniff', 'Asset missing nosniff');
        check(str_contains($asset->headers->get('Cache-Control'), 'max-age=300') && str_contains($asset->headers->get('Cache-Control'), 'must-revalidate'), 'Asset cache policy is unbounded');
        check(!str_contains($asset->getContent(), 'private-machine-credential') && !str_contains($asset->getContent(), config('app.key')), 'Asset leaked private plugin data');
        $conditional = Request::create($path); $conditional->headers->set('If-None-Match', $asset->getEtag());
        check(dispatch($conditional)->getStatusCode() === 304, 'Asset conditional caching failed');
        $query = dispatch(Request::create($path . '?file=../../config.json&v=wrong'));
        check($query->getContent() === $expected, 'Asset query selected an arbitrary file');
    }
    PluginModel::where('code', 'ari_node')->update(['is_enabled' => false]);
    rejectStatus(fn() => $controller->script(Request::create('/arinode/native-table.js')), 403);
    rejectStatus(fn() => $controller->stylesheet(Request::create('/arinode/native-table.css')), 403);
    rejectStatus(fn() => dispatch(Request::create('/arinode/native-table.js')), 403);
    PluginModel::where('code', 'ari_node')->update(['is_enabled' => true]);
    check($controller->script(Request::create('/arinode/native-table.js'))->getStatusCode() === 200, 'Existing asset controller kept stale enabled state');
    Capsule::schema()->drop('v2_plugins');
    $plain = new Response($html, 200, ['Content-Type' => 'text/html']);
    $middleware->handle($request, fn() => $plain);
    check($plain->getContent() === $html, 'Missing plugin DB broke admin page instead of skipping optional assets');
    echo "Native admin assets tests passed\n";
}
