<?php

namespace Arrowtje\AutoProxy\Filament\Admin\Resources\ForwardRules\Pages;

use Arrowtje\AutoProxy\Filament\Admin\Resources\ForwardRules\ForwardRuleResource;
use Filament\Actions\Action;
use Filament\Actions\DeleteAction;
use Filament\Resources\Pages\EditRecord;

class EditForwardRule extends EditRecord
{
    protected static string $resource = ForwardRuleResource::class;

    protected function getHeaderActions(): array
    {
        return [
            DeleteAction::make()->after(fn () => ForwardRuleResource::sync()),
        ];
    }

    /**
     * Same reason as CreateForwardRule: Pelican's icon button style turns the
     * iconless Save and Cancel form actions into blank buttons. These
     * must always show their text label too, so ->button() is
     * chained after ->icon() to override the global iconButton() setting.
     */
    protected function getSaveFormAction(): Action
    {
        return parent::getSaveFormAction()->icon('tabler-device-floppy')->button();
    }

    protected function getCancelFormAction(): Action
    {
        return parent::getCancelFormAction()->icon('tabler-x')->button();
    }

    protected function mutateFormDataBeforeSave(array $data): array
    {
        return ForwardRuleResource::normaliseTarget($data);
    }

    protected function afterSave(): void
    {
        ForwardRuleResource::sync();
    }
}
