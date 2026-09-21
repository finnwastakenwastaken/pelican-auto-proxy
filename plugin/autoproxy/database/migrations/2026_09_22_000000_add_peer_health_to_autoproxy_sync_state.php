<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * The reconcile already asks the VPS which peers exist and how long ago each one
 * handshaked; it threw that away after using it for join codes. The dashboard
 * banner needs it to notice a stopped tunnel client, and a widget must never make
 * an HTTP call, so the snapshot is written down here instead.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('autoproxy_sync_state', function (Blueprint $table) {
            $table->text('peer_health')->nullable();
            $table->timestamp('peer_health_at')->nullable();
        });
    }

    public function down(): void
    {
        Schema::table('autoproxy_sync_state', function (Blueprint $table) {
            $table->dropColumn(['peer_health', 'peer_health_at']);
        });
    }
};
