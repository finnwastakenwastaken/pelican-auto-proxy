<?php

namespace Arrowtje\AutoProxy\Filament\Admin\Resources\ForwardRules;

use Arrowtje\AutoProxy\Exceptions\AutoProxyException;
use Arrowtje\AutoProxy\Filament\Admin\Resources\ForwardRules\Pages\CreateForwardRule;
use Arrowtje\AutoProxy\Filament\Admin\Resources\ForwardRules\Pages\EditForwardRule;
use Arrowtje\AutoProxy\Filament\Admin\Resources\ForwardRules\Pages\ListForwardRules;
use Arrowtje\AutoProxy\Models\ForwardRule;
use Arrowtje\AutoProxy\Services\AgentClient;
use Arrowtje\AutoProxy\Services\SyncService;
use Arrowtje\AutoProxy\Support\ForwardRuleInput;
use BackedEnum;
use Closure;
use Filament\Actions\BulkActionGroup;
use Filament\Actions\DeleteAction;
use Filament\Actions\DeleteBulkAction;
use Filament\Actions\EditAction;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\Textarea;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Resources\Resource;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Components\Utilities\Get;
use Filament\Schemas\Schema;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Columns\ToggleColumn;
use Filament\Tables\Table;
use Illuminate\Database\Eloquent\Model;
use Throwable;

/**
 * Forwards for things that are not Pelican allocations: the panel itself, SFTP,
 * a service on some other machine. Pelican allocations are handled by the alias,
 * never here.
 */
class ForwardRuleResource extends Resource
{
    protected static ?string $model = ForwardRule::class;

    protected static string|BackedEnum|null $navigationIcon = 'tabler-arrows-right-left';

    protected static ?string $recordTitleAttribute = 'name';

    protected static ?int $navigationSort = 3;

    public static function getNavigationLabel(): string
    {
        return 'Forwards';
    }

    public static function getNavigationGroup(): ?string
    {
        return 'Auto Proxy';
    }

    public static function getModelLabel(): string
    {
        return 'manual forward';
    }

    public static function getPluralModelLabel(): string
    {
        return 'manual forwards';
    }

    /**
     * The tunnel clients the VPS knows, for the two pickers. Read live, because a
     * stale list would offer a client that no longer exists; an unreachable VPS
     * gives an empty list and the field says so rather than pretending.
     *
     * $mode filters by the agent's own rule, which is not symmetric:
     *   target_peer must name a REAL-IP client (the agent refuses "peer is in
     *   site mode: forward to an address on its LAN with target_ip and via_peer")
     *   via_peer must name a SITE client (the agent refuses "peer is in real-IP
     *   mode: forward to it with target_peer, not target_ip").
     * Offering the wrong half in either picker just moves the refusal to push
     * time, where the admin can no longer see which field caused it.
     *
     * @return array<string, string>
     */
    public static function peerOptions(?string $mode = null): array
    {
        try {
            $peers = app(AgentClient::class)->peers();
        } catch (AutoProxyException) {
            return [];
        } catch (Throwable) {
            return [];
        }

        $options = [];

        foreach ($peers as $peer) {
            $id = (string) ($peer['id'] ?? '');
            $peerMode = (string) ($peer['mode'] ?? '');

            if ($id === '' || ($mode !== null && $peerMode !== $mode)) {
                continue;
            }

            $options[$id] = sprintf(
                '%s (%s, %s)',
                (string) ($peer['name'] ?? $id),
                (string) ($peer['tunnel_ip'] ?? '?'),
                $peerMode === 'site' ? 'site' : 'real IPs',
            );
        }

        return $options;
    }

    /**
     * The LAN ranges each site client covers, keyed by peer id. The agent
     * refuses a target_ip outside them, so the form checks it first.
     *
     * @return array<string, string[]>
     */
    public static function peerLanCidrs(): array
    {
        try {
            $peers = app(AgentClient::class)->peers();
        } catch (AutoProxyException) {
            return [];
        } catch (Throwable) {
            return [];
        }

        $out = [];

        foreach ($peers as $peer) {
            $id = (string) ($peer['id'] ?? '');

            if ($id === '') {
                continue;
            }

            $out[$id] = array_values(array_map('strval', (array) ($peer['lan_cidrs'] ?? [])));
        }

        return $out;
    }

