<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit;

use MauticPlugin\MauticWhatsQrBundle\Domain\ConnectionRow;
use MauticPlugin\MauticWhatsQrBundle\Domain\PairingView;
use MauticPlugin\MauticWhatsQrBundle\Domain\SessionState;
use PHPUnit\Framework\TestCase;

/**
 * As palavras que as duas telas montam em tempo de execucao.
 *
 * Chave de traducao que falta nao quebra nada: o Mautic imprime a propria chave. Quem le
 * a tela ve "mautic.whatsqr.state.ambiguous_credential" no lugar da situacao do numero --
 * e isso acontece exatamente no estado raro, que e o que ninguem testa a mao.
 *
 * As chaves conferidas aqui sao as que os templates montam concatenando
 * (`'mautic.whatsqr.state.'~row.situation`), porque so essas podem aparecer sem nunca
 * terem sido escritas em lugar nenhum. Chave literal no Twig se acha com uma busca; estas
 * nao.
 */
final class TranslationsTest extends TestCase
{
    /**
     * @return array<string, string>
     */
    private function catalogue(string $locale): array
    {
        $file = __DIR__.'/../../Translations/'.$locale.'/messages.ini';
        self::assertFileExists($file);

        return parse_ini_file($file, false, INI_SCANNER_RAW) ?: [];
    }

    /**
     * @return list<string>
     */
    private function builtKeys(): array
    {
        $keys = [];

        // A coluna da situacao: as cinco palavras do webhook, a do /health, e o numero
        // sem estado gravado.
        foreach ([...SessionState::all(), SessionState::AMBIGUOUS_CREDENTIAL, ConnectionRow::UNKNOWN] as $situation) {
            $keys[] = 'mautic.whatsqr.state.'.$situation;
        }

        foreach ([PairingView::WAITING, PairingView::CONNECTED, PairingView::NOT_DONE] as $stage) {
            $keys[] = 'mautic.whatsqr.pair.stage.'.$stage;
        }

        foreach ([
            PairingView::CAUSE_EXPIRED,
            PairingView::CAUSE_UNPAIRED,
            PairingView::CAUSE_REFUSED,
            PairingView::CAUSE_AMBIGUOUS,
            PairingView::CAUSE_SERVICE_DOWN,
        ] as $cause) {
            // O titulo e o corpo sempre; a saida por escrito so quando nao ha botao, mas
            // a chave tem que existir nas cinco, porque o template a busca antes de saber.
            $keys[] = 'mautic.whatsqr.pair.cause.'.$cause.'.title';
            $keys[] = 'mautic.whatsqr.pair.cause.'.$cause.'.body';
            $keys[] = 'mautic.whatsqr.pair.cause.'.$cause.'.exit';
        }

        return $keys;
    }

    public function testEveryKeyTheScreensBuildAtRuntimeExists(): void
    {
        foreach (['pt_BR', 'en_US'] as $locale) {
            $catalogue = $this->catalogue($locale);
            foreach ($this->builtKeys() as $key) {
                self::assertArrayHasKey($key, $catalogue, sprintf('%s nao tem "%s"', $locale, $key));
            }
        }
    }

    /**
     * Um idioma com chave que o outro nao tem e uma tela que muda de conteudo conforme
     * quem abre -- e quem descobre e sempre o atendente que usa o idioma menos testado.
     */
    public function testBothCataloguesCarryTheSameKeys(): void
    {
        $pt = array_keys($this->catalogue('pt_BR'));
        $en = array_keys($this->catalogue('en_US'));

        sort($pt);
        sort($en);
        self::assertSame($pt, $en);
    }
}
