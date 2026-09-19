<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Controller;

use Doctrine\ORM\EntityManagerInterface;
use Mautic\CoreBundle\Helper\CoreParametersHelper;
use Mautic\CoreBundle\Helper\EncryptionHelper;
use MauticPlugin\MauticMetaBundle\Application\Webhook\WebhookIngestor;
use MauticPlugin\MauticMetaBundle\Domain\AssetType;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticMetaBundle\Entity\MetaAssetRepository;
use MauticPlugin\MauticMetaBundle\Entity\MetaConnection;
use MauticPlugin\MauticMetaBundle\Entity\MetaWebhookEvent;
use MauticPlugin\MauticMetaBundle\Entity\MetaWebhookEventRepository;
use MauticPlugin\MauticMetaBundle\Security\CredentialVault;
use MauticPlugin\MauticMetaBundle\Security\WebhookSignatureVerifier;
use MauticPlugin\MauticWhatsQrBundle\Controller\WebhookController;
use MauticPlugin\MauticWhatsQrBundle\Driver\SessionDriverFactory;
use PHPUnit\Framework\TestCase;
use Psr\Log\NullLogger;
use Symfony\Component\HttpClient\MockHttpClient;
use Symfony\Component\HttpFoundation\Request;
use Symfony\Component\HttpFoundation\Response;

/**
 * A rota do webhook e a unica superficie deste plugin exposta a internet, e a
 * assinatura e a unica coisa entre ela e o mundo -- dai esta suite existir antes do
 * controlador. Tudo aqui e unitario de proposito: o que se afirma e a ordem das
 * recusas, e isso nao precisa de banco para ser verdade.
 */
final class WebhookControllerTest extends TestCase
{
    /**
     * As linhas que "ficaram gravadas", por chave de evento. Faz as vezes da tabela
     * meta_webhook_events: o que interessa afirmar e quantas linhas distintas o mesmo
     * evento produz, nao o SQL que as gravaria.
     *
     * @var array<string, MetaWebhookEvent>
     */
    private array $stored = [];

    /**
     * @var array<int, MetaWebhookEvent>
     */
    private array $storedById = [];

    private int $nextId = 1;

    /**
     * @var array<string, MetaAsset>
     */
    private array $numbers = [];

    protected function setUp(): void
    {
        $this->stored = [];
        $this->storedById = [];
        $this->nextId = 1;
        $this->numbers = [];
    }

    public function testAValidSignatureIsAccepted(): void
    {
        $this->number('sess-atendimento', 'segredo-atendimento');
        $body = $this->messageBody('msg:ABC123', 'sess-atendimento');

        $response = $this->controller()->handle($this->signedRequest('sess-atendimento', 'segredo-atendimento', $body));

        self::assertSame(Response::HTTP_OK, $response->getStatusCode());
        $decoded = json_decode((string) $response->getContent(), true);
        self::assertTrue($decoded['received']);
        // A porta reconhece o tipo e para ai: gravar conversa e mensagem e a tarefa 5.
        self::assertSame('message', $decoded['type']);
        self::assertCount(1, $this->stored);
    }

    public function testAnInvalidSignatureIsRefused(): void
    {
        $this->number('sess-atendimento', 'segredo-atendimento');
        $body = $this->messageBody('msg:ABC123', 'sess-atendimento');

        $response = $this->controller()->handle($this->signedRequest('sess-atendimento', 'segredo-que-nao-e-o-dele', $body));

        self::assertSame(Response::HTTP_UNAUTHORIZED, $response->getStatusCode());
        // Recusado e recusado: um evento que nao provou quem o assinou nao pode deixar
        // rastro na tabela, senao a propria fila de eventos vira porta de entrada.
        self::assertSame([], $this->stored);
    }

    public function testABodySignedWithAnotherNumbersKeyIsRefused(): void
    {
        // O segredo e por numero justamente para este caso: um servico comprometido, ou
        // um numero cujo segredo vazou, nao pode falar pelo numero do vizinho.
        $this->number('sess-atendimento', 'segredo-atendimento');
        $this->number('sess-cobranca', 'segredo-cobranca');
        $body = $this->messageBody('msg:ABC123', 'sess-atendimento');

        $response = $this->controller()->handle($this->signedRequest('sess-atendimento', 'segredo-cobranca', $body));

        self::assertSame(Response::HTTP_UNAUTHORIZED, $response->getStatusCode());
        self::assertSame([], $this->stored);
    }

