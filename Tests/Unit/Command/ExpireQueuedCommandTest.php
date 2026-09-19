<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Command;

use Doctrine\Common\Collections\ArrayCollection;
use Doctrine\Common\Collections\Criteria;
use Doctrine\ORM\EntityManagerInterface;
use MauticPlugin\MauticMetaBundle\Application\Support\InboxIntegrationInterface;
use MauticPlugin\MauticMetaBundle\Domain\AssetType;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticMetaBundle\Entity\MetaAssetRepository;
use MauticPlugin\MauticMetaBundle\Entity\MetaConnection;
use MauticPlugin\MauticMetaBundle\Entity\MetaOutboundJob;
use MauticPlugin\MauticMetaBundle\Entity\MetaOutboundJobRepository;
use MauticPlugin\MauticWhatsQrBundle\Command\ExpireQueuedCommand;
use MauticPlugin\MauticWhatsQrBundle\Domain\SessionState;
use PHPUnit\Framework\TestCase;
use Symfony\Component\Console\Tester\CommandTester;

/**
 * A varredura que transforma resposta parada em "nao saiu", e o que ela nao pode tocar.
 *
 * O teste nao dubla a consulta: o repositorio falso devolve `matching()` avaliado por
 * uma ArrayCollection de verdade, que e o mesmo visitante de criterios que o Doctrine
 * usa. Dublar `matching()` com uma lista fixa afirmaria que o comando chamou o
 * repositorio -- nao que o filtro de estado e de idade escolhe os jobs certos, que e a
 * unica coisa que separa esta tarefa de uma que apaga a fila inteira.
 *
 * O relogio entra por argumento em vez de o job nascer velho: `dateAdded` e gravado no
 * construtor da entidade e nao tem setter, entao envelhecer um job pediria reflexao
 * sobre atributo privado de outro bundle. Varrer "tres horas depois" diz a mesma coisa
 * sem essa muleta.
 */
final class ExpireQueuedCommandTest extends TestCase
{
    /**
     * @var list<MetaOutboundJob>
     */
    private array $queue = [];

    /**
     * @var list<MetaAsset>
     */
    private array $assets = [];

    /**
     * Os jobs de que a caixa foi avisada, na ordem.
     *
     * @var list<MetaOutboundJob>
     */
    private array $inboxCalls = [];

    private int $nextId = 1;

    private function asset(AssetType $type, string $name, ?string $recordedState): MetaAsset
    {
        $settings = null === $recordedState ? [] : [SessionState::SETTING_STATUS => $recordedState];
        $asset = (new MetaAsset($this->nextId++))
            ->setType($type)
            ->setExternalId('sess-'.$name)
            ->setName($name)
            ->setStatus('active')
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
            ->setPayload(['to' => '5511999999999', 'text' => 'ja vou verificar'])
            ->setStatus($status);
        $this->queue[] = $job;

        return $job;
    }

    private function command(): ExpireQueuedCommand
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

        $inbox = $this->createMock(InboxIntegrationInterface::class);
        $inbox->method('outboundJobChanged')->willReturnCallback(function (MetaOutboundJob $job): void {
            $this->inboxCalls[] = $job;
        });

