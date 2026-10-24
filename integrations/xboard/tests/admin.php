<?php

// Real Illuminate routing, validation, SQLite transactions and Blade rendering.
// XBoard's enabled-plugin check and models use minimal boundary fixtures; this
// harness does not claim to boot a production XBoard/Sanctum/Redis installation.
namespace {
    require __DIR__ . '/vendor/autoload.php';
}
namespace App\Http\Controllers {
    class PluginController {
        public bool $disabled = false;
        protected function beforePluginAction(): ?array { return $this->disabled ? ['disabled'] : null; }
    }
}
namespace App\Models {
    class Server extends \Illuminate\Database\Eloquent\Model {
        protected $table = 'v2_server';
        protected $guarded = [];
        public $timestamps = false;
        protected $casts = ['enabled'=>'boolean', 'machine_id'=>'integer', 'protocol_settings'=>'array'];
        public static function normalizeType($type): string { return $type === 'v2ray' ? 'vmess' : ($type === 'hysteria2' ? 'hysteria' : $type); }
    }
    class ServerMachine extends \Illuminate\Database\Eloquent\Model {
        protected $table = 'v2_server_machine';
        protected $guarded = [];
        protected $hidden = ['token'];
        protected $casts = ['is_active'=>'boolean', 'last_seen_at'=>'integer'];
        public $timestamps = false;
        public function servers() { return $this->hasMany(Server::class, 'machine_id'); }
        public static function generateToken(): string { return bin2hex(random_bytes(16)); }
    }
}
namespace App\Services {
    class NodeSyncService {
        public static array $notifications = [];
        public static bool $fail = false;
        public static function notifyMachineNodesChanged(int $id): void {
            if (self::$fail) { throw new \RuntimeException('Redis unavailable'); }
            self::$notifications[] = $id;
        }
    }
}
namespace {
    use App\Models\Server;
    use App\Models\ServerMachine;
    use App\Services\NodeSyncService;
    use Illuminate\Container\Container;
    use Illuminate\Database\Capsule\Manager as Capsule;
    use Illuminate\Events\Dispatcher;
    use Illuminate\Filesystem\Filesystem;
    use Illuminate\Http\JsonResponse;
    use Illuminate\Http\Request;
    use Illuminate\Routing\Router;
    use Illuminate\Support\Facades\Facade;
    use Illuminate\Translation\ArrayLoader;
    use Illuminate\Translation\Translator;
    use Illuminate\Validation\Factory as Validator;
    use Illuminate\Validation\ValidationException;
    use Illuminate\View\Compilers\BladeCompiler;
    use Illuminate\View\Engines\CompilerEngine;
    use Illuminate\View\Engines\EngineResolver;
    use Illuminate\View\Factory;
    use Illuminate\View\FileViewFinder;
    use Plugin\AriNode\Controllers\AdminController;
    use Plugin\AriNode\Services\Deployment;
    use Symfony\Component\HttpKernel\Exception\HttpException;

    function check(bool $value, string $message): void { if (!$value) { throw new \RuntimeException($message); } }
    function rejects(callable $callback, string $type): void {
        try { $callback(); } catch (\Throwable $e) { check($e instanceof $type, 'Wrong rejection: ' . get_class($e) . ': ' . $e->getMessage()); return; }
        throw new \RuntimeException('Expected rejection: ' . $type);
    }
    function config($key) { return 'test-app-key'; }
    function admin_setting($key, $default = null) { return ['secure_path'=>$GLOBALS['testAdminPath'] ?? 'test-admin', 'app_url'=>"https://panel.example.com/sub'path"][$key] ?? $default; }
    function app($key = null) { return $key ? $GLOBALS['container']->make($key) : $GLOBALS['container']; }
    function abort_if($condition, $code, $message): void { if ($condition) { throw new HttpException($code, $message); } }
    function abort_unless($condition, $code, $message): void { abort_if(!$condition, $code, $message); }
    function response() {
        return new class {
            public function json($data, $code = 200) { return new JsonResponse($data, $code); }
            public function view($view) { return new \Illuminate\Http\Response($GLOBALS['views']->make($view)->render()); }
        };
    }
    function req(array $data = []): Request { return Request::create('https://panel.example.com/test', 'POST', $data); }
    function data(JsonResponse $response): array { return $response->getData(true); }

