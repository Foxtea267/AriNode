<?php

use Illuminate\Support\Facades\Route;
use Plugin\AriNode\Controllers\AdminController;

// Public HTML shell; all data and commands require XBoard administrator auth.
Route::get('/arinode/admin', [AdminController::class, 'page']);
