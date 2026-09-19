<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Application;

use Doctrine\Common\Collections\ArrayCollection;
use Doctrine\Common\Collections\Criteria;
use MauticPlugin\MauticMetaBundle\Domain\AssetType;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticMetaBundle\Entity\MetaAssetRepository;
use MauticPlugin\MauticMetaBundle\Entity\MetaConnection;
use MauticPlugin\MauticMetaBundle\Entity\MetaMessage;
use MauticPlugin\MauticMetaBundle\Entity\MetaMessageRepository;
use MauticPlugin\MauticMetaBundle\Entity\MetaOutboundJob;
use MauticPlugin\MauticMetaBundle\Entity\MetaOutboundJobRepository;
use MauticPlugin\MauticWhatsQrBundle\Application\ConnectionsOverview;
use MauticPlugin\MauticWhatsQrBundle\Domain\ConnectionRow;
use MauticPlugin\MauticWhatsQrBundle\Domain\SessionState;
use PHPUnit\Framework\TestCase;

/**
 * As quatro colunas da tela de Conexoes, e o que cada uma nao pode errar.
 *
 * O repositorio de jobs nao e dublado com uma lista fixa: `matching()` roda sobre uma
 * ArrayCollection de verdade, que usa o mesmo visitante de criterios do Doctrine. Dublar
 * a consulta afirmaria que a tela chamou o repositorio -- nao que ela conta os jobs
 * certos, que e a unica coisa que separa "3 clientes esperando" de um numero inventado.
 */
final class ConnectionsOverviewTest extends TestCase
{
    /** @var list<MetaAsset> */
    private array $assets = [];

    /** @var list<MetaOutboundJob> */
    private array $queue = [];

    /** @var array<int, list<MetaMessage>> */
    private array $messages = [];

    private int $nextId = 1;

    /**
     * @param array<string, mixed> $settings
     */
    private function asset(AssetType $type, string $name, array $settings = [], ?string $number = null): MetaAsset
    {
        $asset = (new MetaAsset($this->nextId++))
            ->setType($type)
            ->setExternalId('sess-'.strtolower($name))
            ->setName($name)
            ->setStatus('active')
            ->setPhoneNumber($number)
            ->setSettings($settings)
            ->setConnection(new MetaConnection(1));
        $this->assets[] = $asset;

        return $asset;
    }

    private function job(MetaAsset $asset, string $status = 'pending'): MetaOutboundJob
    {
        $job = (new MetaOutboundJob($this->nextId++))
            ->setAsset($asset)
            ->setOperation('whatsapp_text')
            ->setPayload(['to' => '5531999990000', 'text' => 'ja vou verificar'])
            ->setStatus($status);
        $this->queue[] = $job;

        return $job;
    }

    private function message(MetaAsset $asset, string $direction, string $when): MetaMessage
    {
        $message = (new MetaMessage())
            ->setAsset($asset)
            ->setChannel('whatsapp')
            ->setDirection($direction)
            ->setMessageType('text')
            ->setRecipient('5531999990000')
            ->setStatus('delivered')
            ->setDateAdded(new \DateTimeImmutable($when));
        $this->messages[(int) $asset->getId()][$direction][] = $message;

        return $message;
    }

    private function overview(): ConnectionsOverview
    {
        $jobs = $this->createMock(MetaOutboundJobRepository::class);
        $jobs->method('matching')->willReturnCallback(
            fn (Criteria $criteria) => (new ArrayCollection($this->queue))->matching($criteria)
        );

        $assets = $this->createMock(MetaAssetRepository::class);
        $assets->method('findBy')->willReturnCallback(
            fn (array $by): array => array_values(array_filter(
                $this->assets,
                static fn (MetaAsset $asset): bool => $asset->getType()->value === ($by['type'] ?? null)
            ))
        );

        $messages = $this->createMock(MetaMessageRepository::class);
        $messages->method('findLatestForAssetDirection')->willReturnCallback(
            function (MetaAsset $asset, string $direction): ?MetaMessage {
                $found = $this->messages[(int) $asset->getId()][$direction] ?? [];

                return [] === $found ? null : end($found);
            }
        );

        return new ConnectionsOverview($assets, $jobs, $messages);
    }

    /**
     * @param list<ConnectionRow> $rows
     */
    private function rowOf(array $rows, string $name): ConnectionRow
    {
        foreach ($rows as $row) {
            if ($row->name === $name) {
                return $row;
            }
        }
        self::fail(sprintf('A tela nao trouxe a linha de "%s".', $name));
    }