        return new ExpireQueuedCommand($jobs, $assets, $this->createMock(EntityManagerInterface::class), $inbox);
    }

    private function threeHoursLater(): \DateTimeImmutable
    {
        return (new \DateTimeImmutable())->modify('+3 hours');
    }

    private function reasonOf(MetaOutboundJob $job): string
    {
        // A caixa le `lastError` como JSON e mostra `message`; ver
        // MetaInboxIntegration::jobError(). Ler pelo mesmo caminho e o que prova que o
        // atendente vai enxergar o motivo, e nao um blob.
        $decoded = json_decode((string) $job->getLastError(), true, 16, JSON_THROW_ON_ERROR);
        self::assertIsArray($decoded);
        self::assertArrayHasKey('message', $decoded);

        return (string) $decoded['message'];
    }

    public function testAJobQueuedForOverTwoHoursBecomesFailed(): void
    {
        $job = $this->job($this->asset(AssetType::WhatsAppQrSession, 'Atendimento', SessionState::LOGGED_OUT));

        $expired = $this->command()->sweep($this->threeHoursLater());

        self::assertSame([$job], $expired);
        self::assertSame('failed', $job->getStatus());
        // "nao saiu" e um desfecho, nao um job a meio caminho: deixar o lock aceso faria
        // a recuperacao de travados mexer nele de novo quinze minutos depois.
        self::assertNull($job->getLockedAt());
    }

    public function testTheReasonSaysTheNumberWasDisconnected(): void
    {
        $job = $this->job($this->asset(AssetType::WhatsAppQrSession, 'Atendimento', SessionState::LOGGED_OUT));

        $this->command()->sweep($this->threeHoursLater());

        $reason = $this->reasonOf($job);
        self::assertStringContainsStringIgnoringCase('desconectado', $reason);
        // O nome do numero entra porque a empresa tem mais de um: "o numero caiu" nao
        // diz ao atendente qual chip reconectar.
        self::assertStringContainsString('Atendimento', $reason);
    }

    public function testAJobOfAConnectedNumberIsLeftAlone(): void
    {
        $connected = $this->job($this->asset(AssetType::WhatsAppQrSession, 'Vendas', SessionState::CONNECTED));
        $down = $this->job($this->asset(AssetType::WhatsAppQrSession, 'Atendimento', SessionState::LOGGED_OUT));

        $expired = $this->command()->sweep($this->threeHoursLater());

        self::assertSame([$down], $expired);
        self::assertSame('pending', $connected->getStatus());
        self::assertNull($connected->getLastError());
        self::assertSame([$down], $this->inboxCalls);
    }

    public function testAJobWithoutAQrAssetIsLeftAlone(): void
    {
        // O guarda-corpo dos tres canais oficiais em producao: uma consulta sem filtro de
        // tipo varreria a fila inteira e transformaria em falha o que ia sair normalmente.
        $official = $this->job($this->asset(AssetType::WhatsAppPhoneNumber, 'Numero homologado', null));
        $instagram = $this->job($this->asset(AssetType::InstagramAccount, 'Perfil', null));
        $this->job($this->asset(AssetType::WhatsAppQrSession, 'Atendimento', SessionState::LOGGED_OUT));

        $this->command()->sweep($this->threeHoursLater());

        self::assertSame('pending', $official->getStatus());
        self::assertSame('pending', $instagram->getStatus());
    }

    public function testANumberThatCameBackBeforeTheSweepIsLeftAlone(): void
    {
        // O estado e lido na varredura, nao no enfileiramento: entre uma coisa e outra o
        // numero pode ter voltado, e o job esta prestes a sair na proxima rodada da fila.
        $job = $this->job($this->asset(AssetType::WhatsAppQrSession, 'Atendimento', SessionState::CONNECTED));

        self::assertSame([], $this->command()->sweep($this->threeHoursLater()));
        self::assertSame('pending', $job->getStatus());
    }

    public function testANumberWithNoRecordedStateIsLeftAlone(): void
    {
        // Ausencia de estado nao e prova de queda. Um numero que nunca gravou estado pode
        // estar de pe com a fila parada por outro motivo -- worker desligado, por exemplo,
        // que prende tambem os canais oficiais. Expirar so o QR ai seria escolher a vitima.
        $job = $this->job($this->asset(AssetType::WhatsAppQrSession, 'Recem-configurado', null));

        self::assertSame([], $this->command()->sweep($this->threeHoursLater()));
        self::assertSame('pending', $job->getStatus());
    }

    public function testAJobYoungerThanTheWindowIsLeftAlone(): void
    {
        $job = $this->job($this->asset(AssetType::WhatsAppQrSession, 'Atendimento', SessionState::LOGGED_OUT));

        self::assertSame([], $this->command()->sweep((new \DateTimeImmutable())->modify('+1 hour')));
        self::assertSame('pending', $job->getStatus());
    }

    public function testAJobInATerminalStateIsNotTouched(): void
    {
        $asset = $this->asset(AssetType::WhatsAppQrSession, 'Atendimento', SessionState::LOGGED_OUT);
        $terminal = [];
        foreach (['completed', 'failed', 'blocked', 'uncertain'] as $status) {
            $terminal[$status] = $this->job($asset, $status);
        }
        // `processing` nao e terminal, mas esta na mao de um worker agora: mexer nele
        // enquanto o envio acontece produziria "nao saiu" de uma mensagem entregue.
        $terminal['processing'] = $this->job($asset, 'processing');

        self::assertSame([], $this->command()->sweep($this->threeHoursLater()));
        foreach ($terminal as $status => $job) {
            self::assertSame($status, $job->getStatus());
            self::assertNull($job->getLastError());
        }
        self::assertSame([], $this->inboxCalls);
    }

    public function testARetryingJobExpiresToo(): void
    {
        // O caso real: o job ja fracassou por canal indisponivel, esta em `retry` com
        // `availableAt` no futuro, e e exatamente ele que precisa parar de esperar.
        $job = $this->job($this->asset(AssetType::WhatsAppQrSession, 'Atendimento', SessionState::RECONNECTING), 'retry');

        self::assertSame([$job], $this->command()->sweep($this->threeHoursLater()));
        self::assertSame('failed', $job->getStatus());
    }

    public function testTheConsoleRunHonoursTheWindowGivenToIt(): void
    {
        $job = $this->job($this->asset(AssetType::WhatsAppQrSession, 'Atendimento', SessionState::LOGGED_OUT));
        $tester = new CommandTester($this->command());

        // Sem argumento, a janela padrao: o job acabou de nascer e nao expira.
        $tester->execute([]);
        self::assertSame('pending', $job->getStatus());
        self::assertStringContainsString('"expired":0', $tester->getDisplay());

        // Com janela zero, o operador esvazia agora o que e de numero caido.
        $tester->execute(['--older-than' => '0']);
        self::assertSame('failed', $job->getStatus());
        self::assertStringContainsString('"expired":1', $tester->getDisplay());
    }

    public function testTheWindowIsTwoHoursByDefault(): void
    {
        // O numero mora aqui, e nao em `Config/config.php`: ver o comentario no comando.
        self::assertSame(120, ExpireQueuedCommand::DEFAULT_WINDOW_MINUTES);
    }
}
