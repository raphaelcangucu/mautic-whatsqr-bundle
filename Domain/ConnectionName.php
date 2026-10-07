<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Domain;

final class ConnectionName
{
    public const MAX_LENGTH = 191;

    public static function normalize(string $value): string
    {
        $name = trim($value);
        if ('' === $name || !mb_check_encoding($name, 'UTF-8')
            || mb_strlen($name) > self::MAX_LENGTH || preg_match('/\p{Cc}/u', $name)) {
            throw new \InvalidArgumentException('mautic.whatsqr.form.name.invalid');
        }

        return $name;
    }
}
