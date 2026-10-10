<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Domain;

use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticMetaBundle\Entity\MetaMessage;

/** Content-only repair: never changes delivery, timestamps, contact or assignment. */
final class MessageContentRecovery
{
    public static function apply(MetaMessage $existing, MetaAsset $asset, array $inbound, string $recipient, bool $fromMe): bool
    {
        if ('unsupported' !== $existing->getMessageType()
            || $existing->getAsset()->getExternalId() !== $asset->getExternalId()
            || $existing->getRecipient() !== $recipient
            || $existing->getDirection() !== ($fromMe ? 'outbound' : 'inbound')
            || !MessageContent::isRecoverable($inbound)) { return false; }

        $payload = $existing->getPayload();
        // Recover only records made by this connector, never official/API sends.
        if (!is_array($payload['whatsqr'] ?? null)
            || ($payload['message']['id'] ?? null) !== ($inbound['id'] ?? null)
            || 'view_once' === ($payload['whatsqr']['unsupported_reason'] ?? null)) { return false; }

        $attachment = AttachmentReference::fromInbound($inbound);
        $type = MessageContent::type($inbound);
        $text = is_string($inbound['text'] ?? null) ? $inbound['text'] : '';
        $payload['message']['text'] = $text;
        $payload['message']['unsupported'] = false;
        $payload['message']['content_type'] = $attachment?->type ?? $type;
        unset($payload['message']['unsupported_reason']);
        if (null !== $attachment) {
            // Copy the validated public reference, not unknown incoming fields.
            $payload['message']['attachment'] = ['id' => $attachment->id, 'type' => $attachment->type, 'filename' => $attachment->filename, 'file_size' => $attachment->size];
            $payload['message'][$attachment->type] = ['id' => $attachment->id, 'filename' => $attachment->filename, 'caption' => $text, 'file_size' => $attachment->size];
        }
        $payload['whatsqr']['content_type'] = $attachment?->type ?? $type;
        $payload['whatsqr']['unsupported_reason'] = null;
        $payload['whatsqr']['content_recovered'] = true;
        $existing->setPayload($payload)->setMessageType($attachment?->type ?? (in_array($type, MessageContent::SUMMARIES, true) ? $type : 'text'))->setDateModified(new \DateTimeImmutable());
        return true;
    }
}
