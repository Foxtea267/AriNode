<?php

// Real SQLite transactions, filesystem swaps, flock and ZipArchive. XBoard's
// plugin manager and HTTPS are boundary fixtures (no network or production writes).
namespace { require __DIR__ . '/vendor/autoload.php'; }
namespace App\Models {
    class Plugin extends \Illuminate\Database\Eloquent\Model {
        protected $table = 'v2_plugins'; protected $guarded = []; public $timestamps = false;
        protected $casts = ['is_enabled' => 'boolean'];
    }
}
namespace App\Http\Controllers {
    class PluginController {
        public bool $disabled = false;
        protected function beforePluginAction(): ?array { return $this->disabled ? ['disabled'] : null; }
    }
}
namespace App\Services\Plugin {
    class PluginManager {
        public string $path; public bool $fail = false; public int $calls = 0;
        public function getPluginPath(string $code): string { return $this->path; }
        public function isCorePlugin(string $code): bool { return false; }
        public function update(string $code): bool {
            if ($code !== 'ari_node') { throw new \RuntimeException('Wrong plugin'); }
            $this->calls++;
            $row = \App\Models\Plugin::where('code', $code)->firstOrFail();
            $row->update(['is_enabled' => false]);
            $config = json_decode(file_get_contents($this->path . '/config.json'), true);
            $row->update(['version' => $config['version']]);
            if ($this->fail) { throw new \RuntimeException('simulated native upgrade failure'); }
            $row->update(['is_enabled' => true]); return true;
        }
    }
}
namespace {
    use App\Models\Plugin;
    use App\Services\Plugin\PluginManager;
    use Illuminate\Container\Container;
    use Illuminate\Database\Capsule\Manager as Capsule;
    use Illuminate\Events\Dispatcher;
    use Illuminate\Filesystem\Filesystem;
    use Illuminate\Http\JsonResponse;
    use Illuminate\Http\Request;
    use Illuminate\Support\Facades\Facade;
    use Illuminate\Translation\ArrayLoader;
    use Illuminate\Translation\Translator;
    use Illuminate\Validation\Factory as Validator;
    use Plugin\AriNode\Controllers\UpdateController;
    use Plugin\AriNode\Services\PluginUpdate;
    use Symfony\Component\HttpKernel\Exception\HttpException;

