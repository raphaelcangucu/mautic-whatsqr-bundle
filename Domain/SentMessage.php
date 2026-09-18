<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Domain;

/**
 * O que o plugin sabe de uma mensagem que saiu: o id que o WhatsApp deu a ela e o
 * request_id com que o Mautic a pediu.
 *
 * O request_id volta junto porque e por ele que o job da fila se amarra a esta saida;
 * sem ele, um log de envio nao se liga a resposta que o atendente digitou.
 */
final readonly class SentMessage
{
    public function __construct(
        public string $messageId,
        public string $requestId = '',
    ) {
        if ('' === trim($messageId)) {
            // A OutboundQueue exige o id para gravar em meta_messages, e um envio aceito
            // sem id e justamente o caso em que a mensagem pode ter saido: quem estourar
            // aqui precisa cair no braco "uncertain", que nao retenta as cegas.
            throw new \RuntimeException('O servico aceitou o envio sem devolver message_id.');
        }
    }
}
