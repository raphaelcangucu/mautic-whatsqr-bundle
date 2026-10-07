<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Application;

use MauticPlugin\MauticMetaBundle\Domain\AssetType;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticMetaBundle\Entity\MetaAssetRepository;
use MauticPlugin\MauticWhatsQrBundle\Domain\ConnectionName;
use MauticPlugin\MauticWhatsQrBundle\Driver\SessionDriverFactory;

final class ConnectionManagement
{
    public const SETTING_MANAGED_SESSION = 'whatsqr_managed_session';

    public function __construct(
        private readonly MetaAssetRepository $assets,
        private readonly SessionDriverFactory $drivers,
    ) {
    }

    /** @return list<MetaAsset> Configured servers; no remote requests or credentials in the form. */
    public function sources(): array
    {
        $sources = [];
        foreach ($this->assets->findBy([
            'type' => AssetType::WhatsAppQrSession->value, 'isPublished' => true, 'status' => 'active',
        ], ['id' => 'ASC']) as $asset) {
            if (!$asset instanceof MetaAsset || null === $asset->getId()) {
                continue;
            }
            try {
                $this->drivers->forAsset($asset);
                $sources[] = $asset;
            } catch (\DomainException|\RuntimeException) {
                // An unconfigured server cannot be a source for another account.
            }
        }

        return $sources;
    }

    public function rename(MetaAsset $asset, string $name): void
    {
        $this->assertQr($asset);
        $asset->setName(ConnectionName::normalize($name));
        // The Graph AssetManager replaces settings, which would lose the QR credentials.
        $this->assets->saveEntity($asset);
    }

    public function create(string $name, MetaAsset $source): MetaAsset
    {
        $name = ConnectionName::normalize($name);
        $this->assertQr($source);
        if (!$source->isPublished() || 'active' !== $source->getStatus()) {
            throw new \DomainException('mautic.whatsqr.form.source.unavailable');
        }

        $asset = (new MetaAsset())
            ->setName($name)
            ->setType(AssetType::WhatsAppQrSession)
            ->setExternalId('qr_'.bin2hex(random_bytes(16)))
            ->setConnection($source->getConnection())
            ->setStatus('active')
            ->setIsPublished(true)
            ->setIsDefault(false);

        // Copy conversation policy only; not the paired phone, JID, state or old secret.
        $settings = array_intersect_key($source->getSettings(), array_flip([
            'default_region', 'trusted_import_default_region', 'trusted_import_convert_legacy_br_mobile',
            'contact_match_field', 'require_opt_in', 'anti_spam_enabled', 'daily_send_limit',
            'hourly_send_limit', 'recipient_daily_limit', 'recipient_cooldown_seconds',
            'enforce_customer_service_window',
        ]));
        $asset->setSettings($settings + [
            'default_region' => 'BR', 'require_opt_in' => true, 'anti_spam_enabled' => true,
            self::SETTING_MANAGED_SESSION => true,
        ]);
        $this->drivers->configureAdditionalAsset($asset, $source, bin2hex(random_bytes(32)));
        $this->assets->saveEntity($asset);

        // Provisioning/QR generation require the explicit, CSRF-protected Start POST.
        return $asset;
    }

    private function assertQr(MetaAsset $asset): void
    {
        if (AssetType::WhatsAppQrSession !== $asset->getType()) {
            throw new \DomainException('mautic.whatsqr.form.source.unavailable');
        }
    }
}
