<?php

use Illuminate\Support\Facades\Route;
use Plugin\AriNode\Controllers\ProvisionController;

Route::middleware('admin')->post('/api/v1/arinode/provision', [ProvisionController::class, 'provision']);