    require __DIR__ . '/../AriNode/Services/PluginUpdate.php';
    require __DIR__ . '/../AriNode/Controllers/UpdateController.php';
    function check(bool $ok, string $message): void { if (!$ok) { throw new RuntimeException($message); } }
    function rejects(callable $action, string $message): void {
        try { $action(); } catch (Throwable $e) { check(str_contains($e->getMessage(), $message), 'Unexpected rejection: ' . $e->getMessage()); return; }
        throw new RuntimeException('Expected rejection: ' . $message);
    }
    function abort_if($condition, $code, $message): void { if ($condition) { throw new HttpException($code, $message); } }
    function response() { return new class { public function json($data, $code = 200) { return new JsonResponse($data, $code); } }; }
    class TestUpdater extends PluginUpdate {
        public string $path; public array $responses = []; public array $urls = []; public bool $cacheFailure = false;
        protected function target(): string { return $this->path; }
        protected function download(string $url, int $limit): string {
            $this->urls[] = $url;
            if (!isset($this->responses[$url])) { throw new RuntimeException('simulated network failure'); }
            return $this->responses[$url];
        }
        protected function clearCaches(): void { if ($this->cacheFailure) { throw new RuntimeException('simulated cache failure'); } }
        protected function reloadWorkers(): ?string { return null; }
    }
    $container = new Container(); Container::setInstance($container); Facade::setFacadeApplication($container);
    $events = new Dispatcher($container); $capsule = new Capsule($container);
    $capsule->addConnection(['driver' => 'sqlite', 'database' => ':memory:']);
    $capsule->setEventDispatcher($events); $capsule->setAsGlobal(); $capsule->bootEloquent();
    $container->instance('db', $capsule->getDatabaseManager());
    $validator = new Validator(new Translator(new ArrayLoader(), 'en'), $container);
    Request::macro('validate', function (array $rules) use ($validator) { return $validator->make($this->all(), $rules)->validate(); });
    Capsule::schema()->create('v2_plugins', function ($table) {
        $table->increments('id'); $table->string('code'); $table->string('version'); $table->boolean('is_enabled'); $table->text('config');
    });
    $files = new Filesystem();
    $root = sys_get_temp_dir() . '/arinode-updater-test-' . bin2hex(random_bytes(6));
    $files->makeDirectory($root . '/AriNode', 0700, true);
    $manager = new PluginManager(); $manager->path = $root . '/AriNode';
    $updater = new TestUpdater($manager, $files); $updater->path = $manager->path;
    $commit = str_repeat('a', 40);
    $prefix = 'https://raw.githubusercontent.com/Foxtea267/AriNode/' . $commit . '/dist/';
    $api = 'https://api.github.com/repos/Foxtea267/AriNode/commits/main';
    function resetInstalled(): void {
        global $files, $manager, $updater;
        $files->deleteDirectory($manager->path); $files->makeDirectory($manager->path, 0700, true);
        $files->put($manager->path . '/config.json', json_encode(['code' => 'ari_node', 'version' => '0.5.0']));
        $files->put($manager->path . '/old-only.txt', 'previous version');
        Plugin::query()->delete();
        Plugin::create(['code' => 'ari_node', 'version' => '0.5.0', 'is_enabled' => true, 'config' => '{"original":"keep-me"}']);
        $manager->fail = false; $updater->cacheFailure = false;
    }
    function package(array $extra = [], string $code = 'ari_node', string $version = '0.5.1'): string {
        global $root;
        $path = $root . '/fixture.zip'; $zip = new ZipArchive(); $zip->open($path, ZipArchive::CREATE | ZipArchive::OVERWRITE);
        $entries = ['AriNode/config.json' => json_encode(['code' => $code, 'version' => $version])];
        foreach (['Plugin.php', 'routes/api.php', 'routes/web.php', 'Controllers/AdminController.php', 'Services/PluginUpdate.php', 'resources/views/admin.blade.php'] as $name) {
            $entries['AriNode/' . $name] = '<?php // official fixture';
        }
        foreach (array_merge($entries, $extra) as $name => $content) { $zip->addFromString($name, $content); }
        $zip->close(); return file_get_contents($path);
    }
    function remote(string $zip, string $version = '0.5.1'): void {
        global $updater, $prefix, $commit, $api;
        $updater->responses = [$api => json_encode(['sha' => $commit]),
            $prefix . 'xboard-plugin-update.json' => json_encode(['code' => 'ari_node', 'version' => $version, 'sha256' => hash('sha256', $zip), 'size' => strlen($zip)]),
            $prefix . 'arinode-xboard-plugin.zip' => $zip];
    }
    function oldIntact(): void {
        global $manager;
        $row = Plugin::first();
        check($row->version === '0.5.0' && $row->is_enabled && $row->config === '{"original":"keep-me"}', 'Plugin DB state/config not preserved');
        check(file_get_contents($manager->path . '/old-only.txt') === 'previous version', 'Old plugin files not preserved');
    }
    try {
        resetInstalled(); remote(package());
        $info = $updater->check(); check($info['available'] && $info['current_version'] === '0.5.0' && $info['latest_version'] === '0.5.1', 'Version check incorrect');
        $controller = new UpdateController();
        $controller->disabled = true;
        rejects(fn() => $controller->check($updater), 'disabled');
        rejects(fn() => $controller->install(Request::create('/', 'POST'), $updater), 'disabled');
        $controller->disabled = false;
        rejects(fn() => $controller->install(Request::create('/', 'POST', ['version' => 'https://evil.test', 'commit' => '../main']), $updater), 'validation.regex');
        rejects(fn() => $updater->install('0.5.1', str_repeat('b', 40)), '版本已变化'); oldIntact();
        $updater->responses[$prefix . 'arinode-xboard-plugin.zip'] .= 'tampered';
        rejects(fn() => $updater->install('0.5.1', $commit), 'SHA-256'); oldIntact();
        foreach ([['AriNode/../../escaped.php' => 'bad'], ['Elsewhere/config.json' => '{}'], ['AriNode/database/migrations/one.php' => '<?php'], ['AriNode/../../.env' => 'bad']] as $extra) {
            remote(package($extra)); rejects(fn() => $updater->install('0.5.1', $commit), isset($extra['AriNode/database/migrations/one.php']) ? '数据库迁移' : '非法路径'); oldIntact();
        }
        remote(package([], 'another_plugin')); rejects(fn() => $updater->install('0.5.1', $commit), '身份或版本'); oldIntact();
        remote(package(['AriNode/plugin.php' => '<?php'])); rejects(fn() => $updater->install('0.5.1', $commit), '重复文件'); oldIntact();
        package(['AriNode/link' => 'outside']);
        $symlinkZip = new ZipArchive(); $symlinkZip->open($root . '/fixture.zip');
        $symlinkZip->setExternalAttributesName('AriNode/link', ZipArchive::OPSYS_UNIX, 0120777 << 16); $symlinkZip->close();
        remote(file_get_contents($root . '/fixture.zip')); rejects(fn() => $updater->install('0.5.1', $commit), '符号链接'); oldIntact();
        remote(package(['AriNode/large.txt' => str_repeat('x', 16777217)]));
        rejects(fn() => $updater->install('0.5.1', $commit), '解压大小超限'); oldIntact();
        remote(package([], 'ari_node', '9.0.0')); rejects(fn() => $updater->install('0.5.1', $commit), '身份或版本'); oldIntact();
        remote('not a zip'); rejects(fn() => $updater->install('0.5.1', $commit), '打开插件 ZIP'); oldIntact();
        $zip = package(); remote($zip);
        $updater->responses[$prefix . 'xboard-plugin-update.json'] = '{';
        rejects(fn() => $updater->check(), '元数据格式'); oldIntact();
        remote($zip); unset($updater->responses[$api]); rejects(fn() => $updater->check(), 'network failure'); oldIntact();
        check($controller->check($updater)->getStatusCode() === 422, 'Network failure must return a useful check error');
        rejects(fn() => (new PluginUpdate($manager, $files))->check(), '普通插件安装');
        remote($zip);
        $lock = fopen($root . '/.arinode-updates/update.lock', 'c'); flock($lock, LOCK_EX | LOCK_NB);
        rejects(fn() => $updater->install('0.5.1', $commit), '已有插件更新任务'); oldIntact(); flock($lock, LOCK_UN); fclose($lock);
        $manager->fail = true; rejects(fn() => $updater->install('0.5.1', $commit), '已恢复旧文件'); oldIntact();
        $manager->fail = false; $updater->cacheFailure = true;
        rejects(fn() => $updater->install('0.5.1', $commit), '已恢复旧文件'); oldIntact(); $updater->cacheFailure = false;
        $response = $controller->install(Request::create('/', 'POST', ['version' => '0.5.1', 'commit' => $commit]), $updater);
        check($response->getStatusCode() === 200, 'Install endpoint failed'); $result = $response->getData(true)['data'];
        $row = Plugin::first(); check($row->version === '0.5.1' && $row->is_enabled && $row->config === '{"original":"keep-me"}', 'Successful native upgrade lost config/state');
        check($manager->calls === 3 && !file_exists($manager->path . '/old-only.txt'), 'Native upgrade not called or obsolete plugin file remains');
        check(file_get_contents($root . '/.arinode-updates/' . $result['backup'] . '/backup/old-only.txt') === 'previous version', 'Recoverable backup missing');
        check(!$updater->check()['available'], 'Already latest check incorrect');
        rejects(fn() => $updater->install('0.5.1', $commit), '已是最新版本');
        foreach ($updater->urls as $url) { check($url === $api || str_starts_with($url, $prefix), 'Fetched unpinned or foreign URL'); }
        // Exercise the exact distributable through the validator and swap path too.
        resetInstalled();
        $official = file_get_contents(__DIR__ . '/../../../dist/arinode-xboard-plugin.zip');
        $officialConfig = json_decode(file_get_contents(__DIR__ . '/../AriNode/config.json'), true);
        Plugin::first()->update(['version' => '0.0.0']);
        $files->put($manager->path . '/config.json', json_encode(['code' => 'ari_node', 'version' => '0.0.0']));
        remote($official, $officialConfig['version']);
        $updater->install($officialConfig['version'], $commit);
        check(Plugin::first()->version === $officialConfig['version'] && file_exists($manager->path . '/Controllers/UpdateController.php'), 'Official distributable could not be installed');
        echo "XBoard updater regression tests passed (ZIP validation, isolation, native boundary, rollback, locking, endpoints)\n";
    } finally { $files->deleteDirectory($root); }
}
