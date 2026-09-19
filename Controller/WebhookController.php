<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Controller;

use MauticPlugin\MauticMetaBundle\Application\Webhook\WebhookIngestor;
use MauticPlugin\MauticMetaBundle\Domain\AssetType;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticMetaBundle\Entity\MetaAssetRepository;
use MauticPlugin\MauticMetaBundle\Security\WebhookSignatureVerifier;
use MauticPlugin\MauticWhatsQrBundle\Application\InboundIngestor;
use MauticPlugin\MauticWhatsQrBundle\Domain\WebhookEventType;
use MauticPlugin\MauticWhatsQrBundle\Driver\SessionDriverFactory;
use Psr\Log\LoggerInterface;
use Symfony\Component\HttpFoundation\JsonResponse;
use Symfony\Component\HttpFoundation\Request;
use Symfony\Component\HttpFoundation\Response;

/**
 * A porta por onde o servico em Go entrega o que o WhatsApp avisou.
 *
 * E a unica rota publica deste plugin, e ela precisa ser publica: quem chama esta fora
 * do ciclo de requisicao do Mautic e nao tem sessao para o firewall reconhecer. Quem
 * autentica, entao, e a assinatura -- e por isso a ordem abaixo nao e detalhe de
 * implementacao, e o desenho:
 *
 *   cabecalho X-WhatsQr-Key -> o numero -> assinatura sobre timestamp+corpo -> o corpo
 *
 * Escolher de quem e a chave lendo o corpo seria confiar antes de verificar: o corpo, a
 * essa altura, ainda e texto que qualquer um pode ter escrito. O cabecalho tambem e,
 * mas ele so seleciona; quem prova e a assinatura feita com o segredo daquele numero.
 *
 * O contrato do outro lado esta escrito em service/webhook/sender.go, na funcao sign().
 */
final class WebhookController
{
    /**
     * Os tres cabecalhos, com os nomes exatos de webhook.HeaderKey e companhia.
     */
    public const HEADER_KEY = 'X-WhatsQr-Key';
    public const HEADER_TIMESTAMP = 'X-WhatsQr-Timestamp';
    public const HEADER_SIGNATURE = 'X-WhatsQr-Signature';

    /**
     * A janela do carimbo, em segundos, para cada lado do agora.
     *
     * Cinco minutos: larga o bastante para atravessar o relogio torto de um servidor e
     * as cinco tentativas com recuo do remetente, curta o bastante para que um POST
     * capturado deixe de valer antes de virar ferramenta. Sem ela, um `session:
     * logged_out` assinado uma vez e reenviado em laco manteria o canal marcado como
     * caido indefinidamente -- negacao de servico sem tocar no servico.
     */
    public const TOLERANCE_SECONDS = 300;

    /**
     * O que vai gravado em `object_type` na linha do evento.
     *
     * O corpo do servico nao tem o campo `object` que os webhooks do Graph tem, e sem
     * isto toda linha de numero por QR ficaria como "unknown" na mesma tabela que as da
     * Meta -- uma consulta para achar o que chegou por este canal nao teria por onde
     * comecar.
     */
    public const OBJECT_TYPE = 'whatsapp_qr_session';

    public function __construct(
        private readonly MetaAssetRepository $assets,
        private readonly SessionDriverFactory $drivers,
        private readonly WebhookSignatureVerifier $verifier,
        private readonly WebhookIngestor $ingestor,
        private readonly InboundIngestor $inbound,
        private readonly LoggerInterface $logger,
    ) {
    }

