<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Driver;

use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticWhatsQrBundle\Domain\AttachmentReference;
use Symfony\Component\HttpFoundation\Request;
use Symfony\Component\HttpFoundation\Response;

interface AttachmentDriverInterface
{
    public function attachmentResponse(MetaAsset $asset, AttachmentReference $attachment, Request $request): Response;
}
