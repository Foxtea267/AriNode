<?php

namespace Plugin\AriNode\Services;

use App\Models\Server;
use InvalidArgumentException;

final class Deployment
{
    public const INSTALLER = 'https://raw.githubusercontent.com/Foxtea267/AriNode/main/scripts/install.sh';

    public static function nodeType(Server $server): string
    {
        $type = Server::normalizeType($server->type);
        if ($type === 'hysteria' && (int) ($server->protocol_settings['version'] ?? 0) === 2) {
            $type = 'hysteria2';
        }
        return $type;
    }

    public static function validateProtocol(string $type, string $core = 'sing'): void
    {
        if (!in_array($core, ['sing', 'xray'], true)) {
            throw new InvalidArgumentException('Core must be sing or xray');
        }
        if (!in_array($type, ['vmess', 'vless', 'trojan', 'shadowsocks', 'hysteria', 'hysteria2', 'tuic', 'anytls', 'mieru'], true)) {
            throw new InvalidArgumentException("Unsupported protocol {$type}");
        }
        if ($core === 'xray' && !in_array($type, ['vmess', 'vless', 'trojan', 'shadowsocks'], true)) {
            throw new InvalidArgumentException("Protocol {$type} requires the sing core");
        }
    }

    /** Quote for the target Linux shell, also when tests run on Windows. */
    private static function quote(string $value): string
    {
        if (strpbrk($value, "\0\r\n") !== false) {
            throw new InvalidArgumentException('Deployment values must not contain control characters');
        }
        return "'" . str_replace("'", "'\"'\"'", $value) . "'";
    }

    public static function command(string $panel, string $token, int $machineId, iterable $servers, string $core = 'sing', string $language = 'zh'): string
    {
        $panel = rtrim($panel, '/');
        $url = parse_url($panel);
        if (!$url || !in_array($url['scheme'] ?? '', ['http', 'https'], true) || empty($url['host']) || isset($url['user']) || isset($url['pass']) || isset($url['query']) || isset($url['fragment'])) {
            throw new InvalidArgumentException('Panel URL must be an absolute http(s) URL without credentials, query or fragment');
        }
        if ($token === '' || $machineId <= 0) {
            throw new InvalidArgumentException('Machine token and a positive machine ID are required');
        }
        if (!in_array($language, ['zh', 'en'], true)) {
            throw new InvalidArgumentException('Language must be zh or en');
        }
        $nodes = [];
        foreach ($servers as $server) {
            $type = self::nodeType($server);
            self::validateProtocol($type, $core);
            if (!$server->enabled || (int) $server->machine_id !== $machineId || (int) $server->id <= 0) {
                throw new InvalidArgumentException('Deployment nodes must be enabled and bound to this machine');
            }
            $nodes[] = $type . ':' . $server->id;
        }
        if (!$nodes) {
            throw new InvalidArgumentException('Bind at least one enabled node before deploying');
        }
        $env = [
            'ARINODE_LANGUAGE' => $language,
            'ARINODE_INSTALL_MODE' => 'fresh',
            'ARINODE_PANEL_URL' => $panel,
            'ARINODE_PANEL_TOKEN' => $token,
            'ARINODE_MACHINE_ID' => (string) $machineId,
            'ARINODE_NODES' => implode(' ', array_unique($nodes)),
            'ARINODE_CORE' => $core,
        ];
        $parts = [];
        foreach ($env as $key => $value) {
            $parts[] = $key . '=' . self::quote($value);
        }
        return 'curl -fsSL ' . self::quote(self::INSTALLER) . ' | sudo env ' . implode(' ', $parts) . ' bash';
    }
}
