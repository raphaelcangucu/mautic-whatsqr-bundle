<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Application;

use MauticPlugin\MauticWhatsQrBundle\Domain\PairingView;
use MauticPlugin\MauticWhatsQrBundle\Domain\SessionState;

/**
 * A funcao que decide o que o cartao de parear mostra -- e, sobretudo, se ele oferece o
 * botao de tentar de novo.
 *
 * Existe separada do template porque template nao se testa por unidade, e a decisao
 * abaixo e exatamente a que nao pode estar errada. E a mesma regra da bolha pendente da
 * caixa: "na fila" e "nao saiu" sao duas frases porque a primeira ainda pode virar a
 * segunda, e confundi-las faz o atendente esperar por algo que nunca vem -- aqui, faz
 * ele apertar um botao que nunca resolve.
 */
final class PairingScreen
{
    /**
     * As palavras com que o servico conta que o codigo venceu sem ninguem escanear.
     *
     * Vem do desfecho do whatsmeow, repassado inteiro em `Reason` (ver followQR, em
     * service/session/whatsmeow.go, e o comentario que diz por que o motivo viaja como
     * texto e nao como codigo). A lista e curta de proposito: e ela que autoriza o botao.
     */
    private const EXPIRY_WORDS = ['timeout'];

    public function view(SessionState $state): PairingView
    {
        return match ($state->status) {
            SessionState::PAIRING => new PairingView(PairingView::WAITING, qr: $state->qr),

            // Reconectando e "pareou e caiu da linha", nao "nao pareou". A pergunta que
            // este cartao faz e "escaneou?", e a resposta ja e sim: mandar de volta para
            // o QR pediria um scan que o WhatsApp vai recusar, porque o chip ja esta
            // pareado. Se a linha nao voltar, quem conta isso e a tela de Conexoes.
            SessionState::CONNECTED, SessionState::RECONNECTING => new PairingView(
                PairingView::CONNECTED,
                jid: $state->jid,
            ),

            // O WhatsApp desfez o pareamento. Nada esta quebrado no numero: um scan novo
            // resolve, e por isso o botao aparece.
            SessionState::LOGGED_OUT => new PairingView(
                PairingView::NOT_DONE,
                jid: $state->jid,
                cause: PairingView::CAUSE_UNPAIRED,
                reason: $state->reason,
                offersRetry: true,
            ),

            // Duas credenciais no disco do servico. Gerar outro codigo nao apaga nenhuma
            // das duas: a saida e apagar a sessao, e ela esta escrita na tela de Conexoes.
            SessionState::AMBIGUOUS_CREDENTIAL => new PairingView(
                PairingView::NOT_DONE,
                cause: PairingView::CAUSE_AMBIGUOUS,
                reason: $state->reason,
            ),

            // `failed`, e qualquer palavra que este plugin venha a conhecer sem ninguem
            // lembrar desta tela. Cair aqui e a leitura segura: "nao deu", sem botao.
            default => $this->failed($state),
        };
    }

    /**
     * O servico nao respondeu -- conexao recusada, tempo esgotado, processo parado.
     *
     * Cartao de "nao deu" como os outros, com o botao: nao ha nada quebrado no numero, e
     * o gesto que resolve e literalmente tentar de novo quando o processo voltar. Mandar
     * isto pelo caminho da recusa esconderia o botao no unico caso em que ele resolve
     * sozinho.
     */
    public function canReset(SessionState $state): bool
    {
        $view = $this->view($state);
        return $view->offersRetry && in_array($view->cause, [PairingView::CAUSE_EXPIRED, PairingView::CAUSE_UNPAIRED], true);
    }

    public function serviceUnreachable(string $message): PairingView
    {
        return new PairingView(
            PairingView::NOT_DONE,
            cause: PairingView::CAUSE_SERVICE_DOWN,
            reason: '' === trim($message) ? null : trim($message),
            offersRetry: true,
        );
    }

    /**
     * A distincao que o desenho cobra: expirou pede o botao, recusado nunca resolve.
     *
     * Quando nao da para dizer qual dos dois e -- motivo vazio, palavra que este plugin
     * nao conhece --, a resposta e NAO oferecer. Os dois erros nao custam o mesmo: negar
     * o botao numa expiracao de verdade custa um clique a mais, voltar em Conexoes e
     * pedir para parear de novo; oferece-lo numa recusa poe o atendente repetindo para
     * sempre um gesto que nunca vai dar certo, e a tela some do caminho como fonte de
     * informacao. Na duvida, o erro barato.
     */
    private function failed(SessionState $state): PairingView
    {
        $reason = trim((string) $state->reason);
        // O motivo chega como "<desfecho>" ou "<desfecho>: <erro>" -- e o desfecho, a
        // primeira palavra, e a unica parte que o servico promete. Comparar a frase
        // inteira faria um erro de rede anexado ao "timeout" deixar de casar.
        $outcome = strtolower(trim(explode(':', $reason, 2)[0]));

        return new PairingView(
            PairingView::NOT_DONE,
            jid: $state->jid,
            cause: in_array($outcome, self::EXPIRY_WORDS, true) ? PairingView::CAUSE_EXPIRED : PairingView::CAUSE_REFUSED,
            reason: '' === $reason ? null : $reason,
            offersRetry: in_array($outcome, self::EXPIRY_WORDS, true),
        );
    }
}
