<?php

declare(strict_types=1);
namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Infrastructure;
use MauticPlugin\MauticWhatsQrBundle\Infrastructure\SessionStreamDecoder;
use PHPUnit\Framework\TestCase;
final class SessionStreamDecoderTest extends TestCase
{
    public function testByteSplitFramesIncludingCRLFAndUTF8(): void
    {
        $frames = [];
        $decoder = new SessionStreamDecoder();
        foreach (str_split("event: session\r\ndata: {\"nome\":\"conexão\"}\r\n\r\n: heartbeat\n\n") as $byte) {
            array_push($frames, ...$decoder->push($byte));
        }
        self::assertSame([
            ['event'=>'session','data'=>'{"nome":"conexão"}'],
            ['event'=>'message','data'=>''],
        ], $frames);
    }
    public function testOversizedPartialFrameIsRejected(): void
    {
        $this->expectException(\RuntimeException::class);
        (new SessionStreamDecoder())->push(str_repeat('x',131073));
    }
}