    $container = new Container(); Container::setInstance($container); Facade::setFacadeApplication($container);
    $events = new Dispatcher($container);
    $capsule = new Capsule($container);
    $capsule->addConnection(['driver'=>'sqlite', 'database'=>':memory:']);
    $capsule->setEventDispatcher($events); $capsule->setAsGlobal(); $capsule->bootEloquent();
    $container->instance('db', $capsule->getDatabaseManager());
    $router = new Router($events, $container); $container->instance('router', $router);
    $validator = new Validator(new Translator(new ArrayLoader(), 'en'), $container);
    $container->instance('validator', $validator);
    Request::macro('validate', function (array $rules) use ($validator) { return $validator->make($this->all(), $rules)->validate(); });

    $cache = sys_get_temp_dir() . '/arinode-plugin-views-' . bin2hex(random_bytes(6)); mkdir($cache, 0700);
    $files = new Filesystem(); $compiler = new BladeCompiler($files, $cache);
    $resolver = new EngineResolver(); $resolver->register('blade', fn() => new CompilerEngine($compiler));
    $finder = new FileViewFinder($files, []); $finder->addNamespace('AriNode', __DIR__ . '/../AriNode/resources/views');
    $views = new Factory($resolver, $finder, $events); $views->setContainer($container);

    Capsule::schema()->create('v2_server_machine', function ($table) {
        $table->increments('id'); $table->string('name'); $table->text('notes')->nullable();
        $table->string('token'); $table->boolean('is_active')->default(true); $table->integer('last_seen_at')->nullable();
    });
    Capsule::schema()->create('v2_server', function ($table) {
        $table->increments('id'); $table->string('name'); $table->string('type'); $table->string('host');
        $table->boolean('enabled')->default(true); $table->integer('machine_id')->nullable(); $table->text('protocol_settings')->nullable();
    });
    require __DIR__ . '/../AriNode/Services/Deployment.php';
    require __DIR__ . '/../AriNode/Controllers/AdminController.php';
    require __DIR__ . '/../AriNode/routes/api.php';
    require __DIR__ . '/../AriNode/routes/web.php';
    $routes = [];
    foreach ($router->getRoutes() as $route) { $routes['/' . $route->uri()] = $route; }
    foreach (['/api/v1/arinode/admin/update','/api/v1/arinode/admin/entry','/api/v2/test-admin/plugin/ari-node/entry','/api/v1/arinode/admin/servers','/api/v1/arinode/admin/nodes','/api/v1/arinode/admin/servers/save','/api/v1/arinode/admin/command',
        '/api/v2/test-admin/plugin/ari-node/servers/fetch','/api/v2/test-admin/plugin/ari-node/servers/save','/api/v2/test-admin/plugin/ari-node/nodes/fetch'] as $path) {
        check(in_array('admin', $routes[$path]->gatherMiddleware(), true), "Unprotected data route {$path}");
    }
    check($routes['/arinode/admin']->methods() === ['GET', 'HEAD'], 'HTML shell route missing');
    $updateRoutes = array_filter($router->getRoutes()->getRoutes(), fn($route) => $route->uri() === 'api/v1/arinode/admin/update');
    check(count($updateRoutes) === 2, 'Update must provide separate GET check and POST install routes');
    foreach ($updateRoutes as $route) { check(in_array('admin', $route->gatherMiddleware(), true), 'Unprotected update route'); }
    $testAdminPath = '';
    require __DIR__ . '/../AriNode/routes/api.php';
    $emptyPath = array_filter($router->getRoutes()->getRoutes(), fn($route) => $route->uri() === 'api/v2/plugin/ari-node/servers/fetch');
    check(count($emptyPath) === 1 && in_array('admin', array_values($emptyPath)[0]->gatherMiddleware(),true), 'Empty secure_path generated an unusable/unprotected alias');
    unset($testAdminPath);

