<?php

declare(strict_types=1);
namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Inbox;

use MauticPlugin\MauticMetaBundle\Domain\AssetType;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticMetaBundle\Entity\MetaConversation;
use MauticPlugin\MauticWhatsQrBundle\Inbox\ParticipantAvatarProvider;
use PHPUnit\Framework\TestCase;
use Symfony\Component\Routing\Generator\UrlGeneratorInterface;

final class ParticipantAvatarProviderTest extends TestCase
{
    public function testOnlyQrConversationsGetAProtectedLocalPhotoURL(): void
    {
        $router = $this->createMock(UrlGeneratorInterface::class);
        $router->expects(self::once())->method('generate')->with('mautic_whatsqr_avatar', ['conversationId' => 12])->willReturn('/s/whatsqr/avatars/12');
        $provider = new ParticipantAvatarProvider($router);
        $c = (new MetaConversation())->setAsset((new MetaAsset())->setType(AssetType::WhatsAppQrSession));
        self::assertNull($provider->avatarUrl($c));
        (new \ReflectionProperty(MetaConversation::class, 'id'))->setValue($c, 12);
        self::assertSame('/s/whatsqr/avatars/12', $provider->avatarUrl($c));
        $c->getAsset()->setType(AssetType::WhatsAppPhoneNumber);
        self::assertNull($provider->avatarUrl($c));
    }
}
