<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Transport;

use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticMetaBundle\Infrastructure\WhatsAppTransportInterface;
use MauticPlugin\MauticWhatsQrBundle\Driver\SessionDriverFactory;

/**
 * A saida do canal por QR Code, vista pelo Meta bundle.
 *
 * O WhatsAppSender nao sabe que este plugin existe: ele monta um payload no formato do
 * Graph, pede um transporte ao TransportResolver pelo tipo do asset e chama post(). Tudo
 * o que e daqui para dentro -- id de sessao, endereco do processo em Go, token daquele
 * chip -- mora atras desta classe.
 *
 * A costura e o transporte, e nao o envio inteiro, porque antes de chegar aqui o sender
 * ja fez a guarda de conexao, a normalizacao do telefone, o DNC/opt-out e a cadencia, e
 * ja gravou o MetaMessage que a caixa le. Duplicar qualquer uma dessas coisas aqui
 * criaria uma segunda regra que envelhece separada da primeira -- e a que envelhece
 * calada e sempre a que deixa passar.
 *
 * Esta classe traduz nos dois sentidos. Na ida, o payload do Graph vira {to, text,
 * request_id}. Na volta, o {message_id} do servico vira messages[0].id, que e o unico
 * lugar onde o sender procura o id: devolver o corpo cru faria ele marcar como falha um
 * envio que ja saiu, e a caixa mostraria "nao saiu" de uma mensagem entregue.
 */
final class QrTransport implements WhatsAppTransportInterface
{
    /**
     * Prefixo do request_id derivado. Existe para que a linha do log do servico em Go
     * diga de onde veio aquele id -- um hash pelado nao se distingue de um id que a
     * caixa gerou.
     */
    private const REQUEST_ID_PREFIX = 'whatsqr-';

    /**
     * So a fabrica no construtor, e nenhum escalar.
     *
     * Endereco e token sao por numero e vivem em `settings` do asset: pedi-los aqui
     * obrigaria o autowire a adivinhar duas strings, o que quebra o COMPILE do container
     * -- e container que nao compila derruba o painel inteiro do Mautic, nao so este
     * plugin. Pior ainda se funcionasse: um transporte unico compartilhado falaria pelo
     * endereco do ultimo numero configurado. A fabrica resolve isso em tempo de execucao,
     * um adaptador por asset, com as credenciais daquele asset.
     */
    public function __construct(private readonly SessionDriverFactory $drivers, private readonly ?\MauticPlugin\MauticInboxBundle\Application\Mobile\AudioStore $audioStore = null)
    {
    }

    /**
     * @param array<string, mixed> $payload
     *
     * @return array<string, mixed>
     */
    public function post(MetaAsset $asset, array $payload): array
    {
        $type = trim((string) ($payload['type'] ?? ''));
        if($type==='audio'){
            $mediaId=$payload['audio']['id']??'';
            if(!is_string($mediaId)||!preg_match('/^inbox-audio:([a-f0-9]{32})$/D',$mediaId,$match)||!$this->audioStore)throw new \DomainException('Audio must be uploaded through the scoped Inbox API.');
            $record=$this->audioStore->record($match[1]);if($record['asset']!==(int)$asset->getId())throw new \DomainException('Audio belongs to another WhatsApp number.');
            $driver=$this->drivers->forAsset($asset);if(!$driver instanceof \MauticPlugin\MauticWhatsQrBundle\Driver\AudioSessionDriverInterface)throw new \DomainException('This QR driver does not support audio.');
            $to=trim((string)($payload['to']??''));if($to==='')throw new \InvalidArgumentException('Audio recipient required.');
            $sent=$driver->sendAudio($asset,$to,(string)file_get_contents($record['file']),'audio-'.substr(hash('sha256',$asset->getExternalId()."\0".$to."\0".$record['request_id']),0,32));
            return ['messages'=>[['id'=>$sent->messageId]],'request_id'=>$sent->requestId];
        }

        $this->assertTypeCanLeaveThisChannel($type);

        $to = trim((string) ($payload['to'] ?? ''));
        $text = (string) ($payload['text']['body'] ?? '');
        if ('' === $to || '' === trim($text)) {
            // Recusado aqui, e nao no servico. O Go responderia 400 "o corpo precisa de to
            // e text", que o adaptador traduz para a mesma falha definitiva -- mas so
            // depois de uma ida e volta e de uma linha de log num processo que fala por um
            // numero de verdade. InvalidArgumentException, e nao \DomainException: nada foi
            // recusado pelo canal; chegou um payload sem um campo que a forma do Graph
            // exige. Os dois sao definitivos na fila; a diferenca e para quem ler depois.
            throw new \InvalidArgumentException('Um envio por QR Code precisa de destinatario e de texto no payload do Graph.');
        }

        // preview_url fica de fora, e isto e uma perda consciente: o servico nao tem campo
        // para ela e o WhatsApp monta a previa sozinho a partir do link no corpo. Recusar
        // o envio por causa dela travaria uma resposta inteira por uma diferenca de
        // enfeite -- e a resposta e o que o cliente esta esperando.
        $requestId = $this->requestId($asset, $to, $text);
        $sent = $this->drivers->forAsset($asset)->sendText($asset, $to, $text, $requestId);

        // So o que o sender le, mais o request_id. Inventar `contacts` ou
        // `messaging_product` aqui seria fingir que o Graph respondeu; o request_id entra
        // porque e por ele que o log do Mautic se liga a linha do servico em Go, e este e
        // o unico ponto do caminho em que ele existe.
        return [
            'messages'   => [['id' => $sent->messageId]],
            'request_id' => $sent->requestId,
        ];
    }