    public function testAnOldTimestampIsRefused(): void
    {
        // Assinatura legitima, corpo legitimo, carimbo de dez minutos atras: e isto que
        // um POST capturado e reenviado em laco parece. Sem esta recusa, um
        // "session: logged_out" gravado uma vez mantem o canal marcado como caido para
        // sempre -- negacao de servico sem tocar no servico.
        $this->number('sess-atendimento', 'segredo-atendimento');
        $body = $this->sessionBody('session:deadbeef', 'sess-atendimento');
        $velho = (string) (time() - 600);

        $response = $this->controller()->handle(
            $this->signedRequest('sess-atendimento', 'segredo-atendimento', $body, $velho)
        );

        self::assertSame(Response::HTTP_UNAUTHORIZED, $response->getStatusCode());
        self::assertSame([], $this->stored);
    }

    public function testTheSameEventTwiceIsStoredOnce(): void
    {
        // A retentativa do servico em Go reenvia o mesmo corpo com carimbo novo: o
        // Mautic pode ter gravado e demorado a responder. O id que o servico da a cada
        // evento existe para que a segunda chegada nao vire uma segunda linha.
        $this->number('sess-atendimento', 'segredo-atendimento');
        $body = $this->messageBody('msg:ABC123', 'sess-atendimento');
        $controller = $this->controller();

        $primeira = $controller->handle($this->signedRequest('sess-atendimento', 'segredo-atendimento', $body));
        $segunda = $controller->handle($this->signedRequest('sess-atendimento', 'segredo-atendimento', $body));

        self::assertSame(Response::HTTP_OK, $primeira->getStatusCode());
        self::assertSame(Response::HTTP_OK, $segunda->getStatusCode());
        self::assertCount(1, $this->stored);
        self::assertTrue(json_decode((string) $segunda->getContent(), true)['duplicate']);
    }

    public function testAnUnknownKeyIdIsRefusedWithoutReadingTheBody(): void
    {
        // O corpo deste pedido estoura se alguem o ler. E a unica forma de afirmar a
        // ordem: cabecalho -> numero -> assinatura -> corpo. Escolher de quem e a chave
        // lendo o corpo nao conferido seria confiar antes de verificar.
        $this->number('sess-atendimento', 'segredo-atendimento');
        $request = new class('sess-fantasma') extends Request {
            public function __construct(string $key)
            {
                parent::__construct([], [], [], [], [], [
                    'REQUEST_METHOD'          => 'POST',
                    'HTTP_X_WHATSQR_KEY'      => $key,
                    'HTTP_X_WHATSQR_TIMESTAMP' => (string) time(),
                    'HTTP_X_WHATSQR_SIGNATURE' => 'sha256='.str_repeat('0', 64),
                ], null);
            }

            public function getContent(bool $asResource = false): mixed
            {
                throw new \LogicException('o corpo foi lido antes de saber de quem e a chave');
            }
        };

        $response = $this->controller()->handle($request);

        self::assertSame(Response::HTTP_UNAUTHORIZED, $response->getStatusCode());
        // Mesma resposta que a assinatura errada, de proposito: um 404 aqui e um 401 ali
        // deixariam qualquer um enumerar quais numeros existem batendo na rota.
        self::assertSame([], $this->stored);
    }

    // -----------------------------------------------------------------
    // Montagem
    // -----------------------------------------------------------------

    private function controller(): WebhookController
    {
        return new WebhookController(
            $this->assetRepository(),
            $this->factory(),
            // O verificador de verdade, e nao um duble: e nele que mora o hash_equals, e
            // um duble aqui deixaria de afirmar justamente a parte que importa.
            new WebhookSignatureVerifier(),
            $this->ingestor(),
            new NullLogger(),
        );
    }

    private function number(string $sessionId, string $secret): MetaAsset
    {
        $asset = (new MetaAsset())
            ->setType(AssetType::WhatsAppQrSession)
            ->setExternalId($sessionId)
            ->setName('Numero '.$sessionId)
            ->setConnection(new MetaConnection(1));
        $this->factory()->configure($asset, SessionDriverFactory::ENGINE_WHATSMEOW, 'http://127.0.0.1:8088', 'token-'.$sessionId, $secret);
        $this->numbers[$sessionId] = $asset;

        return $asset;
    }