    $other = ServerMachine::create(['name'=>'Existing server','token'=>'existing-token','is_active'=>true]);
    foreach ([['vless',null],['anytls',null],['vless',$other->id],['naive',null],['hysteria',null]] as $i => [$type,$machine]) {
        Server::create(['name'=>'Node ' . ($i+1), 'type'=>$type, 'host'=>'node.example.com', 'enabled'=>true, 'machine_id'=>$machine,
            'protocol_settings'=>['version'=>2]]);
    }
    $controller = new AdminController();
    check(data($controller->entry())['data'][0]['url'] === '/arinode/admin', 'Plugin menu entry points to the wrong page');
    $saved = data($controller->save(req(['name'=>'US-01','notes'=>'US / 192.0.2.1','node_ids'=>[1,2]])));
    $id = $saved['data']['id']; $machine = ServerMachine::findOrFail($id);
    check(strlen($machine->token) === 32 && $machine->is_active, 'Machine token/default enabled was not saved');
    check(Server::where('machine_id',$id)->pluck('id')->all() === [1,2], 'Nodes not bound');
    check(NodeSyncService::$notifications === [$id], 'Final bindings were not published once');
    $command = data($controller->command(req(['id'=>$id])))['data']['command'];
    check(str_contains($command, Deployment::INSTALLER) && str_contains($command, "ARINODE_NODES='vless:1 anytls:2'"), 'Command missing installer/nodes');
    check(str_contains($command,"ARINODE_CORE='sing'") && str_contains($command,"ARINODE_INSTALL_MODE='fresh'") && str_contains($command,"ARINODE_MACHINE_ID='{$id}'"), 'Default deployment flags incorrect');
    check(str_contains($command, "sub'\"'\"'path'"), 'Panel URL was not shell quoted');
    rejects(fn() => $controller->command(req(['id'=>$id,'core'=>'xray'])), ValidationException::class);

    $count = ServerMachine::count();
    foreach ([['name'=>'Conflict','node_ids'=>[3]],['name'=>'Unknown','node_ids'=>[999]],['name'=>'Unsupported','node_ids'=>[4]],
        ['name'=>'Dup','node_ids'=>[1,1]],['name'=>'','node_ids'=>[1]],['name'=>'Bad','node_ids_text'=>[1]],['name'=>'Empty','node_ids'=>[]]] as $payload) {
        rejects(fn() => $controller->save(req($payload)), ValidationException::class);
        check(ServerMachine::count() === $count && Server::find(3)->machine_id === $other->id, 'Failed save mutated a different machine');
    }
    // Failure during editing rolls back fields and bindings together.
    rejects(fn() => $controller->save(req(['id'=>$id,'name'=>'Bad edit','node_ids'=>[3]])), ValidationException::class);
    check(ServerMachine::find($id)->name === 'US-01' && Server::find(1)->machine_id === $id, 'Rejected edit was partially applied');
    $controller->save(req(['id'=>$id,'name'=>'US updated','node_ids_text'=>'2, 5','install_command'=>'malicious ignored input']));
    check(Server::find(1)->machine_id === null && Server::find(2)->machine_id === $id && Server::find(5)->machine_id === $id, 'Binding replacement incorrect');
    check(Server::find(3)->machine_id === $other->id, 'Another machine changed');
    check(ServerMachine::find($id)->token === $machine->token, 'Editing rotated the original machine token');
    $command = data($controller->command(req(['id'=>$id,'language'=>'en'])))['data']['command'];
    check(str_contains($command,"ARINODE_LANGUAGE='en'") && str_contains($command,'hysteria2:5'), 'HY2 or language normalization incorrect');
    $inventory = data($controller->servers(req(['search'=>'US updated','pageSize'=>1])));
    check($inventory['total'] === 1 && count($inventory['data']) === 1 && !isset($inventory['data'][0]['token']), 'Inventory filtering/secret shape incorrect');
    check($inventory['data'][0]['node_ids_text'] === '2,5' && $inventory['data'][0]['install_command'] !== '', 'Native CRUD deployment row incomplete');
    check(data($controller->servers(req(['current'=>2,'pageSize'=>1])))['data'][0]['id'] === $id, 'Server pagination incorrect');
    $nodePage = data($controller->nodes(req(['current'=>2,'pageSize'=>2])));
    check($nodePage['total'] === 5 && count($nodePage['data']) === 2 && !$nodePage['data'][1]['supported'], 'Node pagination/protocol support incorrect');
    rejects(fn() => $controller->servers(req(['pageSize'=>101])), ValidationException::class);

