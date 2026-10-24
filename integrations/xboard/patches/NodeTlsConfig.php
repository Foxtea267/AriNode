<?php

namespace App\Support;

use InvalidArgumentException;

/** Small companion for XBoard 4f48e61; installed by the adjacent patch. */
class NodeTlsConfig
{
    public static function stable(array $value): array
    {
        // Do not sort ordered route/padding lists or strip protocol fields.
        foreach ($value as $key => $item) {
            if ($item instanceof \JsonSerializable) { $item = $item->jsonSerialize(); }
            if (is_array($item)) { $value[$key] = self::stable($item); }
        }
        if (!array_is_list($value)) { ksort($value, SORT_STRING); }
        return $value;
    }

    public static function validateContent(?array $config, ?string $serverName): void
    {
        if (!$config || strtolower((string) ($config['cert_mode'] ?? $config['mode'] ?? '')) !== 'content') { return; }
        $pem = $config['cert_content'] ?? ''; $key = $config['key_content'] ?? '';
        if (!is_string($pem) || !is_string($key) || $pem === '' || $key === '' || strlen($pem) > 2097152 || strlen($key) > 131072) {
            throw new InvalidArgumentException('content 模式需要有效的 cert_content 和 key_content，且不能超过大小限制。');
        }
        $cert = @openssl_x509_read($pem);
        if (!$cert || !@openssl_x509_check_private_key($cert, $key)) {
            throw new InvalidArgumentException('TLS 证书无法解析或与私钥不匹配。');
        }
        $details = openssl_x509_parse($cert);
        if (!$details || time() < ($details['validFrom_time_t'] ?? PHP_INT_MAX) || time() >= ($details['validTo_time_t'] ?? 0)) {
            throw new InvalidArgumentException('TLS 证书尚未生效或已经过期。');
        }
        if ($serverName === null || trim($serverName) === '') { return; }
        $name = strtolower(rtrim(trim($serverName), '.'));
        if (str_contains($name, '*')) { throw new InvalidArgumentException('TLS server_name 必须是具体名称，不能含通配符；请使用证书 SAN 覆盖的名字。'); }
        $sans = preg_split('/\s*,\s*/', $details['extensions']['subjectAltName'] ?? '');
        foreach ($sans as $san) {
            if (str_starts_with($san, 'IP Address:') && filter_var($name, FILTER_VALIDATE_IP)) {
                if (@inet_pton(substr($san, 11)) === @inet_pton($name)) { return; }
            }
            if (!str_starts_with($san, 'DNS:') || filter_var($name, FILTER_VALIDATE_IP)) { continue; }
            $dns = strtolower(rtrim(substr($san, 4), '.'));
            if ($name === $dns) { return; }
            // RFC hostname wildcard: exactly one leftmost label, never a suffix
            // match covering jp1.standard.novonet.node.example.com.
            if (str_starts_with($dns, '*.')) {
                $suffix = substr($dns, 1);
                if (str_ends_with($name, $suffix)) {
                    $label = substr($name, 0, -strlen($suffix));
                    if ($label !== '' && !str_contains($label, '.')) { return; }
                }
            }
        }
        throw new InvalidArgumentException('TLS server_name 不在证书 SAN 内；通配符仅覆盖一级子域名。请修改 SNI 或更换证书。');
    }
}
