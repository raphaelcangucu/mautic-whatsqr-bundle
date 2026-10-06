<?php

declare(strict_types=1);
namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Controller;

use Mautic\CoreBundle\Security\Permissions\CorePermissions;
use MauticPlugin\MauticMetaBundle\Entity\MetaConversationRepository;
use MauticPlugin\MauticWhatsQrBundle\Application\ConversationAvatar;
use MauticPlugin\MauticWhatsQrBundle\Controller\AvatarController;
use PHPUnit\Framework\TestCase;
use Symfony\Component\HttpFoundation\Request;
use Symfony\Component\Security\Core\Exception\AccessDeniedException;

final class AvatarControllerTest extends TestCase
{
    public function testMissingInboxPermissionStopsBeforeAnyLookupOrDownload(): void
    {
        $permissions = $this->createMock(CorePermissions::class);
        $permissions->expects(self::once())->method('isGranted')->with(['inbox:conversations:view', 'meta:messages:view'])->willReturn(false);
        $conversations = $this->createMock(MetaConversationRepository::class);
        $conversations->expects(self::never())->method('createQueryBuilder');
        $controller = (new \ReflectionClass(AvatarController::class))->newInstanceWithoutConstructor();
        $avatars = (new \ReflectionClass(ConversationAvatar::class))->newInstanceWithoutConstructor();
        $this->expectException(AccessDeniedException::class);
        $controller->show(12, Request::create('/'), $permissions, $conversations, $avatars);
    }
}
