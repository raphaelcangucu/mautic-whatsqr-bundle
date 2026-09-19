<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Transport;

use Mautic\CoreBundle\Helper\CoreParametersHelper;
use Mautic\CoreBundle\Helper\EncryptionHelper;
use MauticPlugin\MauticMetaBundle\Domain\AssetType;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticMetaBundle\Infrastructure\WhatsAppTransportInterface;
use MauticPlugin\MauticMetaBundle\Security\CredentialVault;
use MauticPlugin\MauticWhatsQrBundle\Driver\SessionDriverFactory;
use MauticPlugin\MauticWhatsQrBundle\Transport\QrTransport;
use PHPUnit\Framework\TestCase;
use Symfony\Component\HttpClient\MockHttpClient;
use Symfony\Component\HttpClient\Response\MockResponse;

/**
 * O teste atravessa a fabrica e o adaptador de proposito, em vez de dublar os dois.
 *
 * A fabrica e final, e dubla-la exigiria uma interface criada so para o teste; mas o
 * motivo de verdade e outro: o que esta tarefa promete e que um payload do Graph vira
 * a chamada certa no servico em Go. Uma dublagem afirmaria que o transporte chamou
 * `sendText` -- nao que `{to, text, request_id}` chegou ao endereco daquele numero.
 */
final class QrTransportTest extends TestCase
{
    /**
     * @param array<string,mixed> $content
     *
     * @return array<string,mixed>
     */
    private function graphPayload(string $to, string $type, array $content): array
    {
        // A forma exata que o WhatsAppSender::send() monta antes de chamar o transporte.
        return ['messaging_product' => 'whatsapp', 'recipient_type' => 'individual', 'to' => $to, 'type' => $type] + $content;
    }

    private function vault(): CredentialVault
    {
        $encryption = $this->createMock(EncryptionHelper::class);
        $encryption->method('encrypt')->willReturnCallback(static fn ($plain): string => 'selado:'.base64_encode((string) $plain));
        $encryption->method('decrypt')->willReturnCallback(static fn ($sealed) => base64_decode(substr((string) $sealed, 7), true));

        return new CredentialVault($encryption);
    }

    private function transport(MockHttpClient $http): QrTransport
    {
        $parameters = $this->createMock(CoreParametersHelper::class);
        $parameters->method('get')->willReturnCallback(
            static fn (string $name, $fallback = null) => SessionDriverFactory::PARAMETER_DEFAULT_ENGINE === $name ? SessionDriverFactory::ENGINE_WHATSMEOW : $fallback
        );

        return new QrTransport(new SessionDriverFactory($http, $this->vault(), $parameters));
    }

    private function asset(MockHttpClient $http): MetaAsset
    {
        $asset = (new MetaAsset())
            ->setType(AssetType::WhatsAppQrSession)
            ->setExternalId('sess-atendimento')
            ->setName('Atendimento');

        $parameters = $this->createMock(CoreParametersHelper::class);
        $parameters->method('get')->willReturnCallback(static fn (string $name, $fallback = null) => $fallback);
        (new SessionDriverFactory($http, $this->vault(), $parameters))
            ->configure($asset, SessionDriverFactory::ENGINE_WHATSMEOW, 'http://127.0.0.1:8088', 'token-do-numero', 'segredo');

        return $asset;
    }

    public function testItTranslatesAGraphTextPayload(): void
    {
        $seen = [];
        $http = new MockHttpClient(function (string $method, string $url, array $options) use (&$seen): MockResponse {
            $seen[] = [$method, $url, $options];

            return new MockResponse(json_encode(['message_id' => '3EB0C431C26A1D9F', 'request_id' => 'devolvido'], JSON_THROW_ON_ERROR), [
                'http_code'        => 200,
                'response_headers' => ['content-type: application/json'],
            ]);
        });

        $transport = $this->transport($http);
        $asset = $this->asset($http);

        $response = $transport->post($asset, $this->graphPayload('5511998877665', 'text', [
            'text' => ['body' => 'Ja separei seu pedido.', 'preview_url' => false],
        ]));

        self::assertCount(1, $seen);
        [$method, $url, $options] = $seen[0];
        self::assertSame('POST', $method);
        self::assertSame('http://127.0.0.1:8088/sessions/sess-atendimento/messages', $url);
        self::assertSame('Authorization: Bearer token-do-numero', $options['normalized_headers']['authorization'][0] ?? null);

        $body = json_decode((string) $options['body'], true, 512, JSON_THROW_ON_ERROR);
        self::assertSame(['request_id', 'text', 'to'], $this->sortedKeys($body), 'o servico so conhece estes tres campos');
        self::assertSame('5511998877665', $body['to']);
        self::assertSame('Ja separei seu pedido.', $body['text']);
        self::assertNotSame('', trim((string) $body['request_id']));

        // A volta tambem e traducao: o WhatsAppSender le messages[0].id e trata vazio
        // como erro. Devolver {message_id} cru faria ele marcar como falha um envio que
        // saiu -- e a caixa mostraria "nao saiu" de uma mensagem ja entregue.
        self::assertSame('3EB0C431C26A1D9F', $response['messages'][0]['id'] ?? null);
        self::assertSame($body['request_id'], $response['request_id'] ?? null);
    }

