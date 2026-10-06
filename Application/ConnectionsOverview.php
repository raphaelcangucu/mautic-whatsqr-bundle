<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Application;

use Doctrine\Common\Collections\Criteria;
use MauticPlugin\MauticMetaBundle\Domain\AssetType;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticMetaBundle\Entity\MetaAssetRepository;
use MauticPlugin\MauticMetaBundle\Entity\MetaMessage;
use MauticPlugin\MauticMetaBundle\Entity\MetaMessageRepository;
use MauticPlugin\MauticMetaBundle\Entity\MetaOutboundJob;
use MauticPlugin\MauticMetaBundle\Entity\MetaOutboundJobRepository;
use MauticPlugin\MauticWhatsQrBundle\Domain\ConnectionRow;
use MauticPlugin\MauticWhatsQrBundle\Domain\SessionState;

/**
 * As linhas da tela de Conexoes: numero, situacao, quantas respostas estao na fila, e a
 * ultima mensagem.
 *
 * A coluna da fila nao e decoracao. Quando a sessao cai, a resposta do atendente continua
 * entrando na fila e fica parada, e o desenho quer isso: ela sai sozinha quando o numero
 * voltar. O preco e que ninguem descobre que tres clientes estao esperando -- a caixa
 * mostra "na fila" conversa por conversa, e ninguem abre cinco conversas para somar. Este
 * numero e o unico lugar onde a soma aparece.
 *
 * Fica separado do controlador porque e aqui que mora a aritmetica e a escolha da
 * palavra, e template nao se testa por unidade.
 */
final class ConnectionsOverview
{
    /**
     * Os estados de onde uma resposta ainda pode sair -- e portanto o que conta como "na
     * fila" para quem le a tela.
     *
     * `processing` entra, ao contrario do que a varredura que expira faz. A varredura o
     * deixa de fora porque marcar "nao saiu" enquanto o envio acontece produziria a pior
     * linha possivel na caixa; aqui nao se marca nada, so se conta, e um job que esta na
     * mao de um worker continua sendo um cliente esperando. Deixa-lo de fora faria "3
     * respostas esperando" virar "2" no segundo em que uma delas comeca a sair, e a
     * coluna existe para dizer o tamanho da espera, nao para piscar.
     */
    private const WAITING_STATUSES = ['pending', 'retry', 'processing'];

    /**
     * Teto de jobs lidos por carga da tela.
     *
     * A fila dos tres canais oficiais que estao em producao passa por esta mesma tabela,
     * e uma tela que carrega a fila inteira na memoria do PHP e uma tela que deixa de
     * abrir justamente no dia ruim. O que passar do teto vira `queuedCapped`, e a tela
     * escreve "500+" em vez de mentir um numero exato.
     */
    private const SWEEP_LIMIT = 500;

    public function __construct(
        private readonly MetaAssetRepository $assets,
        private readonly MetaOutboundJobRepository $jobs,
        private readonly MetaMessageRepository $messages,
        private readonly ?ConnectionStatistics $statistics = null,
    ) {
    }

    /**
     * @param array<string, SessionState> $liveStates o que o servico respondeu no `/health`, por id de sessao
     *
     * @return list<ConnectionRow>
     */
    public function rows(array $liveStates = []): array
    {
        $numbers = $this->qrAssets();
        if ([] === $numbers) {
            return [];
        }

        $stats = $this->statistics?->forAssets(array_keys($numbers));
        $queued = null === $stats ? $this->queuedByAsset(array_keys($numbers)) : ['counts' => $stats['queued'], 'capped' => false];
        $counts = $queued['counts'];
        // O teto vale para a tela inteira, e nao so para as linhas que contaram alguma
        // coisa: o corte e por id crescente, entao um numero que voltou zero pode ter a
        // fila inteira do outro lado do corte. Marcar so quem contou faria justamente
        // esse numero -- o pior caso -- aparecer como "nada parado".
        $capped = $queued['capped'];

        $rows = [];
        foreach ($numbers as $assetId => $asset) {
            $live = $liveStates[$asset->getExternalId()] ?? null;
            $situation = $this->situation($asset, $live);

            $rows[] = new ConnectionRow(
                assetId: $assetId,
                name: $asset->getName(),
                number: $this->number($asset),
                situation: $situation,
                needsSomebody: SessionState::needsSomebody($situation),
                reason: $live?->reason,
                queued: $counts[$assetId] ?? 0,
                queuedCapped: $capped,
                lastMessageAt: null === $stats ? $this->lastMessageAt($asset) : ($stats['last'][$assetId] ?? null),
                jid: $this->jid($asset, $live),
            );
        }

        return $rows;
    }

