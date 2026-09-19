<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Command;

use Doctrine\Common\Collections\Criteria;
use Doctrine\ORM\EntityManagerInterface;
use MauticPlugin\MauticMetaBundle\Application\Support\InboxIntegrationInterface;
use MauticPlugin\MauticMetaBundle\Domain\AssetType;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticMetaBundle\Entity\MetaAssetRepository;
use MauticPlugin\MauticMetaBundle\Entity\MetaOutboundJob;
use MauticPlugin\MauticMetaBundle\Entity\MetaOutboundJobRepository;
use MauticPlugin\MauticWhatsQrBundle\Domain\SessionState;
use Symfony\Component\Console\Attribute\AsCommand;
use Symfony\Component\Console\Command\Command;
use Symfony\Component\Console\Input\InputInterface;
use Symfony\Component\Console\Input\InputOption;
use Symfony\Component\Console\Output\OutputInterface;

/**
 * A outra metade do canal que cai: uma resposta nao pode ficar na fila para sempre.
 *
 * A primeira metade ja existe -- ChannelTemporarilyUnavailable mantem o job na fila e a
 * OutboundQueue o reagenda com teto de duas horas, para a resposta sair sozinha quando o
 * numero voltar. Sem esta varredura, o numero que nao volta deixa a resposta esperando
 * indefinidamente, e o atendente le "na fila" de algo que nunca vai sair.
 *
 * Responder "ja vou verificar" seis horas depois e pior que nao responder: o cliente ja
 * desistiu, ja ligou, ja resolveu de outro jeito, e recebe uma mensagem que o faz reviver
 * o problema. Entao o job passa a falha permanente com o motivo por escrito, e a caixa
 * mostra "nao saiu" -- que e diferente de "na fila", e essa distincao e o desenho inteiro.
 */
#[AsCommand(
    name: 'mautic:whatsqr:queue:expire',
    description: 'Expira respostas paradas na fila de um numero de WhatsApp por QR Code que esta desconectado.',
)]
final class ExpireQueuedCommand extends Command
{
    /**
     * As duas horas.
     *
     * Constante, e nao parametro em `Config/config.php`. Aquele arquivo e lido por
     * include cru antes de existir container, entao ele nao consegue referenciar
     * constante nenhuma: o numero teria de ser repetido como literal la. E ele ja e um
     * literal repetido -- o teto de recuo da OutboundQueue
     * (TEMPORARY_CHANNEL_BACKOFF_CAP_SECONDS) e as mesmas duas horas, e os dois numeros
     * so fazem sentido juntos. Uma terceira copia, editavel por uma tela que ninguem
     * abre, e a copia que envelhece calada: quem a subisse para seis horas nao subiria o
     * teto de recuo junto, e o job ficaria quatro horas parado sem ninguem tentar nada.
     *
     * O numero das duas horas e discutivel; o comportamento de expirar nao e. Por isso a
     * janela tambem entra por argumento de comando: quem discorda escreve o valor na
     * linha do cron, onde ele fica a vista de quem lê o agendamento, em vez de ficar
     * guardado num campo de configuracao que nao aparece em lugar nenhum.
     */
    public const DEFAULT_WINDOW_MINUTES = 120;

    /**
     * Estados de onde um job ainda pode sair -- e portanto os unicos que faz sentido
     * expirar. `processing` fica de fora de proposito, apesar de nao ser terminal: ele
     * esta na mao de um worker agora, e marcar "nao saiu" enquanto o envio acontece
     * produziria a pior linha possivel na tela, a de uma mensagem entregue dada como
     * perdida. Job travado de verdade ja tem dono: a recuperacao de travados da
     * OutboundQueue o passa a `uncertain` depois de quinze minutos.
     */
    private const EXPIRABLE_STATUSES = ['pending', 'retry'];

    /**
     * Teto de jobs por rodada, para a varredura nunca carregar a fila inteira na memoria
     * de um processo de cron. O que sobrar expira na rodada seguinte -- um minuto depois,
     * que e irrelevante diante de uma janela de duas horas.
     */
    private const SWEEP_LIMIT = 500;

    public function __construct(
        private readonly MetaOutboundJobRepository $jobs,
        private readonly MetaAssetRepository $assets,
        private readonly EntityManagerInterface $entityManager,
        private readonly InboxIntegrationInterface $inbox,
    ) {
        parent::__construct();
    }

    protected function configure(): void
    {
        $this->addOption(
            'older-than',
            null,
            InputOption::VALUE_REQUIRED,
            'Idade minima do job, em minutos, para ele deixar de esperar.',
            (string) self::DEFAULT_WINDOW_MINUTES,
        );
    }

    protected function execute(InputInterface $input, OutputInterface $output): int
    {
        $expired = $this->sweep(new \DateTimeImmutable(), (int) $input->getOption('older-than'));
        $output->writeln(json_encode([
            'expired' => count($expired),
            'jobs'    => array_map(static fn (MetaOutboundJob $job): ?int => $job->getId(), $expired),
        ], JSON_THROW_ON_ERROR));

        return Command::SUCCESS;
    }

