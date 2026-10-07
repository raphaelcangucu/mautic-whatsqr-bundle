<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Application;

use Doctrine\DBAL\Connection;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;

/** Actual latest WA message IDs, obtained in one bounded query per account. */
final class HistoryAnchorProvider
{
    public function __construct(private readonly Connection $connection) {}

    public function forAsset(MetaAsset $asset): array
    {
        $p = defined('MAUTIC_TABLE_PREFIX') ? MAUTIC_TABLE_PREFIX : '';
        $rows = $this->connection->fetchAllAssociative('SELECT c.recipient,m.external_id,m.direction,m.date_added,m.payload FROM '.$p.'meta_conversations c JOIN '.$p.'meta_messages m ON m.id=(SELECT last.id FROM '.$p.'meta_messages last WHERE last.conversation_id=c.id ORDER BY last.date_added DESC,last.id DESC LIMIT 1) WHERE c.asset_id=:asset ORDER BY c.last_message_at DESC,c.id DESC LIMIT 32', ['asset' => $asset->getId()]);
        return self::present($rows);
    }

    public static function present(array $rows): array
    {
        $anchors = [];
        foreach (array_slice($rows, 0, 32) as $row) {
            $payload = json_decode((string) ($row['payload'] ?? ''), true);
            $jid = $payload['message']['from'] ?? (($row['recipient'] ?? '').'@s.whatsapp.net');
            $id = $payload['message']['id'] ?? ($row['external_id'] ?? '');
            if (!is_string($jid) || !preg_match('/^[0-9]+@(s\.whatsapp\.net|lid)$/D', $jid) || !is_string($id) || !preg_match('/^[A-Za-z0-9._:-]{1,256}$/D', $id) || str_starts_with($id, 'qr:')) { continue; }
            try { $date = new \DateTimeImmutable($row['date_added'], new \DateTimeZone('UTC')); } catch (\Throwable) { continue; }
            $anchors[] = ['jid' => $jid, 'id' => $id, 'from_me' => 'outbound' === $row['direction'], 'timestamp' => $date->getTimestamp()];
        }
        return $anchors;
    }
}
