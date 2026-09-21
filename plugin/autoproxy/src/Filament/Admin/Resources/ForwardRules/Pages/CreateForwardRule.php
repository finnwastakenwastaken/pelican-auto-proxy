<?php

namespace Arrowtje\AutoProxy\Filament\Admin\Resources\ForwardRules\Pages;

use Arrowtje\AutoProxy\Filament\Admin\Resources\ForwardRules\ForwardRuleResource;
use Filament\Actions\Action;
use Filament\Resources\Pages\CreateRecord;

class CreateForwardRule extends CreateRecord
{
    protected static string $resource = ForwardRuleResource::class;

    /**
     * Pelican rewrites every action into an icon-only button when the admin has
     * the icon button style switched on, and Filament's own Create, Create
     * another and Cancel form actions carry no icon. Without one they render as
     * blank buttons: the admin sees no Save at all and can only submit by
     * pressing enter in a text box. Giving each an icon keeps them visible in
     * both button styles.
     *
     * These three actions must always show their text label
     * too, even with the icon-button style on: ->button() after ->icon() wins
     * because Pelican's global Action::configureUsing(fn ($a) => $a->iconButton())
     * runs during ::make(), before parent::get...FormAction() returns here.
     */
    protected function getCreateFormAction(): Action
    {
        return parent::getCreateFormAction()->icon('tabler-device-floppy')->button();
    }

    protected function getCreateAnotherFormAction(): Action
    {
        return parent::getCreateAnotherFormAction()->icon('tabler-copy-plus')->button();
    }

    protected function getCancelFormAction(): Action
    {
        return parent::getCancelFormAction()->icon('tabler-x')->button();
    }

    protected function mutateFormDataBeforeCreate(array $data): array
    {
        $data['created_by'] = user()?->id;

        return ForwardRuleResource::normaliseTarget($data);
    }

    protected function afterCreate(): void
    {
        ForwardRuleResource::sync();
    }
}
