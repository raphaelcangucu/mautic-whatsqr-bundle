<?php

declare(strict_types=1);
namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Driver;

use MauticPlugin\MauticMetaBundle\Domain\AssetType;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticWhatsQrBundle\Driver\WhatsMeowDriver;
use PHPUnit\Framework\TestCase;
use Symfony\Component\HttpClient\MockHttpClient;
use Symfony\Component\HttpClient\Response\MockResponse;

final class ProfileImageTest extends TestCase
{
    public function testPrivateServiceBytesBecomeAnImageWithoutExposingCredentials(): void
    {
        $png = base64_decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/l9sAAAAASUVORK5CYII=');
        $http = new MockHttpClient(static function (string $method, string $url, array $options) use ($png): MockResponse {
            self::assertSame('GET', $method);
            self::assertSame('http://127.0.0.1:8088/sessions/one/avatar?to=123456%40lid', $url);
            self::assertSame('Authorization: Bearer private-token', $options['normalized_headers']['authorization'][0]);
            self::assertSame(0, $options['max_redirects']);
            return new MockResponse($png, ['http_code' => 200]);
        });
        $asset = (new MetaAsset())->setType(AssetType::WhatsAppQrSession)->setExternalId('one');
        $picture = (new WhatsMeowDriver($http, 'http://127.0.0.1:8088', 'private-token'))->profileImage($asset, '123456@lid');
        self::assertSame('image/png', $picture?->mimeType);
        self::assertSame($png, $picture?->contents);
    }

    public function testUnavailableAndUnsafeContentsProduceNoImage(): void
    {
        $asset = (new MetaAsset())->setType(AssetType::WhatsAppQrSession)->setExternalId('one');
        foreach ([[404, ''], [503, ''], [200, '<svg onload="alert(1)">'], [200, str_repeat('x', 262145)]] as [$status, $contents]) {
            $http = new MockHttpClient(new MockResponse($contents, ['http_code' => $status]));
            self::assertNull((new WhatsMeowDriver($http, 'http://127.0.0.1:8088', 'private-token'))->profileImage($asset, '5511999990000'));
        }
    }
}
