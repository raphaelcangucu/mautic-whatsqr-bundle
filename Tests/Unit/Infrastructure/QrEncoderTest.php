<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Tests\Unit\Infrastructure;

use MauticPlugin\MauticWhatsQrBundle\Infrastructure\QrEncoder;
use PHPUnit\Framework\TestCase;

/**
 * O desenho do codigo de pareamento, preso a um resultado conferido por um leitor de
 * verdade.
 *
 * **De onde vem a autoridade destes numeros.** Um QR errado nao parece errado: ele sai
 * quadrado, preto e branco, com os tres cantos no lugar, e simplesmente nao le. Nenhuma
 * asserção sobre "tem 57 modulos" pegaria isso -- foi exatamente assim que uma tabela de
 * alinhamento digitada errada passou pela primeira leva de testes aqui.
 *
 * Entao as matrizes abaixo nao foram escritas a mao nem aceitas por inspecao: elas foram
 * geradas por este codigo e passadas pelo leitor de QR do proprio macOS (CoreImage,
 * CIDetectorTypeQRCode), que devolveu o texto original. Sessenta tamanhos diferentes
 * foram conferidos assim, cobrindo as versoes 1 a 20, os dois tamanhos de contador de
 * caracteres e os blocos duplos -- inclusive um codigo de pareamento de verdade, de 239
 * bytes, que cai na versao 11.
 *
 * O que estes testes fazem e impedir que aquele resultado mude sem ninguem perceber. Se
 * um deles ficar vermelho, a pergunta nao e "qual asserção ajustar": e passar o resultado
 * novo por um leitor de novo, antes de qualquer outra coisa.
 */
final class QrEncoderTest extends TestCase
{
    /**
     * @return list<string>
     */
    private function rows(string $text): array
    {
        return array_map(
            static fn (array $row): string => implode('', array_map(static fn (bool $dark): string => $dark ? '1' : '0', $row)),
            QrEncoder::matrix($text),
        );
    }

    public function testASmallCodeMatchesTheReaderApprovedMatrix(): void
    {
        self::assertSame([
            '111111101011101111111',
            '100000100011001000001',
            '101110101101001011101',
            '101110101100101011101',
            '101110101001001011101',
            '100000100111101000001',
            '111111101010101111111',
            '000000000001100000000',
            '111100101111110011101',
            '010111010011111101100',
            '111100101001010100011',
            '111111010001000101010',
            '111000110100110000101',
            '000000001101001100101',
            '111111100011111110000',
            '100000100000010101111',
            '101110100010101001000',
            '101110101010001001110',
            '101110101110100100100',
            '100000101101011110001',
            '111111101001010100000',
        ], $this->rows('HELLO WORLD'));
    }

    /**
     * Um codigo de pareamento de verdade -- o que saiu do servico no E2E, com os quatro
     * campos separados por virgula.
     *
     * Este e o tamanho que importa: 239 bytes caem na versao 11, que e onde moram o
     * contador de dezesseis bits, os quatro blocos de correcao e a informacao de versao.
     * Um codigo curto passaria por nenhum desses tres caminhos.
     */
    public function testARealPairingCodeMatchesTheReaderApprovedMatrix(): void
    {
        $code = '2@gWECyGjjtORYtS7wOyldeWuOJ1ev4MHgi//CpOUFA8/UxUEKzisNXEGh8ODPLryY3dri9ov56Wvr9JYXycl53IWuqpb9hoZ7Oc4='
            .',+cKK4QQTz72Gce9Y45AA3ApbIxdml59TB1k9q6zbh0A='
            .',TOMOtDWMTRg9Ko8KsC+9FMLy0BDVAi03cCHJb2Syy30='
            .',Cqqe6fs0GGCdYgCIsvcEupbKLCdQ51w2T0gIX9WNKYw=,9';

        $rows = $this->rows($code);

        self::assertCount(57, $rows, 'versao 11');
        self::assertSame(
            'cedc99bd6aba4949842ce17b42f8935495f28906eef115ef6d97d810ea8da863',
            hash('sha256', implode('', $rows)),
        );
    }

    /**
     * A zona de silencio de quatro modulos e obrigatoria pela norma, e nao e enfeite: sem
     * ela a camera nao acha a borda do codigo e a leitura falha sem nada na tela
     * explicando por que.
     */
    public function testTheSvgKeepsTheQuietZoneAroundTheCode(): void
    {
        $svg = QrEncoder::svg('HELLO WORLD', 'Codigo de pareamento');

        // 21 modulos mais quatro de cada lado.
        self::assertStringContainsString('viewBox="0 0 29 29"', $svg);
        self::assertStringContainsString('aria-label="Codigo de pareamento"', $svg);
        // Fundo branco explicito: um SVG transparente sobre tema escuro inverte o codigo,
        // e um QR invertido nao le.
        self::assertStringContainsString('fill="#ffffff"', $svg);
    }

    /**
     * Texto maior do que a faixa de versoes recusa, e recusa aqui.
     *
     * Devolver um quadrado truncado seria a pior saida possivel: ele apareceria na tela,
     * o atendente apontaria a camera, e nada aconteceria.
     */
    public function testACodeTooLongIsRefusedInsteadOfTruncated(): void
    {
        $this->expectException(\DomainException::class);

        QrEncoder::svg(str_repeat('a', 900));
    }
}
