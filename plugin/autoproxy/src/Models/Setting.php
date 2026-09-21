<?php

namespace Arrowtje\AutoProxy\Models;

use Illuminate\Database\Eloquent\Model;

/**
 * @property int $id
 * @property string $key
 * @property string|null $value
 * @property string|null $encrypted_value
 */
class Setting extends Model
{
    protected $table = 'autoproxy_settings';

    protected $fillable = ['key', 'value', 'encrypted_value'];

    protected function casts(): array
    {
        return [
            'encrypted_value' => 'encrypted',
        ];
    }
}
