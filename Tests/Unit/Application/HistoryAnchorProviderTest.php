<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Application;

use Doctrine\DBAL\Connection;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticWhatsQrBundle\Application\HistoryAnchorProvider;
use PHPUnit\Framework\TestCase;

final class HistoryAnchorProviderTest extends TestCase
{
    public function testOnlyActualPrivateMessageAnchorsLeaveTheSingleBoundedQuery(): void
    {
        $row = ['recipient'=>'5511999999999','external_id'=>'qr:scoped','direction'=>'outbound','date_added'=>'2026-10-07 18:00:00','payload'=>json_encode(['message'=>['id'=>'ACTUAL-WA-ID','from'=>'5511999999999@s.whatsapp.net','text'=>'private text']])];
        $group = $row;
        $group['payload'] = json_encode(['message'=>['id'=>'group','from'=>'123456@g.us']]);
        $db = $this->createMock(Connection::class);
        $db->expects(self::once())->method('fetchAllAssociative')->with(self::callback(fn (string $sql): bool => str_contains($sql, 'LIMIT 32') && str_contains($sql, 'c.asset_id=:asset')), ['asset'=>16])->willReturn([$row, $group]);
        $asset = $this->createMock(MetaAsset::class);
        $asset->method('getId')->willReturn(16);
        $anchors = (new HistoryAnchorProvider($db))->forAsset($asset);
        self::assertSame([['jid'=>'5511999999999@s.whatsapp.net','id'=>'ACTUAL-WA-ID','from_me'=>true,'timestamp'=>1791396000]], $anchors);
        self::assertStringNotContainsString('private text', json_encode($anchors));
    }
}
