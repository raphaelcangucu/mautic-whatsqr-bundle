<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Controller;

use Mautic\CoreBundle\Security\Permissions\CorePermissions;
use MauticPlugin\MauticMetaBundle\Entity\MetaAssetRepository;
use MauticPlugin\MauticWhatsQrBundle\Application\ConnectionManagement;
use MauticPlugin\MauticWhatsQrBundle\Controller\ConnectionsController;
use PHPUnit\Framework\TestCase;
use Symfony\Component\HttpFoundation\Request;
use Symfony\Component\Security\Core\Exception\AccessDeniedException;

final class ConnectionManagementAccessTest extends TestCase
{
    public function testCreatingRequiresPermissionBeforeLoadingAnyConfiguration(): void
    {
        $controller = (new \ReflectionClass(ConnectionsController::class))->newInstanceWithoutConstructor();
        $manager = (new \ReflectionClass(ConnectionManagement::class))->newInstanceWithoutConstructor();
        $permissions = $this->createMock(CorePermissions::class);
        $permissions->expects(self::once())->method('isGranted')->with('meta:connections:create')->willReturn(false);
        $this->expectException(AccessDeniedException::class);
        $controller->new(Request::create('/'), $permissions, $manager);
    }

    public function testEditingRequiresPermissionBeforeLookingUpTheAccount(): void
    {
        $controller = (new \ReflectionClass(ConnectionsController::class))->newInstanceWithoutConstructor();
        $manager = (new \ReflectionClass(ConnectionManagement::class))->newInstanceWithoutConstructor();
        $permissions = $this->createMock(CorePermissions::class);
        $permissions->expects(self::once())->method('isGranted')->with('meta:connections:edit')->willReturn(false);
        $assets = $this->createMock(MetaAssetRepository::class);
        $assets->expects(self::never())->method('find');
        $this->expectException(AccessDeniedException::class);
        $controller->edit(16, Request::create('/'), $permissions, $assets, $manager);
    }
}
