<?php

namespace Plugin\AriNode\Services;

use App\Models\Plugin;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\DB;

final class DeploymentSettings
{
    private static function values(?Plugin $plugin): array
    {
        return $plugin && $plugin->config ? json_decode($plugin->config, true, 32, JSON_THROW_ON_ERROR) : [];
    }

    public static function configuredUrl(): string
    {
        // Read the database for each request. XBoard's controller/worker config
        // cache may still contain the previous value after a native settings save.
        $values = self::values(Plugin::query()->where('code', 'ari_node')->first());
        return trim((string) ($values['api_url'] ?? ''));
    }

    public static function panelUrl(Request $request): string
    {
        $configured = self::configuredUrl();
        $url = $configured !== '' ? $configured : (string) (admin_setting('app_url') ?: $request->getSchemeAndHttpHost());
        // Preserve HTTP support for legacy JSON provisioning. Enrollment commands
        // and explicitly configured API endpoints retain their HTTPS requirement.
        return Deployment::normalizePanelUrl($url, $configured !== '');
    }

    public static function save(string $url): void
    {
        $url = trim($url);
        if ($url !== '') {
            $url = Deployment::normalizePanelUrl($url);
        }
        DB::transaction(function () use ($url) {
            $plugin = Plugin::query()->where('code', 'ari_node')->lockForUpdate()->firstOrFail();
            $values = self::values($plugin);
            $values['api_url'] = $url;
            $plugin->config = json_encode($values, JSON_THROW_ON_ERROR | JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE);
            $plugin->save();
        });
        // All consumers read this setting from the database, including native
        // PluginConfigService. Saving does not depend on a Redis cache eviction.
    }
}
