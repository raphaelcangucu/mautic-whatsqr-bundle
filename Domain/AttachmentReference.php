<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Domain;

final readonly class AttachmentReference
{
    public const TYPES = ['image', 'video', 'audio', 'document', 'sticker'];
    public const MAX_BYTES = 33554432;

    public function __construct(public string $id, public string $type, public string $filename, public int $size, public ?string $error) {}

    public static function fromInbound(array $inbound): ?self
    {
        $data = $inbound['attachment'] ?? null;
        if (!is_array($data) || !in_array($data['type'] ?? null, self::TYPES, true)) { return null; }
        $id = is_string($data['id'] ?? null) && 1 === preg_match('/^[a-f0-9]{64}$/D', $data['id']) ? $data['id'] : '';
        $size = is_int($data['file_size'] ?? null) && $data['file_size'] >= 0 ? $data['file_size'] : 0;
        $name = is_string($data['filename'] ?? null) ? $data['filename'] : '';
        $name = mb_substr(preg_replace('/[\p{Cc}\p{Cf}]/u', '', basename(str_replace('\\', '/', $name))) ?? '', 0, 160);
        $error = in_array($data['error'] ?? null, ['too_large', 'storage_full', 'unavailable'], true) ? $data['error'] : null;
        if ($size > self::MAX_BYTES) { $error = 'too_large'; }
        if (null !== $error || 0 === $size) { $id = ''; }

        return new self($id, $data['type'], $name, $size, $error);
    }
}
