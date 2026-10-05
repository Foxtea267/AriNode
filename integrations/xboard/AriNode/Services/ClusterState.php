<?php

namespace Plugin\AriNode\Services;

/** Pure member snapshot merging; persistence and authorization stay in the controller. */
final class ClusterState
{
    public static function prune(array $state, int $now, int $ttl): array
    {
        $state['members'] ??= [];
        foreach ($state['members'] as $id => $member) {
            if ($now - (int) $member['seen_at'] >= $ttl) {
                unset($state['members'][$id]);
            }
        }
        return $state;
    }

    public static function record(array $state, string $member, array $alive, ?array $status, int $now, int $ttl): array
    {
        $state = self::prune($state, $now, $ttl);
        if (!isset($state['members'][$member]) && count($state['members']) >= 128) {
            throw new \RuntimeException('A node group supports at most 128 active members');
        }
        $state['members'][$member] = [
            'seen_at' => $now,
            'alive' => $alive,
            'status' => $status ?? ($state['members'][$member]['status'] ?? null),
        ];
        return $state;
    }

    public static function aggregate(array $state): array
    {
        $alive = [];
        $status = ['cpu' => 0.0, 'mem' => ['total' => 0, 'used' => 0],
            'swap' => ['total' => 0, 'used' => 0], 'disk' => ['total' => 0, 'used' => 0]];
        $count = 0;
        foreach ($state['members'] ?? [] as $member) {
            foreach ($member['alive'] as $uid => $ips) {
                $alive[$uid] = array_values(array_unique(array_merge($alive[$uid] ?? [], $ips)));
            }
            if (is_array($member['status'])) {
                ++$count;
                $status['cpu'] += (float) ($member['status']['cpu'] ?? 0);
                foreach (['mem', 'swap', 'disk'] as $resource) {
                    foreach (['total', 'used'] as $metric) {
                        $status[$resource][$metric] += (int) ($member['status'][$resource][$metric] ?? 0);
                    }
                }
            }
        }
        $status['cpu'] = $count ? $status['cpu'] / $count : 0.0;
        $status['kernel_status'] = count($state['members'] ?? []) > 0;
        return ['alive' => $alive, 'status' => $status];
    }

    public static function summary(array $state): array
    {
        $members = [];
        foreach ($state['members'] ?? [] as $id => $member) {
            $members[] = ['member_id' => (string) $id, 'seen_at' => $member['seen_at'],
                'online_users' => count($member['alive']), 'status' => $member['status']];
        }
        return $members;
    }
}
