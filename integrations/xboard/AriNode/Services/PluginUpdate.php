<?php

namespace Plugin\AriNode\Services;

use App\Models\Plugin;
use App\Services\Plugin\PluginManager;
use Illuminate\Filesystem\Filesystem;
use Illuminate\Support\Facades\Artisan;
use Illuminate\Support\Facades\DB;
use RuntimeException;
use Throwable;
use ZipArchive;

/** Only official, checksum-verified AriNode packages; never accepts a download URL. */
class PluginUpdate
{
    private const REPO = 'https://raw.githubusercontent.com/Foxtea267/AriNode/';
    private const MAX_ZIP = 4194304;
    private const MAX_EXPANDED = 16777216;

    public function __construct(private PluginManager $manager, private Filesystem $files) {}

    protected function target(): string
    {
        $target = $this->manager->getPluginPath('ari_node');
        if (is_link($target) || realpath($target) !== realpath(dirname(__DIR__)) || $this->manager->isCorePlugin('ari_node')) {
            throw new RuntimeException('在线更新仅支持 plugins/AriNode 中的普通插件安装。');
        }
        return realpath($target);
    }

    private function current(string $target): string
    {
        $config = $this->json($this->files->get($target . '/config.json'));
        $record = Plugin::query()->where('code', 'ari_node')->first();
        if (!$record || !$record->is_enabled || ($config['version'] ?? null) !== $record->version) {
            throw new RuntimeException('请先在 XBoard 插件管理中完成当前版本升级并启用 AriNode。');
        }
        return $record->version;
    }

    protected function download(string $url, int $limit): string
    {
        if (!function_exists('curl_init')) { throw new RuntimeException('在线更新需要 PHP cURL 扩展。'); }
        $body = '';
        $curl = curl_init($url);
        curl_setopt_array($curl, [
            CURLOPT_FOLLOWLOCATION => false, CURLOPT_PROTOCOLS => CURLPROTO_HTTPS,
            CURLOPT_SSL_VERIFYPEER => true, CURLOPT_SSL_VERIFYHOST => 2,
            CURLOPT_CONNECTTIMEOUT => 5, CURLOPT_TIMEOUT => 20,
            CURLOPT_HTTPHEADER => ['Accept: application/vnd.github+json', 'User-Agent: AriNode-XBoard-Updater'],
            CURLOPT_WRITEFUNCTION => static function ($handle, string $chunk) use (&$body, $limit): int {
                if (strlen($body) + strlen($chunk) > $limit) { return 0; }
                $body .= $chunk; return strlen($chunk);
            },
        ]);
        $ok = curl_exec($curl); $status = curl_getinfo($curl, CURLINFO_HTTP_CODE); curl_close($curl);
        if ($ok === false || $status !== 200) {
            throw new RuntimeException('无法从 GitHub 获取更新（网络、限流或包大小错误），请稍后重试。');
        }
        return $body;
    }

    private function json(string $body): array
    {
        try { $value = json_decode($body, true, 32, JSON_THROW_ON_ERROR); }
        catch (Throwable $e) { throw new RuntimeException('插件更新元数据格式错误。', 0, $e); }
        if (!is_array($value)) { throw new RuntimeException('插件更新元数据格式错误。'); }
        return $value;
    }

    private function latest(): array
    {
        $commit = $this->json($this->download('https://api.github.com/repos/Foxtea267/AriNode/commits/main', 262144));
        $sha = $commit['sha'] ?? '';
        if (!is_string($sha) || !preg_match('/^[a-f0-9]{40}$/D', $sha)) { throw new RuntimeException('GitHub 提交信息无效。'); }
        // Both metadata and ZIP come from the same immutable commit, avoiding main publication races.
        $manifest = $this->json($this->download(self::REPO . $sha . '/dist/xboard-plugin-update.json', 16384));
        if (($manifest['code'] ?? null) !== 'ari_node' || !is_string($manifest['version'] ?? null)
            || !preg_match('/^\d+\.\d+\.\d+$/D', $manifest['version'])
            || !is_string($manifest['sha256'] ?? null) || !preg_match('/^[a-f0-9]{64}$/D', $manifest['sha256'])
            || !is_int($manifest['size'] ?? null) || $manifest['size'] < 1 || $manifest['size'] > self::MAX_ZIP) {
            throw new RuntimeException('官方插件更新清单无效。');
        }
        return ['commit' => $sha, 'version' => $manifest['version'], 'sha256' => $manifest['sha256'], 'size' => $manifest['size']];
    }

