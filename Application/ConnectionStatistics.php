<?php
declare(strict_types=1);
namespace MauticPlugin\MauticWhatsQrBundle\Application;
use Doctrine\DBAL\Connection;
use Doctrine\DBAL\ArrayParameterType;
final class ConnectionStatistics
{
    public function __construct(private readonly Connection $connection) {}
    public function forAssets(array $ids): array
    {
        if ([] === $ids) return ['queued'=>[], 'last'=>[]];
        $prefix=defined('MAUTIC_TABLE_PREFIX') ? MAUTIC_TABLE_PREFIX : '';
        $queued=[]; $last=[];
        $types=['ids'=>ArrayParameterType::INTEGER];
        $rows=$this->connection->fetchAllAssociative("SELECT asset_id,COUNT(*) AS total FROM ".$prefix."meta_outbound_jobs WHERE asset_id IN (:ids) AND status IN ('pending','retry','processing') GROUP BY asset_id", ['ids'=>$ids], $types);
        foreach($rows as $row) $queued[(int)$row['asset_id']]=(int)$row['total'];
        $rows=$this->connection->fetchAllAssociative('SELECT asset_id,MAX(date_added) AS latest FROM '.$prefix.'meta_messages WHERE asset_id IN (:ids) GROUP BY asset_id', ['ids'=>$ids], $types);
        foreach($rows as $row) if($row['latest']) $last[(int)$row['asset_id']]=new \DateTimeImmutable($row['latest'],new \DateTimeZone('UTC'));
        return ['queued'=>$queued,'last'=>$last];
    }
}