    public static function form(Schema $schema): Schema
    {
        return $schema->components([
            // Every section spans the whole form. A resource form is a two column
            // grid by default, so a section left at the default span sits in the
            // left half with an empty right half, which reads as an off-centre page.
            Section::make('Forward')
                ->description('What to call it, and which port on the VPS it takes.')
                ->columns(2)
                ->columnSpanFull()
                ->schema([
                    TextInput::make('name')
                        ->label('Name')
                        ->required()
                        ->maxLength(ForwardRuleInput::NAME_MAX)
                        ->helperText('Shown on the VPS as the note for this forward.'),
                    Select::make('protocol')
                        ->label('Protocol')
                        ->options(ForwardRule::PROTOCOLS)
                        ->default('both')
                        ->required(),
                    TextInput::make('public_port')
                        ->label('Public port')
                        ->numeric()
                        ->required()
                        ->minValue(ForwardRuleInput::PORT_MIN)
                        ->maxValue(ForwardRuleInput::PORT_MAX)
                        ->helperText('Port on the VPS. The VPS refuses its own SSH, WireGuard and API ports.'),
                    TextInput::make('public_port_end')
                        ->label('Public port (range end)')
                        ->numeric()
                        ->nullable()
                        ->minValue(ForwardRuleInput::PORT_MIN)
                        ->maxValue(ForwardRuleInput::PORT_MAX)
                        ->rules([
                            'nullable',
                            'integer',
                            fn (Get $get): Closure => static function (string $attribute, mixed $value, Closure $fail) use ($get): void {
                                $error = ForwardRuleInput::publicPortEndError($value, $get('public_port'));

                                if ($error !== null) {
                                    $fail($error);
                                }
                            },
                        ])
                        ->helperText('Leave empty for a single port. A range is forwarded 1:1.'),
                ]),

            Section::make('Destination')
                ->description('Where the VPS sends it. Mirrors what the VPS accepts: anything it would reject is rejected here first.')
                ->columns(2)
                ->columnSpanFull()
                ->schema([
                    Select::make('target_kind')
                        ->label('Send it to')
                        ->options([
                            ForwardRule::TARGET_PEER => 'A machine running a tunnel client (keeps real client IPs)',
                            ForwardRule::TARGET_LAN => 'An address on a LAN, reached through a tunnel client',
                        ])
                        ->default(ForwardRule::TARGET_LAN)
                        ->formatStateUsing(fn (?ForwardRule $record): string => $record?->targetsPeer() ? ForwardRule::TARGET_PEER : ForwardRule::TARGET_LAN)
                        ->dehydrated(false)
                        ->live()
                        ->required()
                        ->columnSpanFull(),

                    Select::make('target_peer')
                        ->label('Tunnel client')
                        ->options(fn () => static::peerOptions('real'))
                        ->visible(fn (Get $get): bool => $get('target_kind') === ForwardRule::TARGET_PEER)
                        ->required(fn (Get $get): bool => $get('target_kind') === ForwardRule::TARGET_PEER)
                        ->helperText('Real-IP clients only: traffic is handed straight to that machine, so it sees the real client address. Empty list means there is no real-IP client, or the VPS could not be reached.'),

                    TextInput::make('target_ip')
                        ->label('Target IP on the LAN')
                        ->maxLength(45)
                        ->placeholder('10.0.0.10')
                        ->visible(fn (Get $get): bool => $get('target_kind') === ForwardRule::TARGET_LAN)
                        ->required(fn (Get $get): bool => $get('target_kind') === ForwardRule::TARGET_LAN)
                        ->rule(static function (Get $get): Closure {
                            // Both halves of this test live in ForwardRuleInput so
                            // `autoproxy:setup add-forward` refuses the same input
                            // with the same sentence.
                            return static function (string $attribute, mixed $value, Closure $fail) use ($get): void {
                                if ($get('target_kind') !== ForwardRule::TARGET_LAN) {
                                    return;
                                }

                                $via = (string) ($get('via_peer') ?? '');
                                $cidrs = $via === '' ? [] : (static::peerLanCidrs()[$via] ?? []);
                                $error = ForwardRuleInput::targetIpError(is_string($value) ? $value : null, $cidrs);

                                if ($error !== null) {
                                    $fail($error);
                                }
                            };
                        })
                        ->helperText('RFC1918 only. A public target would turn your VPS into an open relay.'),

                    Select::make('via_peer')
                        ->label('Reached through')
                        ->options(fn () => static::peerOptions('site'))
                        ->live()
                        ->visible(fn (Get $get): bool => $get('target_kind') === ForwardRule::TARGET_LAN)
                        ->required(fn (Get $get): bool => $get('target_kind') === ForwardRule::TARGET_LAN)
                        ->helperText('Site clients only: which tunnel client sits on that LAN. The target shares that client\'s address, so the service sees one IP for everybody. Empty list means there is no site client yet.'),

                    TextInput::make('target_port')
                        ->label('Target port')
                        ->numeric()
                        ->nullable()
                        ->minValue(ForwardRuleInput::PORT_MIN)
                        ->maxValue(ForwardRuleInput::PORT_MAX)
                        ->disabled(fn (Get $get): bool => filled($get('public_port_end')))
                        ->dehydrated()
                        ->rules([
                            fn (Get $get): Closure => static function (string $attribute, mixed $value, Closure $fail) use ($get): void {
                                $error = ForwardRuleInput::targetPortError($value, $get('public_port_end'));

                                if ($error !== null) {
                                    $fail($error);
                                }
                            },
                        ])
                        ->helperText('Empty = same port as the public port. Not allowed on ranges.'),
                ]),

            Section::make('Options')
                ->columns(2)
                ->columnSpanFull()
                ->schema([
                    Toggle::make('enabled')
                        ->label('Enabled')
                        ->default(true)
                        ->helperText('Disabled forwards stay here but are not applied.'),
                    Textarea::make('notes')
                        ->label('Notes')
                        ->nullable()
                        ->rows(2)
                        ->columnSpanFull(),
                ]),
        ]);
    }

