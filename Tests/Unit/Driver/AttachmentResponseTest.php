<?php

declare(strict_types=1);
namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Driver;

use MauticPlugin\MauticMetaBundle\Domain\AssetType;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticWhatsQrBundle\Domain\AttachmentReference;
use MauticPlugin\MauticWhatsQrBundle\Driver\WhatsMeowDriver;
use PHPUnit\Framework\TestCase;
use Symfony\Component\HttpClient\MockHttpClient;
use Symfony\Component\HttpClient\Response\MockResponse;
use Symfony\Component\HttpFoundation\Request;
use Symfony\Component\HttpFoundation\StreamedResponse;

final class AttachmentResponseTest extends TestCase
{
    public function testAttachmentStreamsWithPrivateBearerAndRangeWithoutGraph(): void
    {
        $id = str_repeat('a', 64);
        $http = new MockHttpClient(static function (string $method, string $url, array $options) use ($id): MockResponse {
            self::assertSame('http://127.0.0.1:8088/sessions/one/media/'.$id, $url);
            self::assertSame('Authorization: Bearer secret', $options['normalized_headers']['authorization'][0]);
            self::assertSame('Range: bytes=0-3', $options['normalized_headers']['range'][0]);
            self::assertFalse($options['buffer']); self::assertSame(0, $options['max_redirects']);
            return new MockResponse('%PDF', ['http_code' => 206, 'response_headers' => ['Content-Type: application/pdf', 'Content-Length: 4', 'Content-Range: bytes 0-3/20']]);
        });
        $asset = (new MetaAsset())->setType(AssetType::WhatsAppQrSession)->setExternalId('one');
        $request = Request::create('/'); $request->headers->set('Range', 'bytes=0-3');
        $response = (new WhatsMeowDriver($http, 'http://127.0.0.1:8088', 'secret'))->attachmentResponse($asset, new AttachmentReference($id, 'document', 'relatório.pdf', 20, null), $request);
        self::assertInstanceOf(StreamedResponse::class, $response); self::assertSame(206, $response->getStatusCode());
        self::assertSame('bytes 0-3/20', $response->headers->get('Content-Range')); self::assertTrue($response->headers->hasCacheControlDirective('private')); self::assertSame(300, $response->getMaxAge());
        self::assertSame('nosniff', $response->headers->get('X-Content-Type-Options'));
        self::assertStringStartsWith('attachment;', $response->headers->get('Content-Disposition'));
        ob_start(); $response->sendContent(); self::assertSame('%PDF', ob_get_clean());
    }

    public function testInvalidTypeHugeFilesAndUnsafeRangesAreRefused(): void
    {
        $asset = (new MetaAsset())->setType(AssetType::WhatsAppQrSession)->setExternalId('one');
        $ref = new AttachmentReference(str_repeat('a', 64), 'image', '', 4, null);
        foreach ([['Content-Type: text/html', 'Content-Length: 4'], ['Content-Type: image/png', 'Content-Length: 33554433']] as $headers) {
            $http = new MockHttpClient(new MockResponse('html', ['http_code' => 200, 'response_headers' => $headers]));
            self::assertSame(404, (new WhatsMeowDriver($http, 'http://127.0.0.1:8088', 'secret'))->attachmentResponse($asset, $ref, Request::create('/'))->getStatusCode());
        }
        $http = new MockHttpClient(static function (): MockResponse { self::fail('unsafe range reached private service'); });
        $request = Request::create('/'); $request->headers->set('Range', 'bytes=0-2,4-8');
        self::assertSame(416, (new WhatsMeowDriver($http, 'http://127.0.0.1:8088', 'secret'))->attachmentResponse($asset, $ref, $request)->getStatusCode());
    }

    public function testContradictoryLengthsRangesAndInvalidReferencesAreBlocked(): void
    {
        $asset = (new MetaAsset())->setType(AssetType::WhatsAppQrSession)->setExternalId('one');
        $ref = new AttachmentReference(str_repeat('a', 64), 'document', 'report.pdf', 20, null);
        foreach ([
            [200, 'Content-Length: 4', null],
            [200, 'Content-Length: 0', null],
            [206, 'Content-Length: 4', 'Content-Range: bytes 0-3/21'],
            [206, 'Content-Length: 4', 'Content-Range: bytes 0-5/20'],
            [206, 'Content-Length: 4', 'Content-Range: bytes 17-20/20'],
        ] as [$code, $length, $range]) {
            $headers = ['Content-Type: application/pdf', $length];
            if (null !== $range) { $headers[] = $range; }
            $request = Request::create('/'); $request->headers->set('Range', 'bytes=0-3');
            $http = new MockHttpClient(new MockResponse('%PDF', ['http_code' => $code, 'response_headers' => $headers]));
            $response = (new WhatsMeowDriver($http, 'http://127.0.0.1:8088', 'secret'))->attachmentResponse($asset, $ref, $request);
            self::assertSame(404, $response->getStatusCode());
            self::assertSame('nosniff', $response->headers->get('X-Content-Type-Options'));
            self::assertTrue($response->headers->hasCacheControlDirective('no-store'));
        }
        $http = new MockHttpClient(static function (): MockResponse { self::fail('invalid reference reached upstream'); });
        foreach ([new AttachmentReference('../secret', 'image', '', 4, null), new AttachmentReference(str_repeat('a', 64), 'image', '', 0, null), new AttachmentReference(str_repeat('a', 64), 'image', '', 4, 'unavailable')] as $invalid) {
            self::assertSame(404, (new WhatsMeowDriver($http, 'http://127.0.0.1:8088', 'secret'))->attachmentResponse($asset, $invalid, Request::create('/'))->getStatusCode());
        }
    }
}
