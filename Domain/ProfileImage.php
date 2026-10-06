<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Domain;

final readonly class ProfileImage
{
    public function __construct(public string $contents, public string $mimeType) {}
}
