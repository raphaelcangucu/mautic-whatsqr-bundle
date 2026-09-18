<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Driver;

use MauticPlugin\MauticMetaBundle\Application\Exception\ChannelTemporarilyUnavailable;
use MauticPlugin\MauticMetaBundle\Domain\AssetType;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticWhatsQrBundle\Domain\SessionState;
use MauticPlugin\MauticWhatsQrBundle\Driver\WhatsMeowDriver;
use PHPUnit\Framework\TestCase;
use Symfony\Component\HttpClient\Exception\TransportException;
use Symfony\Component\HttpClient\MockHttpClient;
use Symfony\Component\HttpClient\Response\MockResponse;

final class WhatsMeowDriverTest extends TestCase
{
    private function asset(string $sessionId = 'sess-a1b2c3'): MetaAsset
    {
        return (new MetaAsset())
            ->setType(AssetType::WhatsAppQrSession)
            ->setExternalId($sessionId)
            ->setName('Atendimento');
    }

    private function driver(MockHttpClient $http): WhatsMeowDriver
    {
        // Endereco e token chegam prontos: quem os le no numero e a fabrica da tarefa 3.
        return new WhatsMeowDriver($http, 'http://127.0.0.1:8088', 'token-do-numero');
    }

    private function json(array $body, int $status): MockResponse
    {
        return new MockResponse(json_encode($body, JSON_THROW_ON_ERROR), [
            'http_code'        => $status,
            'response_headers' => ['content-type: application/json'],
        ]);
    }

    public function testOpenSessionReturnsPairingWithAQr(): void
    {
        $http = new MockHttpClient(function (string $method, string $url, array $options): MockResponse {
            self::assertSame('POST', $method);
            self::assertSame('http://127.0.0.1:8088/sessions', $url);
            self::assertSame('Authorization: Bearer token-do-numero', $options['normalized_headers']['authorization'][0] ?? null);
            self::assertSame('{"id":"sess-a1b2c3"}', $options['body']);

            return $this->json(['id' => 'sess-a1b2c3', 'status' => 'pairing', 'qr' => '2@Xk9dL/qr'], 201);
        });

        $state = $this->driver($http)->openSession($this->asset());

        self::assertSame(SessionState::PAIRING, $state->status);
        self::assertTrue($state->isPairing());
        self::assertSame('2@Xk9dL/qr', $state->qr);
        self::assertSame('sess-a1b2c3', $state->sessionId);
    }

    public function testItTranslatesTheServiceDialectIntoSessionState(): void
    {
        $dialect = [
            'pairing'      => SessionState::PAIRING,
            'connected'    => SessionState::CONNECTED,
            'reconnecting' => SessionState::RECONNECTING,
            'logged_out'   => SessionState::LOGGED_OUT,
            'failed'       => SessionState::FAILED,
        ];

        foreach ($dialect as $fromService => $expected) {
            $http = new MockHttpClient($this->json(['id' => 'sess-a1b2c3', 'status' => $fromService], 201));
            $state = $this->driver($http)->openSession($this->asset());
            self::assertSame($expected, $state->status, sprintf('estado "%s" do servico', $fromService));
            self::assertNull($state->qr, 'sem QR no corpo, o objeto do plugin nao inventa um');
        }

        // Palavra que o plugin nao conhece nao vira um estado inventado: um motor que
        // passou a falar outro dialeto precisa aparecer aqui, e nao numa tela que mostra
        // "conectado" para uma sessao que ninguem sabe onde esta.
        $http = new MockHttpClient($this->json(['id' => 'sess-a1b2c3', 'status' => 'sleeping'], 201));
        $this->expectException(\RuntimeException::class);
        $this->driver($http)->openSession($this->asset());
    }

    public function testASendReturnsTheMessageId(): void
    {
        $http = new MockHttpClient(function (string $method, string $url, array $options): MockResponse {
            self::assertSame('POST', $method);
            self::assertSame('http://127.0.0.1:8088/sessions/sess-a1b2c3/messages', $url);
            self::assertSame('Authorization: Bearer token-do-numero', $options['normalized_headers']['authorization'][0] ?? null);
            self::assertSame(
                ['to' => '5511999999999', 'text' => 'Bom dia, Dona Marta', 'request_id' => 'job-77'],
                json_decode((string) $options['body'], true, 512, JSON_THROW_ON_ERROR)
            );

            return $this->json(['message_id' => '3EB0C431C26A1D5F', 'request_id' => 'job-77'], 200);
        });

        $sent = $this->driver($http)->sendText($this->asset(), '5511999999999', 'Bom dia, Dona Marta', 'job-77');

        self::assertSame('3EB0C431C26A1D5F', $sent->messageId);
        self::assertSame('job-77', $sent->requestId);
    }

    public function testAnUnreachableServiceRaisesChannelTemporarilyUnavailable(): void
    {
        // Servico fora do ar. Qualquer outra excecao faz a OutboundQueue classificar como
        // falha definitiva, e a resposta do atendente e descartada em dois segundos com
        // "nao saiu" -- quando o numero volta sozinho dali a pouco.
        // Recusa de conexao e tempo esgotado sao a mesma coisa vista de dois angulos: o
        // transporte do Symfony entrega as duas como TransportExceptionInterface.
        $transportFailures = [
            'Failed to connect to 127.0.0.1 port 8088: Connection refused',
            'Idle timeout reached for "http://127.0.0.1:8088/sessions/sess-a1b2c3/messages".',
        ];
        foreach ($transportFailures as $message) {
            $unreachable = new MockHttpClient(static function () use ($message): MockResponse {
                throw new TransportException($message);
            });
            try {
                $this->driver($unreachable)->sendText($this->asset(), '5511999999999', 'Ola', 'job-77');
                self::fail(sprintf('Falha de transporte passou sem virar falha temporaria: %s', $message));
            } catch (ChannelTemporarilyUnavailable $temporary) {
                self::assertStringNotContainsString('token-do-numero', $temporary->getMessage());
            }
        }

        // 503 e o que o servico responde com a sessao caida, e ele mesmo marca
        // "temporary": true. 500 e falha dele, que tambem nao e do envio.
        foreach ([500, 502, 503] as $status) {
            $down = new MockHttpClient($this->json(['error' => 'sessao desconectada', 'temporary' => true], $status));
            try {
                $this->driver($down)->sendText($this->asset(), '5511999999999', 'Ola', 'job-77');
                self::fail(sprintf('HTTP %d passou sem virar falha temporaria.', $status));
            } catch (ChannelTemporarilyUnavailable $temporary) {
                self::assertStringContainsString('sessao desconectada', $temporary->getMessage());
            }
        }

        // O outro lado da moeda, e e por isto que a classificacao existe: recusa do
        // proprio servico que nao muda sozinha nao pode voltar para a fila para sempre.
        foreach ([400, 404, 409] as $status) {
            $refused = new MockHttpClient($this->json(['error' => 'essa sessao nao esta aberta', 'temporary' => false], $status));
            try {
                $this->driver($refused)->sendText($this->asset(), '5511999999999', 'Ola', 'job-77');
                self::fail(sprintf('HTTP %d passou sem virar falha permanente.', $status));
            } catch (\DomainException $permanent) {
                self::assertStringContainsString('essa sessao nao esta aberta', $permanent->getMessage());
            }
        }
    }
}
