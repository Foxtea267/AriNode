<?php

use Illuminate\Support\Facades\Route;
use Plugin\AriNode\Controllers\ProvisionController;
use Plugin\AriNode\Controllers\ClusterController;

Route::middleware('admin')->post('/api/v1/arinode/provision', [ProvisionController::class, 'provision']);
Route::middleware('admin')->get('/api/v1/arinode/cluster/members', [ClusterController::class, 'members']);
Route::middleware('server.v2')->group(function () {
    Route::get('/api/v1/arinode/cluster/info', [ClusterController::class, 'info']);
    Route::get('/api/v1/arinode/cluster/alivelist', [ClusterController::class, 'alivelist']);
    Route::post('/api/v1/arinode/cluster/report', [ClusterController::class, 'report']);
});