    /**
     * Os numeros por QR, por id do asset.
     *
     * @return array<int, MetaAsset>
     */
    private function qrAssets(): array
    {
        $numbers = [];
        foreach ($this->assets->findBy(['type' => AssetType::WhatsAppQrSession->value], ['name' => 'ASC']) as $asset) {
            if ($asset instanceof MetaAsset && null !== $asset->getId()) {
                $numbers[$asset->getId()] = $asset;
            }
        }

        return $numbers;
    }

    /**
     * A palavra que vai na coluna da situacao.
     *
     * Vem do estado gravado em `settings`, e nao do servico: e o mesmo que a varredura da
     * fila le, entao a tela e a fila contam a mesma historia sobre o mesmo numero.
     * Perguntar ao servico a situacao de cada linha criaria uma segunda verdade -- e uma
     * ida e volta HTTP de dez segundos por numero, justamente quando o servico esta fora
     * do ar, que e quando esta tela e aberta.
     *
     * A excecao e AMBIGUOUS_CREDENTIAL, e ela nao e arbitraria: esse estado NAO PODE
     * estar gravado, porque ele descreve uma sessao que nunca abriu -- sem sessao nao ha
     * evento de sessao, e sem evento nao ha o que gravar. Ele so existe na resposta do
     * `/health`, e sem esta linha o numero apareceria com o ultimo estado bom que ele
     * teve, antes do reinicio: verde, calado, e sem subir nunca.
     */
    private function situation(MetaAsset $asset, ?SessionState $live): string
    {
        if (null !== $live) {
            return $live->status;
        }

        $recorded = trim((string) ($asset->getSettings()[SessionState::SETTING_STATUS] ?? ''));

        // Numero sem estado gravado e um caso de verdade: nunca pareou, ou o plugin subiu
        // antes do servico. "Desconhecido", e nunca "conectado" -- ver ConnectionRow::UNKNOWN.
        return '' === $recorded ? ConnectionRow::UNKNOWN : $recorded;
    }

    /**
     * Quantas respostas estao esperando em cada numero.
     *
     * A consulta e a mesma da varredura que expira (ExpireQueuedCommand::sweep): um
     * criterio por estado, com teto, e o filtro por asset feito em PHP logo depois. O tipo
     * do asset mora do outro lado de uma juncao e `Criteria` so enxerga campos da propria
     * entidade -- e, aqui, filtrar por asset conhecido tambem e o que garante que a fila
     * dos tres canais da Meta que estao em producao nao apareca somada nesta tela.
     *
     * @param list<int> $assetIds
     *
     * @return array{counts: array<int, int>, capped: bool}
     */
    private function queuedByAsset(array $assetIds): array
    {
        $wanted = array_flip($assetIds);
        $criteria = Criteria::create()
            ->where(Criteria::expr()->in('status', self::WAITING_STATUSES))
            ->orderBy(['id' => 'ASC'])
            ->setMaxResults(self::SWEEP_LIMIT);

        $counts = [];
        $seen = 0;
        foreach ($this->jobs->matching($criteria) as $job) {
            ++$seen;
            if (!$job instanceof MetaOutboundJob) {
                continue;
            }
            $assetId = (int) $job->getAsset()->getId();
            if (!isset($wanted[$assetId])) {
                continue;
            }
            $counts[$assetId] = ($counts[$assetId] ?? 0) + 1;
        }

        // Bateu o teto: toda contagem desta rodada e um piso, e nao um total.
        return ['counts' => $counts, 'capped' => $seen >= self::SWEEP_LIMIT];
    }

    /**
     * Quando este numero falou pela ultima vez, em qualquer direcao.
     *
     * As duas direcoes porque a pergunta e "este numero deu sinal de vida?", e um numero
     * que so recebe e tao vivo quanto um que so responde. Duas leituras por numero, com
     * ate cinco numeros, sobre o mesmo indice que a tela de saude do Meta bundle ja usa.
     */
    private function lastMessageAt(MetaAsset $asset): ?\DateTimeInterface
    {
        $latest = null;
        foreach (['inbound', 'outbound'] as $direction) {
            $message = $this->messages->findLatestForAssetDirection($asset, $direction);
            if (!$message instanceof MetaMessage) {
                continue;
            }
            $at = $message->getDateAdded();
            if (null === $latest || $at > $latest) {
                $latest = $at;
            }
        }

        return $latest;
    }

    private function number(MetaAsset $asset): ?string
    {
        $number = trim((string) $asset->getPhoneNumber());

        return '' === $number ? null : $number;
    }

    /**
     * O chip com que o numero pareou.
     *
     * O gravado tem preferencia sobre o do servico porque e ele que a tela precisa dizer
     * quando pede para escanear de novo: "escaneie de novo" sem o chip por escrito nao
     * diz com qual celular.
     */
    private function jid(MetaAsset $asset, ?SessionState $live): ?string
    {
        $recorded = trim((string) ($asset->getSettings()[SessionState::SETTING_JID] ?? ''));
        if ('' !== $recorded) {
            return $recorded;
        }

        return $live?->jid;
    }
}
