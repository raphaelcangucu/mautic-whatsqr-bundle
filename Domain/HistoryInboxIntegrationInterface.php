<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Domain;

use MauticPlugin\MauticMetaBundle\Entity\MetaMessage;

interface HistoryInboxIntegrationInterface
{
    public function record(MetaMessage $message): void;
    public function recordDeviceReply(MetaMessage $message): void;
}