    private function factory(): SessionDriverFactory
    {
        $parameters = $this->createMock(CoreParametersHelper::class);
        $parameters->method('get')->willReturnCallback(
            static fn (string $name, $fallback = null) => SessionDriverFactory::PARAMETER_DEFAULT_ENGINE === $name ? SessionDriverFactory::ENGINE_WHATSMEOW : $fallback
        );

        // Cifra de mentira, reversivel: o segredo do webhook viaja selado em `settings`,
        // e o controlador tem de abrir o cofre para chegar nele.
        $encryption = $this->createMock(EncryptionHelper::class);
        $encryption->method('encrypt')->willReturnCallback(static fn ($plain): string => 'selado:'.base64_encode((string) $plain));
        $encryption->method('decrypt')->willReturnCallback(static fn ($sealed) => base64_decode(substr((string) $sealed, 7), true));

        return new SessionDriverFactory(new MockHttpClient(), new CredentialVault($encryption), $parameters);
    }

    private function assetRepository(): MetaAssetRepository
    {
        $repository = $this->createMock(MetaAssetRepository::class);
        $repository->method('findOneBy')->willReturnCallback(function (array $criteria): ?MetaAsset {
            $asset = $this->numbers[(string) ($criteria['externalId'] ?? '')] ?? null;
            if (null === $asset) {
                return null;
            }

            // O tipo faz parte da busca: um numero do Graph nunca pode ser encontrado
            // por esta rota, que fala com o servico nao homologado.
            return AssetType::WhatsAppQrSession->value === ($criteria['type'] ?? null) ? $asset : null;
        });

        return $repository;
    }

    /**
     * O ingestor de verdade sobre um "banco" de memoria. Ele e quem deduplica, e o que
     * esta suite precisa afirmar e o comportamento dele com estes corpos -- nao um duble
     * que concordaria com qualquer coisa.
     */
    private function ingestor(): WebhookIngestor
    {
        $events = $this->createMock(MetaWebhookEventRepository::class);
        $events->method('findOneBy')->willReturnCallback(
            fn (array $criteria): ?MetaWebhookEvent => $this->stored[(string) ($criteria['eventKey'] ?? '')] ?? null
        );
        $events->method('find')->willReturnCallback(
            fn ($id): ?MetaWebhookEvent => $this->storedById[(int) $id] ?? null
        );

        $entityManager = $this->createMock(EntityManagerInterface::class);
        $entityManager->method('isOpen')->willReturn(true);
        $entityManager->method('persist')->willReturnCallback(function (object $entity): void {
            if (!$entity instanceof MetaWebhookEvent) {
                return;
            }
            if (null === $entity->getId()) {
                // O id vem do banco na vida real; sem ele o complete() nao acharia de
                // volta o evento que acabou de ser gravado.
                $identity = new \ReflectionProperty(MetaWebhookEvent::class, 'id');
                $identity->setValue($entity, $this->nextId++);
            }
            $this->stored[$entity->getEventKey()] = $entity;
            $this->storedById[(int) $entity->getId()] = $entity;
        });

        return new WebhookIngestor($entityManager, $events);
    }

    // -----------------------------------------------------------------
    // O que o servico em Go manda -- ver service/webhook/sender.go
    // -----------------------------------------------------------------

    private function messageBody(string $id, string $sessionId): string
    {
        return json_encode([
            'id'         => $id,
            'type'       => 'message',
            'session_id' => $sessionId,
            'state'      => 'connected',
            'message'    => [
                'id'        => 'ABC123',
                'from'      => '5511999999999@s.whatsapp.net',
                'text'      => 'bom dia',
                'timestamp' => time(),
            ],
        ], JSON_THROW_ON_ERROR);
    }

    private function sessionBody(string $id, string $sessionId): string
    {
        return json_encode([
            'id'         => $id,
            'type'       => 'session',
            'session_id' => $sessionId,
            'state'      => 'logged_out',
            'reason'     => 'sessao encerrada no aparelho',
        ], JSON_THROW_ON_ERROR);
    }

    /**
     * O mesmo formato de sender.go: sha256=hex(HMAC(segredo, timestamp+corpo)), com os
     * bytes exatos do corpo. Escrito aqui a mao, e nao pedido ao controlador, porque um
     * teste que assina com o codigo que confere nao afirma nada.
     */
    private function signedRequest(string $key, string $secret, string $body, ?string $timestamp = null): Request
    {
        $timestamp ??= (string) time();

        return Request::create('/whatsqr/webhook', 'POST', [], [], [], [
            'CONTENT_TYPE'             => 'application/json',
            'HTTP_X_WHATSQR_KEY'       => $key,
            'HTTP_X_WHATSQR_TIMESTAMP' => $timestamp,
            'HTTP_X_WHATSQR_SIGNATURE' => 'sha256='.hash_hmac('sha256', $timestamp.$body, $secret),
        ], $body);
    }
}
