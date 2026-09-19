<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Driver;

use Mautic\CoreBundle\Helper\CoreParametersHelper;
use Mautic\CoreBundle\Helper\EncryptionHelper;
use MauticPlugin\MauticMetaBundle\Domain\AssetType;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticMetaBundle\Security\CredentialVault;
use MauticPlugin\MauticWhatsQrBundle\Driver\SessionDriverFactory;
use MauticPlugin\MauticWhatsQrBundle\Driver\WhatsMeowDriver;
use PHPUnit\Framework\TestCase;
use Symfony\Component\HttpClient\MockHttpClient;
use Symfony\Component\HttpClient\Response\MockResponse;

final class SessionDriverFactoryTest extends TestCase
{
    /**
     * Cifra de mentira, mas reversivel e visivelmente diferente do texto puro. E assim
     * que o teste consegue afirmar que o que ficou gravado no asset nao e o token, e que
     * quem o usa teve de abrir o cofre para chegar nele.
     */
    private function vault(): CredentialVault
    {
        $encryption = $this->createMock(EncryptionHelper::class);
        $encryption->method('encrypt')->willReturnCallback(static fn ($plain): string => 'selado:'.base64_encode((string) $plain));
        $encryption->method('decrypt')->willReturnCallback(static fn ($sealed) => base64_decode(substr((string) $sealed, 7), true));

        return new CredentialVault($encryption);
    }

    private function factory(MockHttpClient $http, string $defaultEngine = SessionDriverFactory::ENGINE_WHATSMEOW): SessionDriverFactory
    {
        $parameters = $this->createMock(CoreParametersHelper::class);
        $parameters->method('get')->willReturnCallback(
            static fn (string $name, $fallback = null) => SessionDriverFactory::PARAMETER_DEFAULT_ENGINE === $name ? $defaultEngine : $fallback
        );

        return new SessionDriverFactory($http, $this->vault(), $parameters);
    }

    private function asset(string $sessionId, array $settings = []): MetaAsset
    {
        return (new MetaAsset())
            ->setType(AssetType::WhatsAppQrSession)
            ->setExternalId($sessionId)
            ->setName('Numero '.$sessionId)
            ->setSettings($settings);
    }

    public function testTwoNumbersWithDifferentEnginesGetDifferentDrivers(): void
    {
        // Cada numero aponta para o seu processo, com o seu token. E isto que "por numero"
        // significa na pratica: trocar o motor de um chip que comeca a cair nao pode
        // mexer no endereco nem na credencial do vizinho.
        $atendimento = $this->asset('sess-atendimento');
        $cobranca = $this->asset('sess-cobranca');

        $seen = [];
        $http = new MockHttpClient(function (string $method, string $url, array $options) use (&$seen): MockResponse {
            $seen[] = [$url, $options['normalized_headers']['authorization'][0] ?? null];

            return new MockResponse(json_encode(['id' => 'x', 'status' => 'connected'], JSON_THROW_ON_ERROR), [
                'http_code'        => 200,
                'response_headers' => ['content-type: application/json'],
            ]);
        });
        $factory = $this->factory($http);

        $factory->configure($atendimento, SessionDriverFactory::ENGINE_WHATSMEOW, 'http://127.0.0.1:8088', 'token-atendimento', 'segredo-atendimento');
        // O segundo numero nao grava motor nenhum: numero novo herda o padrao global, que
        // e a unica coisa que a configuracao do plugin guarda.
        $factory->configure($cobranca, '', 'http://127.0.0.1:8090', 'token-cobranca', 'segredo-cobranca');

        $primeiro = $factory->forAsset($atendimento);
        $segundo = $factory->forAsset($cobranca);

        self::assertInstanceOf(WhatsMeowDriver::class, $primeiro);
        self::assertInstanceOf(WhatsMeowDriver::class, $segundo);
        self::assertNotSame($primeiro, $segundo, 'um adaptador por numero, e nao um compartilhado com o endereco do ultimo que pediu');

        $primeiro->openSession($atendimento);
        $segundo->openSession($cobranca);

        self::assertSame([
            ['http://127.0.0.1:8088/sessions', 'Authorization: Bearer token-atendimento'],
            ['http://127.0.0.1:8090/sessions', 'Authorization: Bearer token-cobranca'],
        ], $seen);
    }

    public function testAnEngineWithNoImplementationIsRefused(): void
    {
        $http = new MockHttpClient();
        $factory = $this->factory($http);

        // Numero gravado a mao, ou gravado antes de a porta existir: a fabrica e a ultima
        // guarda, e recusa antes de qualquer chamada de rede.
        $asset = $this->asset('sess-antiga', [
            SessionDriverFactory::SETTING_ENGINE   => SessionDriverFactory::ENGINE_BAILEYS,
            SessionDriverFactory::SETTING_BASE_URI => 'http://127.0.0.1:8099',
            SessionDriverFactory::SETTING_TOKEN    => 'selado:'.base64_encode('token-qualquer'),
        ]);

        $refusal = null;
        try {
            $factory->forAsset($asset);
        } catch (\DomainException $thrown) {
            $refusal = $thrown;
        }

        self::assertInstanceOf(\DomainException::class, $refusal, 'motor sem implementacao nao pode devolver adaptador nenhum');
        self::assertStringContainsString(SessionDriverFactory::ENGINE_BAILEYS, $refusal->getMessage());
        self::assertSame(0, $http->getRequestsCount(), 'a recusa nao chega a falar com servico nenhum');
    }