    /**
     * A coluna que o desenho diz nao ser decoracao: e como alguem descobre que tres
     * clientes estao esperando.
     */
    public function testTheQueueColumnCountsTheRepliesWaitingOnThatNumber(): void
    {
        $down = $this->asset(AssetType::WhatsAppQrSession, 'Comercial', [SessionState::SETTING_STATUS => SessionState::RECONNECTING]);
        $this->job($down);
        $this->job($down, 'retry');
        // Na mao de um worker agora e um cliente esperando do mesmo jeito: deixa-lo de
        // fora faria a coluna piscar de 3 para 2 no segundo em que uma resposta comeca a
        // sair.
        $this->job($down, 'processing');
        // Desfechos nao contam: a resposta ja saiu, ou ja parou.
        $this->job($down, 'completed');
        $this->job($down, 'failed');

        $row = $this->rowOf($this->overview()->rows(), 'Comercial');

        self::assertSame(3, $row->queued);
        self::assertFalse($row->queuedCapped);
    }

    /**
     * O guarda-corpo dos tres canais oficiais em producao: a fila deles passa pela mesma
     * tabela, e soma-la aqui diria ao atendente que um numero por QR esta represando
     * respostas que na verdade estao saindo normalmente por outro canal.
     */
    public function testTheQueueOfTheOfficialChannelsIsNotCounted(): void
    {
        $qr = $this->asset(AssetType::WhatsAppQrSession, 'Comercial', [SessionState::SETTING_STATUS => SessionState::CONNECTED]);
        $official = $this->asset(AssetType::WhatsAppPhoneNumber, 'Numero homologado');
        $this->job($qr);
        $this->job($official);
        $this->job($official, 'retry');

        $rows = $this->overview()->rows();

        self::assertCount(1, $rows, 'so numero por QR entra nesta tela');
        self::assertSame(1, $rows[0]->queued);
    }

    /**
     * Numero sem estado gravado e um caso de verdade -- nunca pareou, ou o plugin subiu
     * antes do servico.
     *
     * "Conectado" seria a tela discordando da fila sobre o mesmo numero:
     * `ExpireQueuedCommand` pula quem nao tem estado justamente porque ausencia nao e
     * prova de nada.
     */
    public function testANumberWithNoRecordedStateIsUnknownAndNotConnected(): void
    {
        $this->asset(AssetType::WhatsAppQrSession, 'Recem-criado');

        $row = $this->rowOf($this->overview()->rows(), 'Recem-criado');

        self::assertSame(ConnectionRow::UNKNOWN, $row->situation);
        self::assertNotSame(SessionState::CONNECTED, $row->situation);
        // Desconhecido nao e um pedido de socorro: ninguem deve ser chamado por causa de
        // um numero que ainda nao pareou.
        self::assertFalse($row->needsSomebody);
    }

    public function testTheSituationComesFromTheRecordedState(): void
    {
        $this->asset(AssetType::WhatsAppQrSession, 'Suporte', [
            SessionState::SETTING_STATUS => SessionState::CONNECTED,
            SessionState::SETTING_JID    => '5531999990000@s.whatsapp.net',
        ], '+55 31 7544-1171');

        $row = $this->rowOf($this->overview()->rows(), 'Suporte');

        self::assertSame(SessionState::CONNECTED, $row->situation);
        self::assertSame('+55 31 7544-1171', $row->number);
        self::assertSame('5531999990000@s.whatsapp.net', $row->jid);
        self::assertFalse($row->needsSomebody);
    }

    /**
     * O caso do Passo 0, do lado da tela: duas credenciais no disco.
     *
     * Este estado NAO PODE estar gravado -- a sessao nunca abriu, logo nunca houve evento
     * de sessao. Sem a leitura do `/health` o numero apareceria com o ultimo estado bom
     * que teve antes do reinicio: verde, calado, e sem subir nunca.
     */
    public function testTheHealthAnswerOverridesAStaleRecordedStateForTwoCredentials(): void
    {
        $asset = $this->asset(AssetType::WhatsAppQrSession, 'Cobranca', [
            SessionState::SETTING_STATUS => SessionState::CONNECTED,
        ]);
        $live = [
            $asset->getExternalId() => new SessionState(
                $asset->getExternalId(),
                SessionState::AMBIGUOUS_CREDENTIAL,
                reason: 'session: mais de uma credencial para o mesmo numero: 5531999990000@s.whatsapp.net tem 2 credenciais no disco',
            ),
        ];

        $row = $this->rowOf($this->overview()->rows($live), 'Cobranca');

        self::assertSame(SessionState::AMBIGUOUS_CREDENTIAL, $row->situation);
        // Espera alguem, e nao tempo: nenhuma retentativa apaga do disco a segunda
        // credencial.
        self::assertTrue($row->needsSomebody);
        // O motivo por escrito, com os aparelhos que brigam pelo numero la dentro.
        self::assertStringContainsString('5531999990000@s.whatsapp.net', (string) $row->reason);
    }

