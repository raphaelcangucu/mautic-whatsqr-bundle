<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Application;

use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticWhatsQrBundle\Driver\SessionDriverFactory;
use MauticPlugin\MauticWhatsQrBundle\Driver\SessionProvisioningDriverInterface;

final class SessionStarter
{
    public function __construct(private readonly SessionDriverFactory $drivers)
    {
    }

    public function start(MetaAsset $asset): void
    {
        $driver = $this->drivers->forAsset($asset);
        if (isset($driver->serviceSessions()[$asset->getExternalId()])) {
            return;
        }
        if (true === ($asset->getSettings()[ConnectionManagement::SETTING_MANAGED_SESSION] ?? false)) {
            if (!$driver instanceof SessionProvisioningDriverInterface) {
                throw new \DomainException('mautic.whatsqr.form.source.unavailable');
            }
            $driver->registerSession($asset, $this->drivers->webhookSecret($asset));
        }
        $driver->openSession($asset);
    }
}
