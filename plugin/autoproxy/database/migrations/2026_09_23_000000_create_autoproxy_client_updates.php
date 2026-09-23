<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * The admin's per-client "allow remote updates" switch (0.3.0). Keyed by the
 * VPS peer id rather than the Pelican node, because a tunnel client is one
 * machine: a node's own client in real mode, or a LAN client in site mode that
 * no node owns. Default off. The version the client runs and the version it was
 * asked to install live on the VPS agent, which is the only side a client can
 * talk to; this table holds nothing but the switch.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('autoproxy_client_updates', function (Blueprint $table) {
            $table->id();
            $table->string('peer_id')->unique();
            $table->boolean('remote_updates')->default(false);
            $table->timestamps();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('autoproxy_client_updates');
    }
};