    /**
     * O `/health` nao substitui o estado gravado nas outras situacoes: ele e a segunda
     * pergunta, feita so para o que a primeira nao pode saber. Duas fontes discordando
     * sobre "conectado" seriam duas verdades para a mesma pergunta -- e a fila decide pela
     * gravada.
     */
    public function testTheHealthAnswerDoesNotOverrideTheOtherSituations(): void
    {
        $asset = $this->asset(AssetType::WhatsAppQrSession, 'Comercial', [
            SessionState::SETTING_STATUS => SessionState::RECONNECTING,
        ]);
        $live = [$asset->getExternalId() => new SessionState($asset->getExternalId(), SessionState::CONNECTED)];

        $row = $this->rowOf($this->overview()->rows($live), 'Comercial');

        self::assertSame(SessionState::RECONNECTING, $row->situation);
        // Reconectando e esperar, e esperar e o certo a fazer: uma tela que pede socorro
        // a cada oscilacao de linha deixa de ser lida.
        self::assertFalse($row->needsSomebody);
    }

    public function testAnUnpairedNumberAsksForSomebody(): void
    {
        $this->asset(AssetType::WhatsAppQrSession, 'Cobranca', [SessionState::SETTING_STATUS => SessionState::LOGGED_OUT]);

        self::assertTrue($this->rowOf($this->overview()->rows(), 'Cobranca')->needsSomebody);
    }

    /**
     * A ultima mensagem e nas duas direcoes: a pergunta e "este numero deu sinal de
     * vida?", e um numero que so recebe esta tao vivo quanto um que so responde.
     */
    public function testTheLastMessageIsTheMostRecentInEitherDirection(): void
    {
        $asset = $this->asset(AssetType::WhatsAppQrSession, 'Suporte', [SessionState::SETTING_STATUS => SessionState::CONNECTED]);
        $this->message($asset, 'outbound', '2026-09-18 10:00:00');
        $this->message($asset, 'inbound', '2026-09-19 08:30:00');

        $row = $this->rowOf($this->overview()->rows(), 'Suporte');

        self::assertNotNull($row->lastMessageAt);
        self::assertSame('2026-09-19 08:30:00', $row->lastMessageAt->format('Y-m-d H:i:s'));
    }

    /**
     * A fila maior que o teto de leitura da tela.
     *
     * Um numero pequeno e errado nesta coluna e pior do que nenhum: quem le "12
     * esperando" fecha a tela achando que sabe o tamanho do problema. A marca vai em
     * todas as linhas porque o corte e por id crescente -- um numero que voltou zero pode
     * ter a fila inteira do outro lado do corte, e ele e justamente o pior caso.
     */
    public function testACountThatHitTheReadLimitIsMarkedAsAFloor(): void
    {
        $busy = $this->asset(AssetType::WhatsAppQrSession, 'Comercial', [SessionState::SETTING_STATUS => SessionState::LOGGED_OUT]);
        $quiet = $this->asset(AssetType::WhatsAppQrSession, 'Suporte', [SessionState::SETTING_STATUS => SessionState::CONNECTED]);
        for ($i = 0; $i < 600; ++$i) {
            $this->job($busy);
        }

        $rows = $this->overview()->rows();

        self::assertTrue($this->rowOf($rows, 'Comercial')->queuedCapped);
        self::assertTrue($this->rowOf($rows, 'Suporte')->queuedCapped, 'o numero que contou zero pode estar do outro lado do corte');
        self::assertSame(0, $this->rowOf($rows, 'Suporte')->queued);
    }

    public function testANumberThatNeverSpokeHasNoLastMessage(): void
    {
        $this->asset(AssetType::WhatsAppQrSession, 'Recem-criado');

        self::assertNull($this->rowOf($this->overview()->rows(), 'Recem-criado')->lastMessageAt);
    }
}
