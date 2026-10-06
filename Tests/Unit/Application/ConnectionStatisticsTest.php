<?php
declare(strict_types=1);
namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Application;
use Doctrine\DBAL\Connection;
use MauticPlugin\MauticWhatsQrBundle\Application\ConnectionStatistics;
use PHPUnit\Framework\TestCase;
final class ConnectionStatisticsTest extends TestCase
{
    public function testManyAccountsUseTwoAggregateQueries(): void
    {
        $connection=$this->createMock(Connection::class);
        $connection->expects(self::exactly(2))->method('fetchAllAssociative')
            ->willReturnOnConsecutiveCalls([['asset_id'=>5,'total'=>12]], [['asset_id'=>5,'latest'=>'2026-10-05 12:00:00']]);
        $statistics=(new ConnectionStatistics($connection))->forAssets([5,6,7,8,9]);
        self::assertSame(12,$statistics['queued'][5]);
        self::assertSame('2026-10-05T12:00:00+00:00',$statistics['last'][5]->format('c'));
    }
    public function testEmptyAccountsDoNotQueryTheDatabase(): void
    {
        $connection=$this->createMock(Connection::class);
        $connection->expects(self::never())->method('fetchAllAssociative');
        self::assertSame(['queued'=>[],'last'=>[]],(new ConnectionStatistics($connection))->forAssets([]));
    }
}
