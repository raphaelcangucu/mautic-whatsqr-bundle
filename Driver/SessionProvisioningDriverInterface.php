<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Driver;

use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;

interface SessionProvisioningDriverInterface
{
    /** Add a new account's secret; never replace existing account credentials. */
    public function registerSession(MetaAsset $asset, string $secret): void;
}
