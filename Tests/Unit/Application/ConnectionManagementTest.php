<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Application;

use Mautic\CoreBundle\Helper\CoreParametersHelper;
use Mautic\CoreBundle\Helper\EncryptionHelper;
use MauticPlugin\MauticMetaBundle\Domain\AssetType;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticMetaBundle\Entity\MetaAssetRepository;
use MauticPlugin\MauticMetaBundle\Entity\MetaConnection;
use MauticPlugin\MauticMetaBundle\Security\CredentialVault;
use MauticPlugin\MauticWhatsQrBundle\Application\ConnectionManagement;
use MauticPlugin\MauticWhatsQrBundle\Application\SessionStarter;
use MauticPlugin\MauticWhatsQrBundle\Domain\SessionState;
use MauticPlugin\MauticWhatsQrBundle\Driver\SessionDriverFactory;
use PHPUnit\Framework\TestCase;
use Symfony\Component\HttpClient\MockHttpClient;
use Symfony\Component\HttpClient\Response\MockResponse;

final class ConnectionManagementTest extends TestCase
{
    private function factory(MockHttpClient $http): SessionDriverFactory
    {
        $encryption = $this->createMock(EncryptionHelper::class);
        $encryption->method('encrypt')->willReturnCallback(static fn ($value) => 'sealed:'.base64_encode($value));
        $encryption->method('decrypt')->willReturnCallback(static fn ($value) => base64_decode(substr($value, 7)));
        $parameters = $this->createMock(CoreParametersHelper::class);
        $parameters->method('get')->willReturnCallback(static fn ($name, $fallback = null) => $fallback);

        return new SessionDriverFactory($http, new CredentialVault($encryption), $parameters);
    }

    private function source(SessionDriverFactory $factory): MetaAsset
    {
        $asset = (new MetaAsset(16))->setConnection(new MetaConnection(4))
            ->setType(AssetType::WhatsAppQrSession)->setExternalId('existing-unit-session')
            ->setName('Company WhatsApp')->setPhoneNumber('+550000000000')
            ->setIsPublished(true)->setStatus('active')->setIsDefault(true)
            ->setSettings([SessionState::SETTING_STATUS => 'connected', SessionState::SETTING_JID => 'private-existing-jid',
                'default_region' => 'BR', 'require_opt_in' => true, 'daily_send_limit' => 25]);
        $factory->configure($asset, 'whatsmeow', 'http://127.0.0.1:8088', 'unit-server-token', str_repeat('a', 64));

        return $asset;
    }

    public function testRenamePreservesPhoneHistoryIdentityAndAllCredentialsWithoutRemoteCalls(): void
    {
        $http = new MockHttpClient();
        $factory = $this->factory($http);
        $asset = $this->source($factory);
        $settings = $asset->getSettings();
        $connection = $asset->getConnection();
        $repository = $this->createMock(MetaAssetRepository::class);
        $repository->expects(self::once())->method('saveEntity')->with($asset);
        (new ConnectionManagement($repository, $factory))->rename($asset, '  Comercial · São Paulo  ');

        self::assertSame('Comercial · São Paulo', $asset->getName());
        self::assertSame($settings, $asset->getSettings());
        self::assertSame($connection, $asset->getConnection());
        self::assertSame('existing-unit-session', $asset->getExternalId());
        self::assertSame('+550000000000', $asset->getPhoneNumber());
        self::assertTrue($asset->isDefault());
        self::assertSame(0, $http->getRequestsCount());
    }

    public function testNewAccountsHaveIndependentIdentityAndSecretsAndNeverInheritTheDevice(): void
    {
        $http = new MockHttpClient();
        $factory = $this->factory($http);
        $source = $this->source($factory);
        $original = $source->getSettings();
        $repository = $this->createMock(MetaAssetRepository::class);
        $repository->expects(self::exactly(2))->method('saveEntity');
        $manager = new ConnectionManagement($repository, $factory);
        $first = $manager->create('Sales', $source);
        $second = $manager->create('Support', $source);

        self::assertNotSame($first->getExternalId(), $second->getExternalId());
        self::assertMatchesRegularExpression('/^qr_[a-f0-9]{32}$/', $first->getExternalId());
        self::assertSame($source->getConnection(), $first->getConnection());
        self::assertNull($first->getPhoneNumber());
        self::assertArrayNotHasKey(SessionState::SETTING_JID, $first->getSettings());
        self::assertArrayNotHasKey(SessionState::SETTING_STATUS, $first->getSettings());
        self::assertSame(25, $first->getSettings()['daily_send_limit']);
        self::assertTrue($first->getSettings()[ConnectionManagement::SETTING_MANAGED_SESSION]);
        self::assertFalse($first->isDefault());
        self::assertSame('active', $first->getStatus());
        self::assertTrue($first->isPublished());
        self::assertMatchesRegularExpression('/^[a-f0-9]{64}$/', $factory->webhookSecret($first));
        self::assertNotSame($factory->webhookSecret($first), $factory->webhookSecret($second));
        self::assertNotSame($factory->webhookSecret($first), $first->getSettings()[SessionDriverFactory::SETTING_WEBHOOK_SECRET]);
        self::assertSame($original, $source->getSettings());
        self::assertSame(0, $http->getRequestsCount(), 'Creating a record must not open, reset or provision a device.');
    }

