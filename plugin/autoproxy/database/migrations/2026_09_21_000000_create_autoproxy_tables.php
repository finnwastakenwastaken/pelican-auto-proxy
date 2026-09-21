<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * One migration, because this is a first release: there is no upgrade path from
 * the private prototype (different plugin id, different tables), so nothing has
 * ever run an older autoproxy migration. Later releases add files, never edit
 * this one - the panel's migrator tracks what it ran by filename.
 *
 * SQLite-compatible throughout: no enum columns, no json column type.
 */
return new class extends Migration
{
    public function up(): void
    {
        // Key/value settings. Secrets (the API token) go in `encrypted_value`
        // via Laravel's `encrypted` cast; everything else in `value`.
        Schema::create('autoproxy_settings', function (Blueprint $table) {
            $table->id();
            $table->string('key')->unique();
            $table->text('value')->nullable();
            $table->text('encrypted_value')->nullable();
            $table->timestamps();
        });

        // Per Pelican node: is it proxied, how, and which VPS peer belongs to it.
        Schema::create('autoproxy_node_settings', function (Blueprint $table) {
            $table->id();
            $table->unsignedInteger('node_id')->unique();
            $table->boolean('proxied')->default(false);

            // real = a tunnel client runs on this node's own machine, so players
            //        keep their real IP. site = reached over another machine's
            //        client across a LAN, shared IP.
            $table->string('mode')->default('real');

            // site mode: where on the LAN this node is, and through which peer.
            $table->string('lan_ip')->nullable();
            $table->string('via_peer_id')->nullable();

            // real mode: this node's own peer on the VPS.
            $table->string('peer_id')->nullable();

            // Shown once so the admin can copy the join command again; dropped
            // the moment the peer's first handshake proves it was used.
            $table->text('encrypted_join_code')->nullable();
            $table->timestamp('join_code_issued_at')->nullable();

            $table->timestamps();
        });

        // Manual forwards for services that are not Pelican allocations.
        Schema::create('autoproxy_rules', function (Blueprint $table) {
            $table->id();
            $table->string('name');
            $table->string('protocol')->default('both'); // tcp|udp|both
            $table->unsignedInteger('public_port');
            $table->unsignedInteger('public_port_end')->nullable();

            // Either target_peer (real-IP mode: straight to that peer) ...
            $table->string('target_peer')->nullable();
            // ... or target_ip reached through via_peer (site mode).
            $table->string('target_ip')->nullable();
            $table->string('via_peer')->nullable();

            $table->unsignedInteger('target_port')->nullable();
            $table->boolean('enabled')->default(true);
            $table->unsignedInteger('created_by')->nullable();
            $table->text('notes')->nullable();
            $table->timestamps();

            $table->index(['enabled', 'public_port']);
        });

        // Single row (id = 1) describing the last reconcile.
        Schema::create('autoproxy_sync_state', function (Blueprint $table) {
            $table->id();
            $table->timestamp('last_run_at')->nullable();
            $table->timestamp('last_success_at')->nullable();   // agent last answered
            $table->timestamp('last_pushed_at')->nullable();    // rules last accepted
            $table->text('last_error')->nullable();
            $table->string('last_pushed_hash')->nullable();
            $table->text('conflicts')->nullable();
            $table->text('node_not_configured')->nullable();
            $table->text('not_proxied')->nullable();
            $table->text('unassigned')->nullable();
            $table->text('agent_status')->nullable();
            $table->unsignedInteger('rule_count')->default(0);
            $table->unsignedInteger('allocation_rule_count')->default(0);
            $table->unsignedInteger('manual_rule_count')->default(0);
            $table->timestamps();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('autoproxy_sync_state');
        Schema::dropIfExists('autoproxy_rules');
        Schema::dropIfExists('autoproxy_node_settings');
        Schema::dropIfExists('autoproxy_settings');
    }
};
