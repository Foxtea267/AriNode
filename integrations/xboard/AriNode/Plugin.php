<?php

namespace Plugin\AriNode;

use App\Services\Plugin\AbstractPlugin;
use Plugin\AriNode\Middleware\AdminPageAssets;

class Plugin extends AbstractPlugin
{
    public function boot(): void
    {
        // InitializePlugins boots us before Laravel resolves route middleware.
        // Router deduplicates this entry when Octane boots plugins each request.
        app('router')->pushMiddlewareToGroup('web', AdminPageAssets::class);
    }
}
