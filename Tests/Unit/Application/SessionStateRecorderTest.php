<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Application;

use MauticPlugin\MauticWhatsQrBundle\Domain\SessionState;
use MauticPlugin\MauticWhatsQrBundle\Driver\SessionDriverFactory;
use MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Support\InboundIngestorFixture;
use PHPUnit\Framework\TestCase;

/**
 * O evento que ninguem gravava, e as tres coisas que dependiam dele.
 *
 * Ate esta suite existir, o evento `session` chegava a porta, era conferido, era gravado
 * como recebido -- e morria devolvendo nulo. O efeito nao aparecia em teste nenhum
 * porque nao havia o que afirmar: a varredura das duas horas nunca achava numero caido,
 * o compositor da caixa achava que estava tudo no ar, e a tela de Conexoes nao tinha
 * estado para mostrar. Codigo pronto, inerte, e verde.
 *
 * Os testes passam pelo InboundIngestor, e nao pelo gravador direto, de proposito: o que
 * estava quebrado era a costura -- o ponto onde a porta devolvia nulo --, e um teste que
 * chamasse so o gravador continuaria verde com aquela costura desfeita.
 */
final class SessionStateRecorderTest extends TestCase
{
    use InboundIngestorFixture;

    /**
     * As cinco palavras de service/session/state.go, e o que elas NAO podem tocar.
     */
    public function testEachStateTheServiceEmitsIsStored(): void
    {
        foreach (SessionState::all() as $state) {
            $asset = $this->qrAsset();

            $this->inboundIngestor()->ingest($asset, $this->sessionEvent($state));

            self::assertSame($state, $asset->getSettings()[SessionState::SETTING_STATUS] ?? null);

            // A armadilha do desenho, afirmada uma vez por estado. O estado vai para
            // `settings`; `status` do asset continua `active` mesmo com o numero no chao.
            // Gravar a queda em `status` fecharia o compositor da caixa e faria o retry
            // devolver 409 -- e o desenho quer o contrario: compositor aberto, resposta
            // indo para a fila, para sair sozinha quando o numero voltar.
            self::assertSame('active', $asset->getStatus());
        }
    }

    /**
     * Um estado que este plugin nao conhece e um servico mais novo que ele. O que nao
     * pode acontecer e a chave sumir.
     *
     * Sem a chave, `ExpireQueuedCommand::disconnectedNumbers()` pula o asset -- ausencia
     * de estado nao e prova de queda, e ele trata isso como desconhecido por seguranca.
     * Ou seja: a palavra nova apagaria o unico registro de que o numero caiu, e a
     * resposta ficaria na fila para sempre enquanto a tela dizia que estava tudo bem.
     */
    public function testAnUnknownStateDoesNotEraseWhatWasThere(): void
    {
        $asset = $this->qrAsset('sess-atendimento', [
            SessionDriverFactory::SETTING_TOKEN => 'tok-atendimento',
            SessionState::SETTING_STATUS => SessionState::RECONNECTING,
        ]);
        $ingestor = $this->inboundIngestor();

        $ingestor->ingest($asset, $this->sessionEvent('quarantined'));

        // A leitura da varredura, escrita como ela esta la: chave presente e diferente de
        // `connected` e o que a faz achar o numero. A palavra desconhecida entra como
        // "nao da para enviar", que e a leitura segura -- e a mesma escolha por exclusao
        // que o comando ja documenta.
        $state = $asset->getSettings()[SessionState::SETTING_STATUS] ?? '';
        self::assertNotSame('', $state);
        self::assertNotSame(SessionState::CONNECTED, $state);
        self::assertSame('quarantined', $state);

        // O resto de `settings` e um saco compartilhado: motor, endereco, token e segredo
        // moram nele. Reescrever o saco inteiro em vez da chave desaparelharia o numero.
        self::assertSame('tok-atendimento', $asset->getSettings()[SessionDriverFactory::SETTING_TOKEN] ?? null);

        // E o evento sem estado nenhum tambem nao apaga: um corpo pela metade nao e
        // noticia de que o numero voltou.
        $ingestor->ingest($asset, $this->sessionEvent(''));
        self::assertSame('quarantined', $asset->getSettings()[SessionState::SETTING_STATUS] ?? null);

        // E a palavra sem forma de estado tambem nao apaga -- este e o caso que a
        // implementacao recusa e nada afirmava. Apagar aqui seria o pior dos dois mundos:
        // a varredura pula asset sem chave, entao um corpo malformado desligaria em
        // silencio a rede de seguranca do numero.
        $ingestor->ingest($asset, $this->sessionEvent(str_repeat('x', 10000)));
        self::assertSame('quarantined', $asset->getSettings()[SessionState::SETTING_STATUS] ?? null);
    }

