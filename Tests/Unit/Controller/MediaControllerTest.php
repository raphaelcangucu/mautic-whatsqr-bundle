<?php

declare(strict_types=1);
namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Controller;

use Mautic\CoreBundle\Security\Permissions\CorePermissions;
use MauticPlugin\MauticMetaBundle\Entity\MetaMessageRepository;
use MauticPlugin\MauticWhatsQrBundle\Controller\MediaController;
use MauticPlugin\MauticWhatsQrBundle\Driver\SessionDriverFactory;
use PHPUnit\Framework\TestCase;
use Symfony\Component\HttpFoundation\Request;
use Symfony\Component\Security\Core\Exception\AccessDeniedException;

final class MediaControllerTest extends TestCase
{
    public function testPermissionsStopBeforeMessageLookupAndMediaDownload(): void
    {
        $permissions = $this->createMock(CorePermissions::class);
        $permissions->expects(self::once())->method('isGranted')->with(['inbox:conversations:view', 'meta:messages:view'])->willReturn(false);
        $messages = $this->createMock(MetaMessageRepository::class); $messages->expects(self::never())->method('createQueryBuilder');
        $controller = (new \ReflectionClass(MediaController::class))->newInstanceWithoutConstructor();
        $drivers = (new \ReflectionClass(SessionDriverFactory::class))->newInstanceWithoutConstructor();
        $this->expectException(AccessDeniedException::class);
        $controller->show(123, Request::create('/'), $permissions, $messages, $drivers);
    }
}