    public function check(): array
    {
        $target = $this->target(); $current = $this->current($target); $latest = $this->latest();
        return ['current_version' => $current, 'latest_version' => $latest['version'], 'commit' => $latest['commit'],
            'available' => version_compare($latest['version'], $current, '>'), 'source' => 'Foxtea267/AriNode · main'];
    }

    private function unpack(string $zipPath, string $stage, string $version): void
    {
        $zip = new ZipArchive();
        if ($zip->open($zipPath) !== true) { throw new RuntimeException('无法打开插件 ZIP。'); }
        try {
            if ($zip->numFiles > 256) { throw new RuntimeException('插件包文件数量异常。'); }
            $total = 0; $seen = [];
            for ($i = 0; $i < $zip->numFiles; $i++) {
                $stat = $zip->statIndex($i); $name = $stat['name'];
                if (!preg_match('~^AriNode/(?:[A-Za-z0-9_-]+/)*[A-Za-z0-9_.-]*/?$~D', $name)
                    || str_contains($name, '..') || isset($seen[strtolower($name)])) {
                    throw new RuntimeException('插件包包含非法路径或重复文件。');
                }
                $seen[strtolower($name)] = true;
                $zip->getExternalAttributesIndex($i, $opsys, $attrs);
                if (($attrs >> 16 & 0170000) === 0120000) { throw new RuntimeException('插件包不能包含符号链接。'); }
                $total += $stat['size'];
                if ($total > self::MAX_EXPANDED) { throw new RuntimeException('插件解压大小超限。'); }
                // Schema migrations may not be transactionally reversible (e.g. MySQL DDL).
                if (str_starts_with($name, 'AriNode/database/')) { throw new RuntimeException('此版本包含数据库迁移，请通过 XBoard 插件管理手动升级。'); }
            }
            $config = $this->json((string) $zip->getFromName('AriNode/config.json'));
            if (($config['code'] ?? null) !== 'ari_node' || ($config['version'] ?? null) !== $version) {
                throw new RuntimeException('插件包身份或版本与更新清单不符。');
            }
            foreach (['Plugin.php', 'routes/api.php', 'routes/web.php', 'Controllers/AdminController.php', 'Services/PluginUpdate.php', 'resources/views/admin.blade.php'] as $file) {
                if ($zip->locateName('AriNode/' . $file) === false) { throw new RuntimeException('插件包缺少必要文件。'); }
            }
            if (!$zip->extractTo($stage)) { throw new RuntimeException('插件包解压失败。'); }
        } finally { $zip->close(); }
    }

    protected function clearCaches(): void
    {
        foreach (['view:clear', 'route:clear'] as $command) {
            if (Artisan::call($command) !== 0) { throw new RuntimeException('无法清理 XBoard 缓存。'); }
        }
    }

    private function invalidate(string $target): void
    {
        clearstatcache(true);
        if (!function_exists('opcache_invalidate')) { return; }
        foreach ($this->files->allFiles($target) as $file) {
            if ($file->getExtension() === 'php') { opcache_invalidate($file->getPathname(), true); }
        }
    }

    protected function reloadWorkers(): ?string
    {
        $state = config('octane.state_file') ?: storage_path('logs/octane-server-state.json');
        if (!is_file($state)) { return null; }
        // Octane's reload is graceful: active requests finish before workers exit.
        try { if (Artisan::call('octane:reload') === 0) { return null; } }
        catch (Throwable $e) { /* Files and database are already committed. */ }
        return '插件已更新，但 Octane 工作进程未能自动重载，请执行 php artisan octane:reload。';
    }