    /**
     * Exactly one target form survives a save, so a forward switched from a LAN
     * address to a tunnel client cannot keep half of its old destination.
     *
     * @param array<string, mixed> $data
     * @return array<string, mixed>
     */
    public static function normaliseTarget(array $data): array
    {
        if (filled($data['target_peer'] ?? null)) {
            $data['target_ip'] = null;
            $data['via_peer'] = null;

            return $data;
        }

        $data['target_peer'] = null;

        return $data;
    }

    public static function table(Table $table): Table
    {
        return $table
            ->columns([
                TextColumn::make('name')->label('Name')->searchable()->sortable(),
                TextColumn::make('protocol')->label('Protocol')->badge(),
                TextColumn::make('public_port')
                    ->label('Public port')
                    ->sortable()
                    ->formatStateUsing(fn (ForwardRule $record): string => $record->portLabel()),
                TextColumn::make('target_ip')
                    ->label('Target')
                    ->formatStateUsing(fn (ForwardRule $record): string => $record->targetLabel()),
                ToggleColumn::make('enabled')
                    ->label('Enabled')
                    ->afterStateUpdated(fn () => static::sync()),
                TextColumn::make('updated_at')->label('Changed')->since()->sortable()->toggleable(),
            ])
            ->defaultSort('public_port')
            ->recordActions([
                EditAction::make(),
                DeleteAction::make()->after(fn () => static::sync()),
            ])
            ->toolbarActions([
                BulkActionGroup::make([
                    // Bulk delete runs as a query and fires no model events; the
                    // explicit sync here is the fast path, the minute reconcile is the truth.
                    DeleteBulkAction::make()->after(fn () => static::sync()),
                ]),
            ])
            ->emptyStateHeading('No manual forwards')
            ->emptyStateDescription('Pelican allocations with the public alias are forwarded automatically; this list is for everything else.');
    }

    /**
     * Push straight away so the admin sees the result, rather than waiting up to a
     * minute for the scheduler. Failures are recorded in the sync state, not thrown.
     */
    public static function sync(): void
    {
        app(SyncService::class)->run();
    }

    public static function getPages(): array
    {
        return [
            'index' => ListForwardRules::route('/'),
            'create' => CreateForwardRule::route('/create'),
            'edit' => EditForwardRule::route('/{record}/edit'),
        ];
    }

    protected static function isRootAdmin(): bool
    {
        return user()?->isRootAdmin() ?? false;
    }

    public static function canAccess(): bool
    {
        return static::isRootAdmin();
    }

    public static function canViewAny(): bool
    {
        return static::isRootAdmin();
    }

    public static function canCreate(): bool
    {
        return static::isRootAdmin();
    }

    public static function canEdit(Model $record): bool
    {
        return static::isRootAdmin();
    }

    public static function canDelete(Model $record): bool
    {
        return static::isRootAdmin();
    }

    public static function canDeleteAny(): bool
    {
        return static::isRootAdmin();
    }

    public static function canView(Model $record): bool
    {
        return static::isRootAdmin();
    }
}