    /**
     * A recusa do que este canal nao sabe mandar, com o motivo por escrito.
     */
    private function assertTypeCanLeaveThisChannel(string $type): void
    {
        if ('text' === $type) {
            return;
        }

        // \DomainException nos dois casos: a OutboundQueue a le como falha DEFINITIVA, e e
        // exatamente isso que se quer -- nenhuma retentativa vai fazer um template sair por
        // um canal que nao tem templates. Marcar como temporario poria a fila a retentar
        // para sempre um envio que nunca vai dar certo, e a mensagem certa para o atendente
        // e "nao saiu, mande de outro numero".
        if ('template' === $type) {
            // Template tem recusa propria porque o motivo dele e estrutural, e nao uma
            // funcao que falta: template aprovado e coisa do WhatsApp Business homologado,
            // e um numero por QR Code nao tem catalogo nenhum. Dizer so "tipo nao
            // suportado" faria alguem tentar implementar isto.
            throw new \DomainException(
                'Um numero por QR Code nao envia template: templates sao aprovados no WhatsApp Business homologado, '
                .'e este canal nao e homologado. Use um numero do Graph para o template, ou responda em texto dentro da janela de atendimento.'
            );
        }

        throw new \DomainException(sprintf(
            'Um numero por QR Code so envia texto; este envio pediu "%s". Para mandar "%s", use um numero do Graph.',
            $type,
            $type
        ));
    }

    /**
     * O request_id que o servico em Go recebe, derivado do proprio envio.
     *
     * O sender monta o payload sem nenhum identificador -- `messaging_product`,
     * `recipient_type`, `to`, `type` e o conteudo, so isso --, e o transporte nao ve o
     * job da fila. Entao o id e derivado de sessao + destino + texto, e nao sorteado: o
     * mesmo job retentado produz o mesmo id, que e o que faz a linha do log do servico se
     * ligar a retentativa que a produziu. Sorteado, cada retentativa pareceria um envio
     * novo justamente na hora em que alguem esta lendo o log para entender o que saiu.
     *
     * O preco: duas mensagens identicas para o mesmo destino compartilham o id. Hoje isso
     * nao esconde envio nenhum -- o servico nao deduplica por request_id, so o devolve e
     * o registra (ver service/api/routes.go). Se ele passar a deduplicar, esta derivacao
     * precisa virar o request_id de verdade do job, o que pede uma mudanca no Meta bundle
     * para o transporte enxergar a chave de idempotencia.
     *
     * O formato segue o que a caixa ja exige de um request_id seu ([A-Za-z0-9_-]{16,64}),
     * para este id poder ser reaproveitado la sem ser recusado.
     */
    private function requestId(MetaAsset $asset, string $to, string $text): string
    {
        return self::REQUEST_ID_PREFIX.substr(hash('sha256', implode("\0", [$asset->getExternalId(), $to, $text])), 0, 32);
    }
}