    public function install(string $expectedVersion, string $expectedCommit): array
    {
        if (!class_exists(ZipArchive::class)) { throw new RuntimeException('在线更新需要 PHP ZIP 扩展。'); }
        $target = $this->target();
        $root = dirname($target) . '/.arinode-updates';
        if (is_link($root)) { throw new RuntimeException('插件更新目录不能为符号链接。'); }
        $this->files->ensureDirectoryExists($root, 0700);
        $lock = fopen($root . '/update.lock', 'c');
        if (!$lock) { throw new RuntimeException('插件目录不可写，请检查 PHP 运行用户的权限。'); }
        if (!flock($lock, LOCK_EX | LOCK_NB)) { fclose($lock); throw new RuntimeException('已有插件更新任务正在执行，请稍后重试。'); }
        $work = null; $swapped = false; $backedUp = false; $success = false;
        try {
            $current = $this->current($target); $latest = $this->latest();
            if ($latest['version'] !== $expectedVersion || $latest['commit'] !== $expectedCommit) {
                throw new RuntimeException('官方版本已变化，请重新检查更新后再安装。');
            }
            if (!version_compare($latest['version'], $current, '>')) { throw new RuntimeException('当前插件已是最新版本，无需更新。'); }
            $body = $this->download(self::REPO . $latest['commit'] . '/dist/arinode-xboard-plugin.zip', self::MAX_ZIP);
            if (strlen($body) !== $latest['size'] || !hash_equals($latest['sha256'], hash('sha256', $body))) {
                throw new RuntimeException('插件包 SHA-256 校验失败，未修改当前插件。');
            }
            $id = gmdate('Ymd-His') . '-' . bin2hex(random_bytes(6));
            $work = $root . '/' . $id; $this->files->makeDirectory($work, 0700);
            $this->files->put($work . '/package.zip', $body);
            $this->unpack($work . '/package.zip', $work . '/stage', $latest['version']);
            $this->files->put($work . '/backup.json', json_encode(['previous_version' => $current, 'new_version' => $latest['version'], 'commit' => $latest['commit']], JSON_PRETTY_PRINT));
            DB::beginTransaction();
            try {
                if (!rename($target, $work . '/backup')) { throw new RuntimeException('无法备份插件目录。'); }
                $backedUp = true;
                if (!rename($work . '/stage/AriNode', $target)) { throw new RuntimeException('无法替换插件目录。'); }
                $swapped = true; $this->invalidate($target);
                if (!$this->manager->update('ari_node')) { throw new RuntimeException('XBoard 未完成插件升级。'); }
                $this->clearCaches();
                DB::commit(); $success = true;
            } catch (Throwable $e) {
                if (DB::transactionLevel() > 0) { DB::rollBack(); }
                if ($swapped && !rename($target, $work . '/failed')) {
                    throw new RuntimeException('升级失败且无法移走新目录，旧版备份保存在 ' . $work . '/backup，请手动恢复。', 0, $e);
                }
                if ($backedUp && !rename($work . '/backup', $target)) {
                    throw new RuntimeException('升级失败且自动恢复受阻，旧版备份保存在 ' . $work . '/backup，请手动恢复。', 0, $e);
                }
                $backedUp = false; $this->invalidate($target);
                throw new RuntimeException('插件更新失败，已恢复旧文件和插件状态：' . $e->getMessage(), 0, $e);
            }
            return ['version' => $latest['version'], 'backup' => $id, 'message' => '插件更新成功，请刷新页面。', 'warning' => $this->reloadWorkers()];
        } finally {
            // Keep the previous plugin on success, or a recovery backup if rollback was obstructed.
            try {
                if ($work && !$success && !$backedUp) { $this->files->deleteDirectory($work); }
                if ($success) { $this->files->delete($work . '/package.zip'); $this->files->deleteDirectory($work . '/stage'); }
            } finally { flock($lock, LOCK_UN); fclose($lock); }
        }
    }
}
