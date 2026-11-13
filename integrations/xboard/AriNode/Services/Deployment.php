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

    public static function normalizePanelUrl(string $panel, bool $requireHttps = true): string
    {
        $panel = trim($panel);
        if (preg_match('/[\x00-\x20\x7f\\\\]/', $panel)) {
            throw new InvalidArgumentException('API 通讯地址不能包含空白、控制字符或反斜杠。');
        }
        $panel = rtrim($panel, '/');
        $url = parse_url($panel);
        $scheme = strtolower($url['scheme'] ?? '');
        $schemes = $requireHttps ? ['https'] : ['http', 'https'];
        if (!$url || !in_array($scheme, $schemes, true) || empty($url['host']) || isset($url['user']) || isset($url['pass']) || isset($url['query']) || isset($url['fragment']) || (isset($url['port']) && $url['port'] < 1)) {
            throw new InvalidArgumentException('API 通讯地址必须是完整的 ' . ($requireHttps ? 'HTTPS' : 'HTTP(S)') . ' 地址，且不能包含账号、密码、查询参数或片段。');
        }
        if (preg_match('~/api(?:/v[0-9]+)?$~i', $url['path'] ?? '')) {
            throw new InvalidArgumentException('请填写 API 服务根地址，例如 https://api.example.com，不要附加 /api 或 /api/v1。');
        }
        $host = strtolower($url['host']);
        $hostname = trim($host, '[]');
        if (!filter_var($hostname, FILTER_VALIDATE_IP)
            && !filter_var($hostname, FILTER_VALIDATE_DOMAIN, FILTER_FLAG_HOSTNAME)) {
            throw new InvalidArgumentException('API 通讯地址中的主机名无效。');
        }
        return $scheme . '://' . $host . (isset($url['port']) ? ':' . $url['port'] : '') . ($url['path'] ?? '');
    }

    public static function command(string $panel, string $token, int $machineId, iterable $servers, string $core = 'sing', bool $multiNode = true, bool $multiPanel = true): string
    {
        $panel = self::normalizePanelUrl($panel);
        if ($token === '' || strpbrk($token, "\0\r\n") !== false || $machineId <= 0) {
            throw new InvalidArgumentException('Machine token and a positive machine ID are required');
        }
        self::validateProtocol('vless', $core);
        foreach ($servers as $server) {
            $type = self::nodeType($server);
            self::validateProtocol($type, $core);
            if (!$server->enabled || (int) $server->machine_id !== $machineId || (int) $server->id <= 0) {
                throw new InvalidArgumentException('Deployment nodes must be enabled and bound to this machine');
            }
        }
        $code = self::issue($machineId, $token, $core, $multiNode, $multiPanel);
        return 'curl -fsSL ' . self::quote(self::INSTALLER) . ' | sudo bash -s -- --enroll ' . self::quote($panel) . ' ' . self::quote($code);
    }

    private static function issue(int $id, string $token, string $core, bool $multiNode, bool $multiPanel): string
    {
        $payload = ['id' => $id, 'exp' => time() + 1800, 'key' => self::credentialFingerprint($token),
            'core' => $core, 'multi_node' => $multiNode, 'multi_panel' => $multiPanel];
        $body = rtrim(strtr(base64_encode(json_encode($payload, JSON_THROW_ON_ERROR)), '+/', '-_'), '=');
        return $body . '.' . self::signature($body);
    }

    public static function verify(string $code): array
    {
        if (!preg_match('/\A([A-Za-z0-9_-]{1,950})\.([a-f0-9]{64})\z/', $code, $parts)
            || !hash_equals(self::signature($parts[1]), $parts[2])) {
            throw new InvalidArgumentException('Invalid enrollment code');
        }
        $payload = json_decode(base64_decode(strtr($parts[1], '-_', '+/'), true) ?: '', true);
        if (!is_array($payload) || !is_int($payload['id'] ?? null) || $payload['id'] < 1
            || !is_int($payload['exp'] ?? null) || $payload['exp'] <= time()
            || !is_string($payload['key'] ?? null) || !in_array($payload['core'] ?? null, ['sing', 'xray'], true)
            || !is_bool($payload['multi_node'] ?? null) || !is_bool($payload['multi_panel'] ?? null)) {
            throw new InvalidArgumentException('Invalid or expired enrollment code');
        }
        return $payload;
    }

    private static function signature(string $body): string
    {
        return self::sign('arinode-enroll-v1:' . $body);
    }

    public static function credentialFingerprint(string $token): string
    {
        return self::sign('arinode-machine-token-v1:' . $token);
    }

    private static function sign(string $value): string
    {
        $key = (string) config('app.key');
        if ($key === '') {
            throw new InvalidArgumentException('XBoard application key is required');
        }
        return hash_hmac('sha256', $value, $key);
    }
}
