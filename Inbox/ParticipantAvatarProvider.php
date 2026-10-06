<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Inbox;

use MauticPlugin\MauticInboxBundle\Contract\ParticipantAvatarProviderInterface;
use MauticPlugin\MauticMetaBundle\Domain\AssetType;
use MauticPlugin\MauticMetaBundle\Entity\MetaConversation;
use Symfony\Component\Routing\Generator\UrlGeneratorInterface;

final class ParticipantAvatarProvider implements ParticipantAvatarProviderInterface
{
    public function __construct(private UrlGeneratorInterface $router) {}

    public function avatarUrl(MetaConversation $conversation): ?string
    {
        if (AssetType::WhatsAppQrSession !== $conversation->getAsset()->getType() || !$conversation->getId()) {
            return null;
        }

        return $this->router->generate('mautic_whatsqr_avatar', ['conversationId' => $conversation->getId()]);
    }
}
