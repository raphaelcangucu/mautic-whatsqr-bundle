<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Driver;

use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;

interface HistoryDriverInterface
{
    public function requestHistory(MetaAsset $asset): void;
}