    public function handle(Request $request): Response
    {
        $key = trim((string) $request->headers->get(self::HEADER_KEY, ''));
        if ('' === $key) {
            return $this->refuse('pedido sem o cabecalho '.self::HEADER_KEY);
        }

        // O tipo entra na busca: um numero do Graph atendido por aqui receberia entrada
        // pelo servico nao homologado sem que ninguem tivesse pedido isso.
        $asset = $this->assets->findOneBy(['externalId' => $key, 'type' => AssetType::WhatsAppQrSession->value]);
        if (!$asset instanceof MetaAsset || !$asset->isPublished()) {
            return $this->refuse(sprintf('chave "%s" nao corresponde a nenhum numero por QR publicado', $key));
        }

        try {
            $secret = $this->drivers->webhookSecret($asset);
        } catch (\DomainException $exception) {
            // Numero configurado pela metade. Recusar e o certo -- aceitar sem conferir
            // seria transformar configuracao incompleta em porta aberta --, mas quem
            // opera precisa ver no log a diferenca entre isto e assinatura errada.
            return $this->refuse('numero sem segredo de webhook: '.$exception->getMessage());
        }

        $timestamp = trim((string) $request->headers->get(self::HEADER_TIMESTAMP, ''));
        $signature = trim((string) $request->headers->get(self::HEADER_SIGNATURE, ''));
        // Aqui o corpo e lido como bytes, nao como conteudo: sao exatamente os bytes que
        // foram assinados. Reserializar depois de decodificar reordena campo e muda
        // espaco, e a assinatura deixaria de bater por motivo nenhum.
        $body = (string) $request->getContent();

        // O verificador e o do Meta bundle porque e ali que mora o hash_equals. Um
        // segundo verificador escrito aqui e onde nasceria um === de string, que devolve
        // no tempo de resposta quantos caracteres do segredo o atacante ja acertou.
        if (!$this->verifier->verify($timestamp.$body, $signature, $secret)) {
            return $this->refuse(sprintf('assinatura invalida para a chave "%s"', $key));
        }

        // So agora o carimbo e um fato: ate a linha acima ele era um numero que qualquer
        // um podia ter escrito. E a assinatura cobrir timestamp E corpo que impede
        // reaproveitar um corpo capturado com um carimbo novo.
        if (!$this->isFresh($timestamp)) {
            return $this->refuse(sprintf('carimbo fora da janela de %d s para a chave "%s"', self::TOLERANCE_SECONDS, $key));
        }

        $payload = json_decode($body, true);
        if (!is_array($payload)) {
            return new JsonResponse(['received' => false, 'error' => 'Invalid JSON.'], Response::HTTP_BAD_REQUEST);
        }

        if ('' === trim((string) ($payload['id'] ?? ''))) {
            // O id e a chave do dedupe, e o servico da um a todo evento -- inclusive a
            // status e session, que nao teriam um natural. Sem ele nao ha o que
            // deduplicar, e aceitar assim mesmo seria gravar duas vezes a mesma queda.
            return new JsonResponse(['received' => false, 'error' => 'Event without id.'], Response::HTTP_BAD_REQUEST);
        }

        $type = trim((string) ($payload['type'] ?? ''));
        if (!WebhookEventType::isKnown($type)) {
            // Tipo desconhecido e um servico mais novo que o plugin. Gravar e responder
            // 200 deixaria o evento na tabela esperando um processador que nao existe, e
            // ninguem saberia; 400 faz o remetente registrar o descarte com o motivo.
            return new JsonResponse(['received' => false, 'error' => 'Unknown event type.'], Response::HTTP_BAD_REQUEST);
        }

        if (trim((string) ($payload['session_id'] ?? '')) !== $key) {
            // Corpo assinado pelo numero certo mas falando de outro. Nao deveria
            // acontecer, e e por isso mesmo que precisa ser recusado em voz alta: seguir
            // em frente faria a entrada de um numero cair na conversa de outro.
            return new JsonResponse(['received' => false, 'error' => 'Session mismatch.'], Response::HTTP_BAD_REQUEST);
        }

        $payload['object'] = self::OBJECT_TYPE;
        $ingested = $this->ingestor->ingest($asset->getConnection(), $payload);
        if (true === $ingested['duplicate']) {
            // Ja processado uma vez. Responder 200 e proposital: o remetente so para de
            // tentar com um 2xx, e insistir num evento que ja entrou prenderia atras
            // dele a fila de memoria do servico, que tem fim.
            return new JsonResponse(['received' => true, 'type' => $type, 'duplicate' => true]);
        }

        try {
            $this->inbound->ingest($asset, $payload);
        } catch (\Throwable $exception) {
            // O evento fica `failed`, e e assim que ele continua retentavel: o ingest() do
            // Meta bundle reabre um evento nesse estado quando o mesmo id volta. Marcar
            // como processado o que estourou perderia mensagem de cliente em silencio.
            $this->ingestor->complete((int) $ingested['eventId'], $exception);
            $this->logger->error(sprintf(
                'whatsqr: falha ao processar evento "%s" da chave "%s" -- %s',
                (string) $payload['id'],
                $key,
                $exception->getMessage()
            ));

            // 500 de proposito: o remetente so para de tentar com um 2xx, e este e o unico
            // caso em que insistir e o certo -- o corpo ja provou quem o assinou.
            return new JsonResponse(['received' => false, 'error' => 'Processing failed.'], Response::HTTP_INTERNAL_SERVER_ERROR);
        }

        $this->ingestor->complete((int) $ingested['eventId']);

        return new JsonResponse(['received' => true, 'type' => $type, 'duplicate' => false]);
    }

    /**
     * Toda recusa sai igual, e isso e a decisao.
     *
     * Chave desconhecida, numero despublicado, segredo faltando e assinatura errada
     * respondem o mesmo 401 com o mesmo texto. Um 404 para chave inexistente e um 401
     * para assinatura errada deixariam qualquer um enumerar os numeros da instalacao
     * batendo na rota. O motivo de verdade vai para o log, que e onde quem opera olha --
     * e o 401 nao e retentavel do outro lado, entao o servico para de insistir.
     */
    private function refuse(string $reason): JsonResponse
    {
        $this->logger->warning('whatsqr: webhook recusado -- '.$reason);

        return new JsonResponse(['received' => false, 'error' => 'Invalid signature.'], Response::HTTP_UNAUTHORIZED);
    }

    private function isFresh(string $timestamp): bool
    {
        // Epoch em segundos, base 10, como o remetente o escreve. Recusar o que nao tem
        // essa forma evita que " 12e9" ou "0x..." virem um inteiro qualquer no cast.
        if (1 !== preg_match('/^\d{1,12}$/', $timestamp)) {
            return false;
        }

        // Os dois lados da janela: para tras fecha o replay, para frente fecha o carimbo
        // do futuro, que sobreviveria a janela inteira se fosse aceito.
        return abs(time() - (int) $timestamp) <= self::TOLERANCE_SECONDS;
    }
}
