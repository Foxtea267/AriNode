<?php

use Illuminate\Support\Facades\Route;
use Plugin\AriNode\Controllers\ProvisionController;
use Plugin\AriNode\Controllers\ClusterController;
use Plugin\AriNode\Controllers\AdminController;

Route::middleware('admin')->group(function () {
    Route::get('/api/v1/arinode/admin/entry', [AdminController::class, 'entry']);
    Route::get('/api/v1/arinode/admin/servers', [AdminController::class, 'servers']);
    Route::get('/api/v1/arinode/admin/nodes', [AdminController::class, 'nodes']);
    Route::post('/api/v1/arinode/admin/servers/save', [AdminController::class, 'save']);
    Route::post('/api/v1/arinode/admin/command', [AdminController::class, 'command']);
    // XBoard's native plugin CRUD runtime prefixes APIs with /api/v2/<secure_path>.
    $secure = admin_setting('secure_path', admin_setting('frontend_admin_path', hash('crc32b', config('app.key'))));
    $secure = trim((string) $secure, '/');
    $base = '/api/v2' . ($secure !== '' ? '/' . $secure : '') . '/plugin/ari-node';
    Route::get($base . '/entry', [AdminController::class, 'entry']);
    Route::get($base . '/servers/fetch', [AdminController::class, 'servers']);
    Route::post($base . '/servers/save', [AdminController::class, 'save']);
    Route::get($base . '/nodes/fetch', [AdminController::class, 'nodes']);
});

Route::middleware('admin')->post('/api/v1/arinode/provision', [ProvisionController::class, 'provision']);
Route::middleware('admin')->get('/api/v1/arinode/cluster/members', [ClusterController::class, 'members']);
Route::middleware('server.v2')->group(function () {
    Route::get('/api/v1/arinode/devices/alivelist', [ClusterController::class, 'devices']);
    Route::get('/api/v1/arinode/cluster/info', [ClusterController::class, 'info']);
    Route::get('/api/v1/arinode/cluster/alivelist', [ClusterController::class, 'alivelist']);
    Route::post('/api/v1/arinode/cluster/report', [ClusterController::class, 'report']);
});
