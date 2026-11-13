<?php

use Illuminate\Support\Facades\Route;
use Plugin\AriNode\Controllers\AdminController;
use Plugin\AriNode\Controllers\NativeAssetsController;

// Public HTML shell; all data and commands require XBoard administrator auth.
Route::get('/arinode/admin', [AdminController::class, 'page']);

// Static enhancement only: commands and credentials stay in authenticated APIs.
Route::get('/arinode/native-table.js', [NativeAssetsController::class, 'script']);
Route::get('/arinode/native-table.css', [NativeAssetsController::class, 'stylesheet']);
