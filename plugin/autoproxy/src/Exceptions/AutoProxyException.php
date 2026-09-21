<?php

namespace Arrowtje\AutoProxy\Exceptions;

use RuntimeException;

/**
 * Anything the agent (or the way we reach it) refused. Message is safe to show
 * to a root admin: it carries the agent's own response body.
 */
class AutoProxyException extends RuntimeException {}
