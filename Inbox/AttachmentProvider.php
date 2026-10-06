<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Inbox;

use MauticPlugin\MauticInboxBundle\Contract\AttachmentProviderInterface;
use MauticPlugin\MauticMetaBundle\Domain\AssetType;
use MauticPlugin\MauticMetaBundle\Entity\MetaMessage;
use MauticPlugin\MauticWhatsQrBundle\Domain\AttachmentReference;
use Symfony\Component\Routing\Generator\UrlGeneratorInterface;

final class AttachmentProvider implements AttachmentProviderInterface
{
    public function __construct(private UrlGeneratorInterface $urls) {}

    public function attachmentUrl(MetaMessage $message, string $type): ?string
    {
        if (null === $message->getId() || 'inbound' !== $message->getDirection() || AssetType::WhatsAppQrSession !== $message->getAsset()->getType()) { return null; }
        $reference = AttachmentReference::fromInbound($message->getPayload()['message'] ?? []);
        if (null === $reference || '' === $reference->id || $reference->type !== $type) { return null; }

        return $this->urls->generate('mautic_whatsqr_media', ['messageId' => $message->getId()], UrlGeneratorInterface::ABSOLUTE_URL);
    }
}
