<?php

declare(strict_types=1);
namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Inbox;

use MauticPlugin\MauticMetaBundle\Domain\AssetType;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticMetaBundle\Entity\MetaMessage;
use MauticPlugin\MauticWhatsQrBundle\Domain\AttachmentReference;
use MauticPlugin\MauticWhatsQrBundle\Inbox\AttachmentProvider;
use PHPUnit\Framework\TestCase;
use Symfony\Component\Routing\Generator\UrlGeneratorInterface;

final class AttachmentProviderTest extends TestCase
{
    public function testQrMessageUsesProtectedRouteWhileOfficialChannelIsPreserved(): void
    {
        $router = $this->createMock(UrlGeneratorInterface::class);
        $router->expects(self::once())->method('generate')->with('mautic_whatsqr_media', ['messageId' => 15], UrlGeneratorInterface::ABSOLUTE_URL)->willReturn('https://mautic.test/s/whatsqr/media/15');
        $message = (new MetaMessage())->setAsset((new MetaAsset())->setType(AssetType::WhatsAppQrSession))->setDirection('inbound')->setMessageType('image')->setPayload(['message' => ['attachment' => ['type' => 'image', 'id' => str_repeat('b', 64), 'file_size' => 128]]]);
        (new \ReflectionProperty(MetaMessage::class, 'id'))->setValue($message, 15);
        $provider = new AttachmentProvider($router);
        self::assertSame('https://mautic.test/s/whatsqr/media/15', $provider->attachmentUrl($message, 'image'));
        self::assertNull($provider->attachmentUrl($message, 'video'));
        $message->getAsset()->setType(AssetType::WhatsAppPhoneNumber); self::assertNull($provider->attachmentUrl($message, 'image'));
    }

    public function testReferenceSanitizesNameAndRefusesUnboundedOrMalformedFiles(): void
    {
        $ref = AttachmentReference::fromInbound(['attachment' => ['type' => 'document', 'id' => str_repeat('a', 64), 'filename' => "..\\relatório\r\n.pdf", 'file_size' => 20]]);
        self::assertSame('relatório.pdf', $ref?->filename);
        self::assertSame('', AttachmentReference::fromInbound(['attachment' => ['type' => 'image', 'id' => '../secret', 'file_size' => 128]])?->id);
        $large = AttachmentReference::fromInbound(['attachment' => ['type' => 'video', 'id' => str_repeat('a', 64), 'file_size' => 33554433]]);
        self::assertSame('too_large', $large?->error); self::assertSame('', $large?->id);
    }
}