    public function testTheRefusalHappensWhenConfiguring(): void
    {
        $http = new MockHttpClient();
        $factory = $this->factory($http);

        // A pergunta que a tela de configuracao faz nao precisa de asset, de endereco nem
        // de token: um motor pode ser recusado antes de existir numero para ele. Se a
        // unica recusa fosse a do forAsset(), ela chegaria com um cliente esperando.
        self::assertSame([SessionDriverFactory::ENGINE_WHATSMEOW], $factory->implementedEngines());
        self::assertContains(SessionDriverFactory::ENGINE_BAILEYS, $factory->offeredEngines(), 'opcao listada e nao implementada e honesta; o que nao pode e aceitar');

        $listedButRefused = null;
        try {
            $factory->assertEngineIsImplemented(SessionDriverFactory::ENGINE_BAILEYS);
        } catch (\DomainException $thrown) {
            $listedButRefused = $thrown;
        }
        self::assertInstanceOf(\DomainException::class, $listedButRefused);
        self::assertStringContainsString('nao tem implementacao', $listedButRefused->getMessage(), 'o motivo vai escrito, porque quem le e quem esta configurando');

        // E a gravacao passa pela mesma porta: escolher baileys nao grava metade da
        // configuracao para estourar depois no envio.
        $asset = $this->asset('sess-nova', ['contact_match_field' => 'mobile']);
        $before = $asset->getSettings();

        $refusal = null;
        try {
            $factory->configure($asset, SessionDriverFactory::ENGINE_BAILEYS, 'http://127.0.0.1:8099', 'token-novo', 'segredo-novo');
        } catch (\DomainException $thrown) {
            $refusal = $thrown;
        }

        self::assertInstanceOf(\DomainException::class, $refusal);
        self::assertSame($before, $asset->getSettings(), 'nada foi gravado: a recusa vem antes da escrita, e nao depois dela');
        self::assertSame(0, $http->getRequestsCount());

        // Motor que nem na lista esta tambem e recusado aqui, e nao no envio.
        $this->expectException(\DomainException::class);
        $factory->assertEngineIsImplemented('wppconnect');
    }

    public function testTheTokenAndTheWebhookSecretAreStoredSealed(): void
    {
        $http = new MockHttpClient(new MockResponse(
            json_encode(['id' => 'sess-nova', 'status' => 'connected'], JSON_THROW_ON_ERROR),
            ['http_code' => 200, 'response_headers' => ['content-type: application/json']]
        ));
        $factory = $this->factory($http);
        $asset = $this->asset('sess-nova');

        $factory->configure($asset, SessionDriverFactory::ENGINE_WHATSMEOW, 'http://127.0.0.1:8088/', 'token-em-claro', 'segredo-em-claro');
        $settings = $asset->getSettings();

        // Quem le o banco, um dump ou a tela de MCP que devolve `settings` nao pode sair
        // dali sabendo falar pelo numero.
        self::assertStringNotContainsString('token-em-claro', json_encode($settings, JSON_THROW_ON_ERROR));
        self::assertStringNotContainsString('segredo-em-claro', json_encode($settings, JSON_THROW_ON_ERROR));
        self::assertSame('token-em-claro', $this->vault()->open($settings[SessionDriverFactory::SETTING_TOKEN]));
        self::assertSame('segredo-em-claro', $this->vault()->open($settings[SessionDriverFactory::SETTING_WEBHOOK_SECRET]));
        self::assertSame(SessionDriverFactory::ENGINE_WHATSMEOW, $settings[SessionDriverFactory::SETTING_ENGINE]);

        // E o adaptador recebe o token aberto, que e o que prova que o selo serve para
        // guardar e nao para atrapalhar.
        $factory->forAsset($asset)->openSession($asset);
        self::assertSame(1, $http->getRequestsCount());
    }

    public function testANumberWithoutAServiceAddressIsRefused(): void
    {
        $factory = $this->factory(new MockHttpClient());
        $asset = $this->asset('sess-solta', [SessionDriverFactory::SETTING_ENGINE => SessionDriverFactory::ENGINE_WHATSMEOW]);

        // Sem endereco nao ha a quem pedir nada, e isso nao melhora com o tempo:
        // \DomainException e o que a fila le como definitiva.
        $this->expectException(\DomainException::class);
        $factory->forAsset($asset);
    }

    public function testAnAssetOfAnotherChannelIsRefused(): void
    {
        $factory = $this->factory(new MockHttpClient());
        $graph = (new MetaAsset())->setType(AssetType::WhatsAppPhoneNumber)->setExternalId('123456')->setName('Oficial');

        // Um numero do Graph passando por aqui sairia pelo servico nao homologado sem que
        // ninguem tivesse pedido isso.
        $this->expectException(\DomainException::class);
        $factory->forAsset($graph);
    }
}
