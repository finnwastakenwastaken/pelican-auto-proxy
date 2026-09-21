<?php

namespace Arrowtje\AutoProxy\Filament\Admin\Resources\ForwardRules\Pages;

use App\Filament\Admin\Resources\Nodes\Pages\EditNode;
use Arrowtje\AutoProxy\Filament\Admin\Resources\ForwardRules\ForwardRuleResource;
use Arrowtje\AutoProxy\Services\RuleSetBuilder;
use Arrowtje\AutoProxy\Support\AutoProxySettings;
use Filament\Actions\CreateAction;
use Filament\Resources\Pages\ListRecords;
use Illuminate\Contracts\View\View;
use Throwable;

class ListForwardRules extends ListRecords
{
    protected static string $resource = ForwardRuleResource::class;

    protected function getHeaderActions(): array
    {
        return [
            CreateAction::make(),
        ];
    }

    /**
     * Read-only list of the allocations that ask to be published right now, below
     * the manual forwards. Read-only on purpose: the alias in Pelican is the
     * source of truth, and this list exists to explain what it did.
     */
    public function getFooter(): ?View
    {
        $address = AutoProxySettings::publicAddress();
        $builder = app(RuleSetBuilder::class);
        $rows = [];

        foreach ($builder->allocationRows($address) as $row) {
            $target = $builder->targetFor($row);

            $rows[] = $row + [
                'target_label' => isset($target['reason']) ? null : $builder->targetLabel($target + ['target_port' => null]),
                'reason' => $target['reason'] ?? null,
            ];
        }

        $nodeUrls = [];
        foreach ($rows as $row) {
            $nodeId = $row['node_id'] ?? null;
            if ($nodeId !== null && !array_key_exists($nodeId, $nodeUrls)) {
                $nodeUrls[$nodeId] = $this->nodeUrl($nodeId);
            }
        }

        return view('autoproxy::filament.admin.resources.forward-rules.allocations', [
            'address' => $address,
            'rows' => $rows,
            'keywords' => AutoProxySettings::keywords(),
            'nodeUrls' => $nodeUrls,
        ]);
    }

    /**
     * The node edit page carries the Allocations tab, where the alias is typed.
     * Guarded: a panel that moves this page must not break the whole list.
     */
    protected function nodeUrl(int|string $nodeId): ?string
    {
        if (!class_exists(EditNode::class)) {
            return null;
        }

        try {
            return EditNode::getUrl(['record' => $nodeId]);
        } catch (Throwable) {
            return null;
        }
    }
}