    public function testInvalidNameAndGraphAssetsAreRejectedBeforeAnyWrites(): void
    {
        $http = new MockHttpClient();
        $factory = $this->factory($http);
        $source = $this->source($factory);
        $repository = $this->createMock(MetaAssetRepository::class);
        $repository->expects(self::never())->method('saveEntity');
        $manager = new ConnectionManagement($repository, $factory);
        foreach (['', '   ', str_repeat('é', 192), "Name\0bad", "Name\nbad"] as $name) {
            try { $manager->rename($source, $name); self::fail('Invalid name accepted'); }
            catch (\InvalidArgumentException) { self::assertSame('Company WhatsApp', $source->getName()); }
        }
        $source->setType(AssetType::WhatsAppPhoneNumber);
        $this->expectException(\DomainException::class);
        $manager->create('Must stay official', $source);
    }

    public function testSourceListOmitsUnconfiguredAccountsAndDoesNotContactTheServer(): void
    {
        $http = new MockHttpClient();
        $factory = $this->factory($http);
        $source = $this->source($factory);
        $unconfigured = (new MetaAsset(17))->setType(AssetType::WhatsAppQrSession);
        $repository = $this->createMock(MetaAssetRepository::class);
        $repository->expects(self::once())->method('findBy')->with([
            'type' => 'whatsapp_qr_session', 'isPublished' => true, 'status' => 'active',
        ], ['id' => 'ASC'])->willReturn([$source, $unconfigured]);

        self::assertSame([$source], (new ConnectionManagement($repository, $factory))->sources());
        self::assertSame(0, $http->getRequestsCount());
    }

    public function testNewAccountIsProvisionedBeforeOpeningAndNeverDeletesAnySession(): void
    {
        $seen = [];
        $http = new MockHttpClient(function ($method, $url, $options) use (&$seen) {
            $seen[] = [$method, parse_url($url, PHP_URL_PATH), json_decode($options['body'] ?? '{}', true)];
            return new MockResponse(json_encode(str_ends_with($url, '/health')
                ? ['sessions' => []] : ['id' => 'new-session', 'configured' => true, 'status' => 'pairing']));
        });
        $factory = $this->factory($http);
        $asset = $this->source($factory)->setExternalId('new-session');
        $asset->setSettings($asset->getSettings() + [ConnectionManagement::SETTING_MANAGED_SESSION => true]);
        (new SessionStarter($factory))->start($asset);

        self::assertSame([['GET', '/health'], ['POST', '/sessions/new-session/configuration'], ['POST', '/sessions']],
            array_map(static fn ($call) => [$call[0], $call[1]], $seen));
        self::assertSame(str_repeat('a', 64), $seen[1][2]['webhook_secret']);
        self::assertSame('new-session', $seen[2][2]['id']);
    }

    public function testStartingAnExistingLiveAccountIsReadOnly(): void
    {
        $http = new MockHttpClient(new MockResponse('{"sessions":[{"id":"existing-unit-session","status":"connected"}]}'));
        $factory = $this->factory($http);
        $asset = $this->source($factory);
        $asset->setSettings($asset->getSettings() + [ConnectionManagement::SETTING_MANAGED_SESSION => true]);
        (new SessionStarter($factory))->start($asset);
        self::assertSame(1, $http->getRequestsCount());
    }

    public function testProvisioningFailureDoesNotOpenOrDeleteAnyDevice(): void
    {
        $http = new MockHttpClient([new MockResponse('{"sessions":[]}'), new MockResponse('{"error":"private failure"}', ['http_code' => 409])]);
        $factory = $this->factory($http);
        $asset = $this->source($factory);
        $asset->setSettings($asset->getSettings() + [ConnectionManagement::SETTING_MANAGED_SESSION => true]);
        try { (new SessionStarter($factory))->start($asset); self::fail('Registration refusal was ignored'); }
        catch (\DomainException) { self::assertSame(2, $http->getRequestsCount()); }
    }
}