    /**
     * Passa a "nao saiu" o que esperou demais por um numero caido, e devolve o que passou.
     *
     * O relogio entra por argumento, e nao e lido aqui dentro, para o teste poder varrer
     * "tres horas depois" sem envelhecer um `dateAdded` que a entidade grava sozinha.
     *
     * @return list<MetaOutboundJob>
     */
    public function sweep(\DateTimeImmutable $now, int $windowMinutes = self::DEFAULT_WINDOW_MINUTES): array
    {
        $disconnected = $this->disconnectedNumbers();
        if ([] === $disconnected) {
            // Nenhum numero caido, nenhuma consulta na fila. Este e o caso normal, e ele
            // custa uma leitura de no maximo cinco assets.
            return [];
        }

        // A idade e medida do `dateAdded`, e nao do `availableAt` nem da ultima
        // tentativa. O que se promete ao cliente comeca quando o atendente aperta enviar;
        // `availableAt` anda para a frente a cada recuo, entao contar dele faria o prazo
        // se afastar justamente no caso que esta varredura existe para cortar -- um numero
        // que fica caindo nunca completaria duas horas "desde a ultima tentativa".
        $cutoff = $now->modify(sprintf('-%d minutes', max(0, $windowMinutes)));
        $criteria = Criteria::create()
            ->where(Criteria::expr()->in('status', self::EXPIRABLE_STATUSES))
            ->andWhere(Criteria::expr()->lt('dateAdded', $cutoff))
            ->orderBy(['id' => 'ASC'])
            ->setMaxResults(self::SWEEP_LIMIT);

        // O tipo do asset nao entra no criterio porque ele mora do outro lado de uma
        // juncao, e `Criteria` so enxerga campos da propria entidade. O filtro fica em
        // PHP logo abaixo, sobre uma lista curta: job parado ha mais de duas horas em
        // `pending` ou `retry` e raro nos canais oficiais, que batem `maxAttempts` em
        // poucos minutos. E raro nao e nenhum -- por isso o filtro e por asset conhecido
        // de QR, e nao por exclusao: a fila dos tres canais da Meta que estao em producao
        // passa por aqui e precisa sair intacta.
        $expired = [];
        foreach ($this->jobs->matching($criteria) as $job) {
            $state = $disconnected[(int) $job->getAsset()->getId()] ?? null;
            if (null === $state) {
                continue;
            }

            $job->setStatus('failed')
                ->setLockedAt(null)
                ->setLastError($this->reason($job, $state, $now));
            $this->entityManager->persist($job);
            $expired[] = $job;
        }

        if ([] === $expired) {
            return [];
        }

        $this->entityManager->flush();
        foreach ($expired as $job) {
            $this->notifyInbox($job);
        }

        return $expired;
    }

    /**
     * Os numeros por QR que estao caidos agora, por id do asset, com o estado gravado.
     *
     * A pergunta e feita ao estado gravado em `settings`, e nao ao servico em Go. Custo:
     * a varredura roda a cada minuto e cada consulta ao servico e uma ida e volta HTTP com
     * teto de dez segundos -- e ela justamente acontece quando o servico esta fora do ar,
     * entao cada numero caido custaria os dez segundos inteiros e o cron ficaria pendurado
     * no exato momento em que precisa ser rapido. Coerencia: o estado gravado e o mesmo
     * que o compositor da caixa le, entao expirar por ele mantem a tela e a fila contando
     * a mesma historia; perguntar ao servico criaria uma segunda verdade.
     *
     * O preco e o estado velho, e ele foi pago para o lado seguro: o webhook de sessao e
     * quem grava, e enquanto ele nao chega o numero conta como desconhecido. Numero sem
     * estado gravado nao entra nesta lista -- ausencia de estado nao e prova de queda, e a
     * fila tambem para quando o worker morre, o que prenderia os canais oficiais junto.
     *
     * @return array<int, string>
     */
    private function disconnectedNumbers(): array
    {
        $disconnected = [];
        foreach ($this->assets->findBy(['type' => AssetType::WhatsAppQrSession->value]) as $asset) {
            if (!$asset instanceof MetaAsset || null === $asset->getId()) {
                continue;
            }

            $state = trim((string) ($asset->getSettings()[SessionState::SETTING_STATUS] ?? ''));
            // Caido e tudo que nao e `connected`: pareando, reconectando, deslogado ou
            // falho. A lista e por exclusao de proposito -- um estado novo que apareca no
            // desenho entra como "nao da para enviar", que e a leitura segura; a lista
            // positiva deixaria passar calada a palavra que ninguem lembrou de acrescentar.
            if ('' === $state || SessionState::CONNECTED === $state) {
                continue;
            }

            $disconnected[(int) $asset->getId()] = $state;
        }

        return $disconnected;
    }

    /**
     * O motivo que o atendente vai ler, em JSON com a chave `message`.
     *
     * O formato nao e enfeite: a caixa decodifica `lastError` e mostra `message` (ver
     * MetaInboxIntegration::jobError()). Texto cru apareceria como blob na tela, e e a
     * mesma forma que a OutboundQueue ja grava nas outras falhas.
     */
    private function reason(MetaOutboundJob $job, string $state, \DateTimeImmutable $now): string
    {
        $waited = max(0, (int) floor(($now->getTimestamp() - $job->getDateAdded()->getTimestamp()) / 60));

        return json_encode([
            'message' => sprintf(
                'Esta resposta esperou %d minutos na fila e nao saiu: o numero "%s" esta desconectado (%s). '
                .'Uma resposta que chega horas depois e pior do que nenhuma, entao ela para aqui. '
                .'Reenvie quando o numero reconectar, ou responda por outro numero.',
                $waited,
                $job->getAsset()->getName(),
                $state,
            ),
        ], JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_THROW_ON_ERROR);
    }

    private function notifyInbox(MetaOutboundJob $job): void
    {
        try {
            $this->inbox->outboundJobChanged($job);
        } catch (\Throwable) {
            // A caixa e projecao de apoio: uma falha ao avisa-la nao pode desfazer nem
            // interromper a varredura, senao um job expirado no banco ficaria sem os
            // seguintes so porque a tela tropecou.
        }
    }
}
