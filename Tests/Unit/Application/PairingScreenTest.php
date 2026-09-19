<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Application;

use MauticPlugin\MauticWhatsQrBundle\Application\PairingScreen;
use MauticPlugin\MauticWhatsQrBundle\Domain\PairingView;
use MauticPlugin\MauticWhatsQrBundle\Domain\SessionState;
use PHPUnit\Framework\TestCase;

/**
 * Um componente, tres estados -- e a decisao que esta tarefa existe para acertar: quando
 * a tela oferece o botao de gerar outro codigo.
 *
 * O template nao e testado aqui e nem poderia ser. O que e testado e a funcao que decide
 * o que ele mostra, que e onde o erro mora: um `{% if %}` no Twig escolhendo pela causa
 * passa a valer para toda causa nova sem ninguem reparar.
 */
final class PairingScreenTest extends TestCase
{
    private function view(string $status, ?string $qr = null, ?string $jid = null, ?string $reason = null): PairingView
    {
        return (new PairingScreen())->view(new SessionState('sess-1', $status, $qr, $jid, $reason));
    }

    public function testWaitingShowsTheCodeAndNothingElse(): void
    {
        $view = $this->view(SessionState::PAIRING, qr: 'codigo-de-agora');

        self::assertSame(PairingView::WAITING, $view->stage);
        self::assertSame('codigo-de-agora', $view->qr);
        self::assertFalse($view->offersRetry);
        self::assertNull($view->cause);
    }

    public function testAScannedNumberShowsTheChipItPairedWith(): void
    {
        $view = $this->view(SessionState::CONNECTED, jid: '5531999990000@s.whatsapp.net');

        self::assertSame(PairingView::CONNECTED, $view->stage);
        self::assertSame('5531999990000@s.whatsapp.net', $view->jid);
    }

    /**
     * Reconectando e "pareou e caiu da linha", nao "nao pareou".
     *
     * Mandar de volta para o QR pediria um scan que o WhatsApp recusa, porque o chip ja
     * esta pareado -- e o atendente ficaria apontando o celular para um codigo que nao
     * fecha, sem nada explicando por que.
     */
    public function testADropAfterTheScanStillCountsAsPaired(): void
    {
        $view = $this->view(SessionState::RECONNECTING, jid: '5531999990000@s.whatsapp.net');

        self::assertSame(PairingView::CONNECTED, $view->stage);
        self::assertFalse($view->offersRetry);
    }

    /**
     * A distincao que o desenho cobra, metade um: o codigo venceu sem ninguem escanear.
     * Nada foi criado, e outro codigo resolve.
     */
    public function testAnExpiredCodeOffersAnotherOne(): void
    {
        $view = $this->view(SessionState::FAILED, reason: 'timeout');

        self::assertSame(PairingView::NOT_DONE, $view->stage);
        self::assertSame(PairingView::CAUSE_EXPIRED, $view->cause);
        self::assertTrue($view->offersRetry);
    }

    /**
     * O motivo chega como "<desfecho>" ou "<desfecho>: <erro>". So o desfecho e promessa
     * do servico -- comparar a frase inteira faria um erro de rede grudado no "timeout"
     * deixar de casar, e o atendente perderia o botao numa expiracao comum.
     */
    public function testAnExpiredCodeWithTheErrorAppendedStillOffersAnotherOne(): void
    {
        $view = $this->view(SessionState::FAILED, reason: 'timeout: contexto cancelado');

        self::assertSame(PairingView::CAUSE_EXPIRED, $view->cause);
        self::assertTrue($view->offersRetry);
    }

    /**
     * Metade dois, e a que importa: o WhatsApp recusou. Isso nunca resolve, e oferecer o
     * botao ali faria o atendente repetir para sempre.
     */
    public function testARefusalByWhatsAppDoesNotOfferAnotherCode(): void
    {
        foreach ([
            'err-client-outdated',
            'err-scanned-without-multidevice',
            'a sessao foi assumida por outro cliente',
            'TemporaryBan: conta suspensa',
        ] as $reason) {
            $view = $this->view(SessionState::FAILED, reason: $reason);

            self::assertSame(PairingView::NOT_DONE, $view->stage, $reason);
            self::assertSame(PairingView::CAUSE_REFUSED, $view->cause, $reason);
            self::assertFalse($view->offersRetry, $reason);
            // O motivo continua na tela mesmo sem botao: "nao deu" sozinho nao diz a
            // ninguem o que fazer em seguida.
            self::assertSame($reason, $view->reason);
        }
    }

    /**
     * Quando nao da para dizer qual dos dois e, a resposta e nao oferecer.
     *
     * Os dois erros nao custam o mesmo: negar o botao numa expiracao de verdade custa um
     * clique a mais voltando em Conexoes; oferece-lo numa recusa poe o atendente
     * repetindo para sempre. Na duvida, o erro barato.
     */
    public function testAFailureWithNothingWrittenDoesNotOfferAnotherCode(): void
    {
        $view = $this->view(SessionState::FAILED);

        self::assertSame(PairingView::CAUSE_REFUSED, $view->cause);
        self::assertFalse($view->offersRetry);
        self::assertNull($view->reason);
    }

    /**
     * O WhatsApp desfez o pareamento. Nada esta quebrado no numero: um scan novo e
     * exatamente o que resolve.
     */
    public function testAnUnpairedNumberOffersAnotherCode(): void
    {
        $view = $this->view(SessionState::LOGGED_OUT, jid: '5531999990000@s.whatsapp.net');

        self::assertSame(PairingView::NOT_DONE, $view->stage);
        self::assertSame(PairingView::CAUSE_UNPAIRED, $view->cause);
        self::assertTrue($view->offersRetry);
        // O chip por escrito: "escaneie de novo" sem ele nao diz com qual celular.
        self::assertSame('5531999990000@s.whatsapp.net', $view->jid);
    }

    /**
     * Duas credenciais no disco do servico. Gerar outro codigo nao apaga nenhuma das
     * duas, entao o botao seria um convite a repetir um gesto inutil -- a saida e apagar
     * a sessao, e ela esta escrita na tela de Conexoes.
     */
    public function testTwoCredentialsForTheSameNumberDoNotOfferAnotherCode(): void
    {
        $view = $this->view(
            SessionState::AMBIGUOUS_CREDENTIAL,
            reason: 'session: mais de uma credencial para o mesmo numero: 5531999990000@s.whatsapp.net tem 2 credenciais no disco',
        );

        self::assertSame(PairingView::NOT_DONE, $view->stage);
        self::assertSame(PairingView::CAUSE_AMBIGUOUS, $view->cause);
        self::assertFalse($view->offersRetry);
    }
}
