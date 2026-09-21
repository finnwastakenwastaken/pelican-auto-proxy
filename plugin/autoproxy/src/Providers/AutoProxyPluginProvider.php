<?php

namespace Arrowtje\AutoProxy\Providers;

use Arrowtje\AutoProxy\Console\Commands\SyncCommand;
use Illuminate\Support\Facades\Schedule;
use Illuminate\Support\ServiceProvider;

/**
 * Auto-discovered by Pelican from src/Providers.
 *
 * Views and migrations are registered by the panel's PluginService (view
 * namespace = plugin id, so templates are "autoproxy::..."), and it also loads
 * config/autoproxy.php itself. The merge below is belt and braces for the short
 * window during install when that has not happened yet.
 */
class AutoProxyPluginProvider extends ServiceProvider
{
    public function register(): void
    {
        $config = plugin_path('autoproxy', 'config', 'autoproxy.php');

        if (file_exists($config)) {
            $this->mergeConfigFrom($config, 'autoproxy');
        }
    }

    public function boot(): void
    {
        // The official image runs supercronic -> `artisan schedule:run` every minute.
        // withoutOverlapping(5): a hung run must not block the next five minutes forever.
        Schedule::command(SyncCommand::class)->everyMinute()->withoutOverlapping(5);
    }
}