    public function testATemplatePayloadIsRefusedWithAClearReason(): void
    {
        $http = new MockHttpClient(static function (): MockResponse {
            self::fail('um template nao pode chegar a sair pelo servico');
        });

        $transport = $this->transport($http);
        $asset = $this->asset($http);

        try {
            $transport->post($asset, $this->graphPayload('5511998877665', 'template', [
                'template' => ['name' => 'boas_vindas', 'language' => ['code' => 'pt_BR']],
            ]));
            self::fail('o transporte aceitou um template que este canal nao sabe mandar');
        } catch (\DomainException $refusal) {
            // \DomainException, e nao RuntimeException: a OutboundQueue le os dois tipos
            // do LogicException como falha DEFINITIVA, e e isso que se quer -- retentar
            // nunca vai fazer um template sair por um canal que nao tem templates.
            self::assertStringContainsStringIgnoringCase('template', $refusal->getMessage());
            // A recusa precisa dizer por que, e nao so que nao deu: quem le isto na caixa
            // e um atendente, que precisa saber que a saida e outro numero.
            self::assertMatchesRegularExpression('/QR|homologad|Graph/i', $refusal->getMessage());
        }
    }

    public function testEveryOtherGraphTypeIsRefusedByName(): void
    {
        $http = new MockHttpClient(static function (): MockResponse {
            self::fail('so texto sai por este canal');
        });
        $transport = $this->transport($http);
        $asset = $this->asset($http);

        foreach (['image' => ['image' => ['id' => '99']], 'interactive' => ['interactive' => ['type' => 'button']]] as $type => $content) {
            try {
                $transport->post($asset, $this->graphPayload('5511998877665', $type, $content));
                self::fail(sprintf('o transporte aceitou um payload do tipo "%s"', $type));
            } catch (\DomainException $refusal) {
                // O tipo recusado aparece na mensagem: "nao suportado" sem dizer o que
                // manda quem le a caixa abrir o log para descobrir o que tentou sair.
                self::assertStringContainsString($type, $refusal->getMessage());
            }
        }
    }

    public function testAPayloadWithoutATextBodyIsRefusedBeforeTheRoundTrip(): void
    {
        $http = new MockHttpClient(static function (): MockResponse {
            self::fail('corpo vazio nao precisa de ida e volta ao servico para ser recusado');
        });
        $transport = $this->transport($http);
        $asset = $this->asset($http);

        // InvalidArgumentException, e nao DomainException: aqui o canal nao recusou nada
        // -- o payload chegou sem um campo que a propria forma do Graph exige. Os dois
        // sao definitivos na fila; a diferenca e para quem for ler o erro depois.
        $this->expectException(\InvalidArgumentException::class);
        $transport->post($asset, $this->graphPayload('5511998877665', 'text', ['text' => ['preview_url' => true]]));
    }

    public function testTheRequestIdIsStableAcrossRetriesOfTheSameSend(): void
    {
        $bodies = [];
        $http = new MockHttpClient(function (string $method, string $url, array $options) use (&$bodies): MockResponse {
            $bodies[] = json_decode((string) $options['body'], true, 512, JSON_THROW_ON_ERROR);

            return new MockResponse(json_encode(['message_id' => 'wamid-'.count($bodies)], JSON_THROW_ON_ERROR), [
                'http_code'        => 200,
                'response_headers' => ['content-type: application/json'],
            ]);
        });
        $transport = $this->transport($http);
        $asset = $this->asset($http);

        $payload = $this->graphPayload('5511998877665', 'text', ['text' => ['body' => 'Ja separei seu pedido.', 'preview_url' => false]]);
        $transport->post($asset, $payload);
        // A fila retenta o MESMO job: o request_id precisa ser o mesmo, senao a linha do
        // log do servico em Go nao se liga a retentativa que a produziu.
        $transport->post($asset, $payload);
        $transport->post($asset, $this->graphPayload('5511998877665', 'text', ['text' => ['body' => 'Outra coisa.', 'preview_url' => false]]));

        self::assertSame($bodies[0]['request_id'], $bodies[1]['request_id']);
        self::assertNotSame($bodies[0]['request_id'], $bodies[2]['request_id']);
        // O formato e o mesmo que a caixa ja exige de um request_id seu: sem isso, um id
        // derivado aqui seria recusado no dia em que alguem o reaproveitasse la.
        self::assertMatchesRegularExpression('/^[A-Za-z0-9_-]{16,64}$/', (string) $bodies[0]['request_id']);
    }

    public function testItIsTheTransportSeamTheMetaBundleResolves(): void
    {
        // Sem isto, a etiqueta em Config/services.php registraria um objeto que o
        // TransportResolver nao tem como chamar, e a falha apareceria no primeiro envio.
        self::assertInstanceOf(WhatsAppTransportInterface::class, $this->transport(new MockHttpClient()));
    }

    /**
     * @param array<string,mixed> $body
     *
     * @return list<string>
     */
    private function sortedKeys(array $body): array
    {
        $keys = array_keys($body);
        sort($keys);

        return $keys;
    }
}
