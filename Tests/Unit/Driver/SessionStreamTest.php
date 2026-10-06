<?php

declare(strict_types=1);
namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Driver;
use MauticPlugin\MauticMetaBundle\Domain\AssetType;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticWhatsQrBundle\Driver\WhatsMeowDriver;
use MauticPlugin\MauticWhatsQrBundle\Domain\SessionState;
use Symfony\Component\HttpClient\MockHttpClient;
use Symfony\Component\HttpClient\Response\MockResponse;
use PHPUnit\Framework\TestCase;
final class SessionStreamTest extends TestCase
{
    private function client(MockResponse $response): MockHttpClient
    {
        return new class($response) extends MockHttpClient {
            public ?\Symfony\Contracts\HttpClient\ResponseInterface $last = null;
            public function request(string $method, string $url, array $options = []): \Symfony\Contracts\HttpClient\ResponseInterface
            {
                return $this->last = parent::request($method, $url, $options);
            }
        };
    }
    private function asset(): MetaAsset { return (new MetaAsset())->setType(AssetType::WhatsAppQrSession)->setExternalId('one'); }
    public function testPrivateStreamingRequestAndInitialAndChangedStates(): void
    {
        $body = (static function(): \Generator {
            yield 'event: sess';
            yield "ion\ndata: {\"id\":\"one\",\"status\":\"pairing\",\"qr\":\"next\"}\n\n";
            yield ": heartbeat\n\n";
            yield "event: session\ndata: {\"id\":\"one\",\"status\":\"connected\",\"jid\":\"user@s.whatsapp.net\"}\n\n";
        })();
        $response = new MockResponse($body, ['response_headers'=>['content-type: text/event-stream']]);
        $http = $this->client($response);
        $http->setResponseFactory(function($method,$url,$options) use ($response) {
            self::assertSame('GET',$method);
            self::assertSame('http://127.0.0.1:8088/sessions/one/events',$url);
            self::assertFalse($options['buffer']);
            self::assertSame(0,$options['max_redirects']);
            self::assertStringNotContainsString('secret',$url);
            return $response;
        });
        $states = []; $beats = 0;
        (new WhatsMeowDriver($http,'http://127.0.0.1:8088','secret'))->watchSession($this->asset(), function($state)use(&$states){$states[]=$state;return true;},function()use(&$beats){$beats++;return true;});
        self::assertCount(2,$states);
        self::assertSame(SessionState::PAIRING,$states[0]->status);
        self::assertSame('next',$states[0]->qr);
        self::assertSame(SessionState::CONNECTED,$states[1]->status);
        self::assertSame(1,$beats);
        self::assertTrue($http->last->getInfo('canceled'));
    }
    public function testAnotherAccountsQRIsRejected(): void
    {
        $response=new MockResponse("event: session\ndata: {\"id\":\"other\",\"status\":\"pairing\",\"qr\":\"private\"}\n\n");
        $driver=new WhatsMeowDriver(new MockHttpClient($response),'http://127.0.0.1:8088','secret');
        $this->expectException(\RuntimeException::class);
        $driver->watchSession($this->asset(),function(){self::fail('cross-account callback');},fn()=>true);
    }
    public function testClosedPageCancelsWithoutConsumingMoreStates(): void
    {
        $response=new MockResponse("event: session\ndata: {\"id\":\"one\",\"status\":\"ready\"}\n\n");
        $seen=0;
        $http = $this->client($response);
        (new WhatsMeowDriver($http,'http://127.0.0.1:8088','secret'))->watchSession($this->asset(),function($state)use(&$seen){self::assertNull($state);$seen++;return false;},fn()=>false);
        self::assertSame(1,$seen);self::assertTrue($http->last->getInfo('canceled'));
    }
}