    NodeSyncService::$fail = true;
    $saved = data($controller->save(req(['id'=>$id,'name'=>'US updated','node_ids'=>[2,5],'is_active'=>false])));
    check($saved['warning'] !== null && !ServerMachine::find($id)->is_active, 'Post-commit Redis failure lost a successful save');
    rejects(fn() => $controller->command(req(['id'=>$id])), HttpException::class);
    $disabledRow = data($controller->servers(req(['search'=>'US updated'])))['data'][0];
    check($disabledRow['install_command'] === '' && $disabledRow['deploy_error'] !== '', 'Disabled machine leaked an executable command');
    $controller->disabled = true;
    foreach ([fn() => $controller->entry(), fn() => $controller->servers(req()), fn() => $controller->nodes(req()), fn() => $controller->save(req()), fn() => $controller->command(req()), fn() => $controller->page()] as $action) {
        rejects($action, HttpException::class);
    }
    $controller->disabled = false;
    foreach (['https://user:pass@example.com','javascript:alert(1)','https://panel.example.com?token=x','https://panel.example.com/#x'] as $url) {
        rejects(fn() => Deployment::command($url,'token',$id,[Server::find(2)]), InvalidArgumentException::class);
    }
    rejects(fn() => Deployment::command('https://panel.example.com',"bad\nvalue",$id,[Server::find(2)]), InvalidArgumentException::class);
    $quoted = Deployment::command('https://panel.example.com',"test'\$(touch /tmp/arinode-shell-injection)",$id,[Server::find(2)]);
    check(str_contains($quoted,"test'\"'\"'\$(touch /tmp/arinode-shell-injection)'"), 'Token shell injection was not escaped');

    if (PHP_OS_FAMILY !== 'Windows') {
        // Execute the generated pipeline with harmless curl/sudo fixtures. This
        // checks real Bash quoting and env propagation without network or root.
        $bin = $cache . '/bin'; mkdir($bin,0700);
        file_put_contents($bin . '/curl', <<<'SH'
#!/bin/sh
cat <<'INSTALLER'
printf '%s\n' "$ARINODE_PANEL_TOKEN" "$ARINODE_NODES" "$ARINODE_CORE"
INSTALLER
SH
        );
        file_put_contents($bin . '/sudo', "#!/bin/sh\nexec \"\$@\"\n");
        chmod($bin . '/curl',0700); chmod($bin . '/sudo',0700);
        $pipes = [];
        $proc = proc_open(['bash','-c',$quoted],[1=>['pipe','w'],2=>['pipe','w']],$pipes,null,['PATH'=>$bin . ':' . getenv('PATH')]);
        $output = stream_get_contents($pipes[1]); $error = stream_get_contents($pipes[2]); fclose($pipes[1]); fclose($pipes[2]);
        check(proc_close($proc) === 0 && $error === '', 'Generated pipeline failed');
        check(explode("\n",trim($output)) === ["test'\$(touch /tmp/arinode-shell-injection)",'anytls:2','sing'], 'Shell expanded or lost deployment values');
    }

    $html = $controller->page();
    check(str_contains($html->getContent(),'保存并生成部署命令') && !str_contains($html->getContent(),$machine->token), 'Rendered page is broken or exposes token before authentication');
    check(str_contains($html->headers->get('Content-Security-Policy'),"connect-src 'self'") && $html->headers->get('Cache-Control') === 'no-store, private', 'Page cache/CSP headers incorrect');
    check(!str_contains($html->getContent(),'@include'), 'Blade partials did not compile');
    if ($target = getenv('ARINODE_TEST_RENDER')) { file_put_contents($target,$html->getContent()); }
    $files->deleteDirectory($cache);
    echo "XBoard admin regression tests passed (real validation, SQLite, routes and Blade)\n";
}
