<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Domain;

/** Safe content classification shared by new events and history enrichment. */
final class MessageContent
{
    public const TYPES = ['text', 'image', 'video', 'audio', 'document', 'sticker', 'contact', 'location', 'poll', 'interactive', 'view_once', 'unknown'];
    public const SUMMARIES = ['contact', 'location', 'poll', 'interactive'];

    public static function type(array $inbound): string
    {
        $type = $inbound['content_type'] ?? null;
        if (in_array($type, self::TYPES, true)) { return $type; }
        return true === ($inbound['unsupported'] ?? false) ? 'unknown' : 'text';
    }

    public static function reason(array $inbound): ?string
    {
        if ('view_once' === self::type($inbound) || 'view_once' === ($inbound['unsupported_reason'] ?? null)) { return 'view_once'; }
        return 'unsupported_type' === ($inbound['unsupported_reason'] ?? null) ? 'unsupported_type' : null;
    }

    public static function isRecoverable(array $inbound): bool
    {
        if ('view_once' === self::reason($inbound)) { return false; }
        $attachment = AttachmentReference::fromInbound($inbound);
        if (null !== $attachment) { return '' !== $attachment->id && null === $attachment->error; }
        return false === ($inbound['unsupported'] ?? true) && is_string($inbound['text'] ?? null) && '' !== trim($inbound['text']);
    }
}
