<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Driver;

use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticWhatsQrBundle\Domain\ProfileImage;

interface ProfileImageDriverInterface
{
    public function profileImage(MetaAsset $asset, string $recipient): ?ProfileImage;
}
