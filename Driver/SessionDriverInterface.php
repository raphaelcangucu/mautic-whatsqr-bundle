<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Driver;

use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticWhatsQrBundle\Domain\SentMessage;
use MauticPlugin\MauticWhatsQrBundle\Domain\SessionState;

/**
 * O motor de um numero, visto de dentro do plugin.
 *
 * Quatro metodos, um por gesto que o plugin ja faz hoje: abrir para parear, mostrar o
 * QR enquanto ele dura, desligar, e mandar texto. Nao ha metodo aqui esperando um motor
 * futuro -- interface larga vira metodo vazio que alguem chama por engano.
 *
 * A escolha e por numero, e nao global: em canal nao homologado o comportamento varia
 * por chip, e um que comece a cair precisa trocar de motor sem derrubar os outros.
 *
 * Sobre o que cada implementacao lanca, porque disto depende o que o atendente ve:
 * canal fora do ar que volta sozinho e ChannelTemporarilyUnavailable; recusa que nao
 * muda sozinha e \DomainException. A OutboundQueue le os dois tipos, nao a mensagem.
 */
interface SessionDriverInterface
{
    public function openSession(MetaAsset $asset): SessionState;

    /**
     * O QR de agora, ou null quando a sessao nao esta em pareamento. Null e resposta, e
     * nao erro: a tela de Parear distingue "ainda nao chegou" de "ja conectou".
     */
    public function pairingQr(MetaAsset $asset): ?string;

    public function closeSession(MetaAsset $asset): void;

    public function sendText(MetaAsset $asset, string $to, string $text, string $requestId): SentMessage;
}
