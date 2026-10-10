<?php
declare(strict_types=1);
namespace MauticPlugin\MauticWhatsQrBundle\Driver;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticWhatsQrBundle\Domain\SentMessage;
interface AudioSessionDriverInterface { public function sendAudio(MetaAsset $asset,string $to,string $bytes,string $requestId): SentMessage; }
