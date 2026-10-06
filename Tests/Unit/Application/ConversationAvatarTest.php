<?php

declare(strict_types=1);
namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Application;

use MauticPlugin\MauticWhatsQrBundle\Application\ConversationAvatar;
use MauticPlugin\MauticWhatsQrBundle\Domain\ProfileImage;
use PHPUnit\Framework\TestCase;
use Symfony\Component\HttpFoundation\Request;

final class ConversationAvatarTest extends TestCase
{
    public function testAuthenticatedImageIsPrivateAndSupportsConditionalRequests(): void
    {
        $picture = new ProfileImage('image-bytes', 'image/jpeg');
        $response = ConversationAvatar::imageResponse(Request::create('/'), $picture);
        self::assertSame('image/jpeg', $response->headers->get('Content-Type'));
        self::assertTrue($response->headers->hasCacheControlDirective('private'));
        self::assertSame('nosniff', $response->headers->get('X-Content-Type-Options'));
        $request = Request::create('/');
        $request->headers->set('If-None-Match', $response->getEtag());
        $cached = ConversationAvatar::imageResponse($request, $picture);
        self::assertSame(304, $cached->getStatusCode());
        self::assertSame('', $cached->getContent());
    }

    public function testMissingPhotoFallsBackWithoutAnySensitiveErrorDetails(): void
    {
        $response = ConversationAvatar::imageResponse(Request::create('/'), null);
        self::assertSame(404, $response->getStatusCode());
        self::assertSame('', $response->getContent());
        self::assertTrue($response->headers->hasCacheControlDirective('private'));
    }
}