    /**
     * Cinco numeros dividem a mesma tabela de assets. A queda de um nao e a queda dos
     * outros -- e a varredura le todos eles.
     */
    public function testASessionEventOfOneNumberDoesNotTouchAnother(): void
    {
        $atendimento = $this->qrAsset('sess-atendimento', [SessionState::SETTING_STATUS => SessionState::CONNECTED]);
        $cobranca = $this->qrAsset('sess-cobranca', [SessionState::SETTING_STATUS => SessionState::CONNECTED]);

        $this->inboundIngestor()->ingest($atendimento, $this->sessionEvent(SessionState::LOGGED_OUT));

        self::assertSame(SessionState::LOGGED_OUT, $atendimento->getSettings()[SessionState::SETTING_STATUS] ?? null);
        self::assertSame(SessionState::CONNECTED, $cobranca->getSettings()[SessionState::SETTING_STATUS] ?? null);
    }

    /**
     * A outra metade da regra do chip trocado.
     *
     * O servico ja recusa reconexao com JID diferente (ver checkJID em
     * service/session/state.go). Gravar o JID aqui e o que permite ao Mautic contar a
     * mesma historia: a tela de Conexoes mostra com qual chip o numero pareou, e quem
     * for diagnosticar uma recusa do servico ve os dois lados sem abrir o log de la.
     */
    public function testPairingStoresTheJid(): void
    {
        $asset = $this->qrAsset();
        $ingestor = $this->inboundIngestor();

        // QR na tela, ninguem escaneou ainda: nao ha chip para gravar.
        $ingestor->ingest($asset, $this->sessionEvent(SessionState::PAIRING, ''));
        self::assertSame(SessionState::PAIRING, $asset->getSettings()[SessionState::SETTING_STATUS] ?? null);
        self::assertSame('', (string) ($asset->getSettings()[SessionState::SETTING_JID] ?? ''));

        // Escaneou. O servico avisa o pareamento com o JID junto, e e este o unico
        // momento em que o Mautic fica sabendo qual chip esta do outro lado.
        $ingestor->ingest($asset, $this->sessionEvent(SessionState::CONNECTED, '5511333333333@s.whatsapp.net'));
        self::assertSame('5511333333333@s.whatsapp.net', $asset->getSettings()[SessionState::SETTING_JID] ?? null);

        // A queda nao apaga o chip pareado. Apagar seria perder justamente o que a tela
        // precisa dizer ao atendente: "escaneie de novo com ESTE numero".
        $ingestor->ingest($asset, $this->sessionEvent(SessionState::LOGGED_OUT, ''));
        self::assertSame('5511333333333@s.whatsapp.net', $asset->getSettings()[SessionState::SETTING_JID] ?? null);
        self::assertSame(SessionState::LOGGED_OUT, $asset->getSettings()[SessionState::SETTING_STATUS] ?? null);
    }

    /**
     * Os dois pedem coisas diferentes do atendente: `reconnecting` espera, `logged_out`
     * precisa de alguem com o celular na mao escaneando de novo.
     *
     * Guardar os dois como um "caido" generico faria alguem esperar por um numero que nao
     * volta sozinho -- e o numero so voltaria no dia em que alguem desconfiasse.
     */
    public function testLoggedOutIsDistinguishableFromReconnecting(): void
    {
        $caiu = $this->qrAsset('sess-atendimento');
        $deslogou = $this->qrAsset('sess-cobranca');
        $ingestor = $this->inboundIngestor();

        $ingestor->ingest($caiu, $this->sessionEvent(SessionState::RECONNECTING));
        $ingestor->ingest($deslogou, $this->sessionEvent(SessionState::LOGGED_OUT));

        $estadoDeQuemCaiu = $caiu->getSettings()[SessionState::SETTING_STATUS] ?? null;
        $estadoDeQuemDeslogou = $deslogou->getSettings()[SessionState::SETTING_STATUS] ?? null;

        self::assertSame(SessionState::RECONNECTING, $estadoDeQuemCaiu);
        self::assertSame(SessionState::LOGGED_OUT, $estadoDeQuemDeslogou);
        self::assertNotSame($estadoDeQuemCaiu, $estadoDeQuemDeslogou);

        // Para a fila, os dois sao a mesma coisa -- nenhum dos dois envia. E ai que a
        // distincao acima deixa de ser cosmetica: a varredura trata os dois igual, e so a
        // tela sabe que um deles precisa de gente.
        foreach ([$estadoDeQuemCaiu, $estadoDeQuemDeslogou] as $estado) {
            self::assertNotSame(SessionState::CONNECTED, $estado);
            self::assertNotSame('', $estado);
        }
    }
}
