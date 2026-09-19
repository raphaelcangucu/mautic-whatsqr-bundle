<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Infrastructure;

/**
 * O codigo de pareamento virando um quadrado que a camera do celular le.
 *
 * Existe por falta: o servico devolve o codigo como TEXTO, e nenhum dos dois lados desta
 * casa sabe desenhar QR -- nem o Mautic, nem o Meta bundle, nem o binario em Go. Sem isto
 * a tela de Parear mostraria duzentos e tantos caracteres que ninguem consegue apontar a
 * camera para ler.
 *
 * As tres saidas descartadas, para quem vier depois nao refazer o caminho:
 *
 * - **Um servico de QR na internet.** Este codigo E a credencial de pareamento: quem o le
 *   pareia o numero. Manda-lo para um gerador de imagem de terceiros e entregar o numero
 *   a quem hospeda o gerador.
 * - **Desenhar no navegador.** Precisaria de uma biblioteca vinda de CDN -- mesmo
 *   problema, mais a dependencia externa dentro do painel.
 * - **Desenhar no servico em Go.** Seria o lugar certo pela regra da casa (protocolo mora
 *   na borda), mas custaria uma dependencia nova no binario e um redeploy do servico, e
 *   ha sessao pareando la que nao pode cair por causa de uma tela.
 *
 * O que fica: byte mode, correcao de erro L, versoes 1 a 20 -- o codigo do WhatsApp tem
 * uns 250 caracteres e cabe na versao 11. Nivel L e o mesmo que o WhatsApp Web usa: e a
 * tela de um computador a trinta centimetros da camera, nao uma etiqueta amassada.
 *
 * A correcao deste arquivo nao se argumenta, se mede: o teste passa o resultado por um
 * leitor de QR de verdade e confere que o texto volta igual. Uma tabela digitada errada
 * aqui produz um quadrado de aparencia perfeita que nenhum celular le.
 */
final class QrEncoder
{
    /**
     * Por versao: [codewords de correcao por bloco, blocos do grupo 1, dados por bloco do
     * grupo 1, blocos do grupo 2, dados por bloco do grupo 2]. Sempre no nivel L.
     */
    private const BLOCKS = [
        1  => [7, 1, 19, 0, 0],
        2  => [10, 1, 34, 0, 0],
        3  => [15, 1, 55, 0, 0],
        4  => [20, 1, 80, 0, 0],
        5  => [26, 1, 108, 0, 0],
        6  => [18, 2, 68, 0, 0],
        7  => [20, 2, 78, 0, 0],
        8  => [24, 2, 97, 0, 0],
        9  => [30, 2, 116, 0, 0],
        10 => [18, 2, 68, 2, 69],
        11 => [20, 4, 81, 0, 0],
        12 => [24, 2, 92, 2, 93],
        13 => [26, 4, 107, 0, 0],
        14 => [30, 3, 115, 1, 116],
        15 => [22, 5, 87, 1, 88],
        16 => [24, 5, 98, 1, 99],
        17 => [28, 1, 107, 5, 108],
        18 => [30, 5, 120, 1, 121],
        19 => [28, 3, 113, 4, 114],
        20 => [28, 3, 107, 5, 108],
    ];

    /**
     * Centros dos padroes de alinhamento, por versao. Nao dependem do nivel de correcao.
     */
    private const ALIGNMENT = [
        1  => [],
        2  => [6, 18],
        3  => [6, 22],
        4  => [6, 26],
        5  => [6, 30],
        6  => [6, 34],
        7  => [6, 22, 38],
        8  => [6, 24, 42],
        9  => [6, 26, 46],
        10 => [6, 28, 50],
        11 => [6, 30, 54],
        12 => [6, 32, 58],
        13 => [6, 34, 62],
        14 => [6, 26, 46, 66],
        15 => [6, 26, 48, 70],
        16 => [6, 26, 50, 74],
        17 => [6, 30, 54, 78],
        18 => [6, 30, 56, 82],
        19 => [6, 30, 58, 86],
        20 => [6, 34, 62, 90],
    ];

    /** O indicador de nivel L nos bits de formato. */
    private const FORMAT_LEVEL_L = 0b01;

    /**
     * O quadrado, como matriz de booleanos: `true` e modulo escuro.
     *
     * @return list<list<bool>>
     */
    public static function matrix(string $text): array
    {
        $bytes = array_values(unpack('C*', $text) ?: []);
        $version = self::versionFor(count($bytes));
        $codewords = self::interleave(self::dataBits($bytes, $version), $version);

        $best = null;
        $bestPenalty = PHP_INT_MAX;
        for ($mask = 0; $mask < 8; ++$mask) {
            $candidate = self::draw($codewords, $version, $mask);
            $penalty = self::penalty($candidate);
            if ($penalty < $bestPenalty) {
                $bestPenalty = $penalty;
                $best = $candidate;
            }
        }

        /* @var list<list<bool>> $best */
        return $best;
    }

    /**
     * O mesmo quadrado como SVG, pronto para ir inteiro dentro da pagina.
     *
     * SVG e nao PNG porque nao ha extensao de imagem obrigatoria neste PHP, e porque o
     * codigo tem que ficar nitido em qualquer tamanho -- inclusive num celular, que e onde
     * esta tela costuma ser aberta. Vai embutido e nao como arquivo: um QR servido por URL
     * e um QR que alguem abre sem sessao, e quem o abre pareia o numero.
     *
     * A margem de quatro modulos e obrigatoria pela norma; sem ela a camera nao acha as
     * bordas e o codigo simplesmente nao le.
     */
    public static function svg(string $text, string $label = ''): string
    {
        $matrix = self::matrix($text);
        $size = count($matrix) + 8;

        $path = '';
        foreach ($matrix as $y => $row) {
            foreach ($row as $x => $dark) {
                if ($dark) {
                    $path .= sprintf('M%d %dh1v1h-1z', $x + 4, $y + 4);
                }
            }
        }

        return sprintf(
            '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %1$d %1$d" role="img" aria-label="%2$s" shape-rendering="crispEdges">'
            .'<rect width="%1$d" height="%1$d" fill="#ffffff"/><path fill="#000000" d="%3$s"/></svg>',
            $size,
            htmlspecialchars($label, ENT_QUOTES | ENT_SUBSTITUTE, 'UTF-8'),
            $path,
        );
    }

    private static function versionFor(int $length): int
    {
        foreach (array_keys(self::BLOCKS) as $version) {
            // O cabecalho e quatro bits de modo mais o contador, que muda de tamanho na
            // versao 10. Errar esse degrau produz um codigo que so falha nos textos
            // longos, que sao justamente os deste canal.
            $header = 4 + (10 > $version ? 8 : 16);
            if ($length <= intdiv(self::dataCodewords($version) * 8 - $header, 8)) {
                return $version;
            }
        }

        throw new \DomainException(sprintf('Um codigo de %d bytes nao cabe num QR desta faixa de versoes.', $length));
    }

    private static function dataCodewords(int $version): int
    {
        [, $blocks1, $data1, $blocks2, $data2] = self::BLOCKS[$version];

        return $blocks1 * $data1 + $blocks2 * $data2;
    }

    /**
     * Os codewords de dados: modo, contador, conteudo, terminador e enchimento.
     *
     * @param list<int> $bytes
     *
     * @return list<int>
     */
    private static function dataBits(array $bytes, int $version): array
    {
        $bits = '0100'; // byte mode
        $bits .= str_pad(decbin(count($bytes)), 10 > $version ? 8 : 16, '0', STR_PAD_LEFT);
        foreach ($bytes as $byte) {
            $bits .= str_pad(decbin($byte), 8, '0', STR_PAD_LEFT);
        }

        $capacity = self::dataCodewords($version) * 8;
        $bits .= str_repeat('0', min(4, $capacity - strlen($bits)));
        $bits .= str_repeat('0', (8 - strlen($bits) % 8) % 8);

        $codewords = [];
        foreach (str_split($bits, 8) as $chunk) {
            $codewords[] = bindec($chunk);
        }
        // O enchimento alterna 236 e 17 por norma. Nao e decorativo: um preenchimento
        // constante produz um bloco monotono que o mascaramento nao consegue quebrar.
        $padding = [236, 17];
        for ($i = 0; count($codewords) < self::dataCodewords($version); ++$i) {
            $codewords[] = $padding[$i % 2];
        }

        return $codewords;
    }

    /**
     * Intercala blocos de dados e de correcao, na ordem que a norma manda.
     *
     * @param list<int> $data
     *
     * @return list<int>
     */
    private static function interleave(array $data, int $version): array
    {
        [$ecLength, $blocks1, $size1, $blocks2, $size2] = self::BLOCKS[$version];

        $dataBlocks = [];
        $ecBlocks = [];
        $offset = 0;
        foreach ([[$blocks1, $size1], [$blocks2, $size2]] as [$count, $size]) {
            for ($i = 0; $i < $count; ++$i) {
                $block = array_slice($data, $offset, $size);
                $offset += $size;
                $dataBlocks[] = $block;
                $ecBlocks[] = self::errorCorrection($block, $ecLength);
            }
        }

        $out = [];
        foreach ([$dataBlocks, $ecBlocks] as $group) {
            $longest = max(array_map('count', $group));
            for ($i = 0; $i < $longest; ++$i) {
                foreach ($group as $block) {
                    if (isset($block[$i])) {
                        $out[] = $block[$i];
                    }
                }
            }
        }

        return $out;
    }

    /**
     * Reed-Solomon sobre GF(256), com o polinomio 0x11d -- o da norma do QR.
     *
     * @param list<int> $block
     *
     * @return list<int>
     */
    private static function errorCorrection(array $block, int $ecLength): array
    {
        [$exp, $log] = self::tables();

        $generator = [1];
        for ($i = 0; $i < $ecLength; ++$i) {
            $next = array_fill(0, count($generator) + 1, 0);
            foreach ($generator as $j => $coefficient) {
                $next[$j] ^= $coefficient;
                $next[$j + 1] ^= 0 === $coefficient ? 0 : $exp[($log[$coefficient] + $i) % 255];
            }
            $generator = $next;
        }

        $remainder = array_merge($block, array_fill(0, $ecLength, 0));
        for ($i = 0; $i < count($block); ++$i) {
            $lead = $remainder[$i];
            if (0 === $lead) {
                continue;
            }
            foreach ($generator as $j => $coefficient) {
                if (0 !== $coefficient) {
                    $remainder[$i + $j] ^= $exp[($log[$coefficient] + $log[$lead]) % 255];
                }
            }
        }

        return array_values(array_slice($remainder, count($block)));
    }

    /**
     * @return array{list<int>, array<int, int>}
     */
    private static function tables(): array
    {
        static $exp = null;
        static $log = null;
        if (null !== $exp && null !== $log) {
            return [$exp, $log];
        }

        $exp = array_fill(0, 512, 0);
        $log = array_fill(0, 256, 0);
        $value = 1;
        for ($i = 0; $i < 255; ++$i) {
            $exp[$i] = $value;
            $log[$value] = $i;
            $value <<= 1;
            if ($value & 0x100) {
                $value ^= 0x11D;
            }
        }
        for ($i = 255; $i < 512; ++$i) {
            $exp[$i] = $exp[$i - 255];
        }

        return [$exp, $log];
    }

    /**
     * Desenha o quadrado inteiro com uma mascara, e devolve a matriz.
     *
     * @param list<int> $codewords
     *
     * @return list<list<bool>>
     */
    private static function draw(array $codewords, int $version, int $mask): array
    {
        $size = 17 + 4 * $version;
        $modules = array_fill(0, $size, array_fill(0, $size, false));
        $reserved = array_fill(0, $size, array_fill(0, $size, false));

        foreach ([[0, 0], [$size - 7, 0], [0, $size - 7]] as [$x, $y]) {
            self::finder($modules, $reserved, $x, $y, $size);
        }
        self::timing($modules, $reserved, $size);
        self::alignment($modules, $reserved, $version, $size);

        // O modulo escuro fixo. Sempre aceso, sempre aqui: leitores o usam de referencia.
        $modules[$size - 8][8] = true;
        $reserved[$size - 8][8] = true;
        self::reserveFormat($reserved, $size);
        if (7 <= $version) {
            self::versionInfo($modules, $reserved, $version, $size);
        }

        self::placeData($modules, $reserved, $codewords, $size, $mask);
        self::formatInfo($modules, $mask, $size);

        return $modules;
    }

    /**
     * @param list<list<bool>> $modules
     * @param list<list<bool>> $reserved
     */
    private static function finder(array &$modules, array &$reserved, int $x0, int $y0, int $size): void
    {
        for ($dy = -1; $dy <= 7; ++$dy) {
            for ($dx = -1; $dx <= 7; ++$dx) {
                $x = $x0 + $dx;
                $y = $y0 + $dy;
                if ($x < 0 || $y < 0 || $x >= $size || $y >= $size) {
                    continue;
                }
                $inside = $dx >= 0 && $dx <= 6 && $dy >= 0 && $dy <= 6;
                $ring = $inside && (0 === $dx || 6 === $dx || 0 === $dy || 6 === $dy);
                $core = $inside && $dx >= 2 && $dx <= 4 && $dy >= 2 && $dy <= 4;
                $modules[$y][$x] = $ring || $core;
                $reserved[$y][$x] = true;
            }
        }
    }

    /**
     * @param list<list<bool>> $modules
     * @param list<list<bool>> $reserved
     */
    private static function timing(array &$modules, array &$reserved, int $size): void
    {
        for ($i = 8; $i < $size - 8; ++$i) {
            $dark = 0 === $i % 2;
            $modules[6][$i] = $dark;
            $reserved[6][$i] = true;
            $modules[$i][6] = $dark;
            $reserved[$i][6] = true;
        }
    }

    /**
     * @param list<list<bool>> $modules
     * @param list<list<bool>> $reserved
     */
    private static function alignment(array &$modules, array &$reserved, int $version, int $size): void
    {
        $centers = self::ALIGNMENT[$version];
        $first = $centers[0] ?? 0;
        $last = $centers[count($centers) - 1] ?? 0;
        foreach ($centers as $cy) {
            foreach ($centers as $cx) {
                // Os tres cantos ja tem padrao de busca; alinhamento por cima deles
                // destruiria o que o leitor procura primeiro.
                //
                // Sao exatamente estas tres posicoes, e nao "toda posicao ja reservada":
                // a fila e a coluna 6 sao o cronometro e ja estao reservadas, e e por
                // elas que passam os alinhamentos da borda de cima e da esquerda a partir
                // da versao 7. Pula-los desenha um quadrado de aparencia perfeita que
                // nenhum leitor decodifica -- foi o que aconteceu aqui.
                $onFinder = ($cx === $first && $cy === $first)
                    || ($cx === $first && $cy === $last)
                    || ($cx === $last && $cy === $first);
                if ($onFinder) {
                    continue;
                }
                for ($dy = -2; $dy <= 2; ++$dy) {
                    for ($dx = -2; $dx <= 2; ++$dx) {
                        $modules[$cy + $dy][$cx + $dx] = 2 === max(abs($dx), abs($dy)) || (0 === $dx && 0 === $dy);
                        $reserved[$cy + $dy][$cx + $dx] = true;
                    }
                }
            }
        }
    }

    /**
     * @param list<list<bool>> $reserved
     */
    private static function reserveFormat(array &$reserved, int $size): void
    {
        for ($i = 0; $i < 9; ++$i) {
            $reserved[8][$i] = true;
            $reserved[$i][8] = true;
        }
        for ($i = 0; $i < 8; ++$i) {
            $reserved[8][$size - 1 - $i] = true;
            $reserved[$size - 1 - $i][8] = true;
        }
    }

    /**
     * @param list<list<bool>> $modules
     * @param list<list<bool>> $reserved
     */
    private static function versionInfo(array &$modules, array &$reserved, int $version, int $size): void
    {
        $bits = $version << 12;
        $remainder = $bits;
        for ($i = 0; $i < 6; ++$i) {
            if ($remainder & (1 << (17 - $i))) {
                $remainder ^= 0x1F25 << (5 - $i);
            }
        }
        $bits |= $remainder;

        for ($i = 0; $i < 18; ++$i) {
            $dark = (bool) (($bits >> $i) & 1);
            $modules[intdiv($i, 3)][$size - 11 + $i % 3] = $dark;
            $reserved[intdiv($i, 3)][$size - 11 + $i % 3] = true;
            $modules[$size - 11 + $i % 3][intdiv($i, 3)] = $dark;
            $reserved[$size - 11 + $i % 3][intdiv($i, 3)] = true;
        }
    }

    /**
     * @param list<list<bool>> $modules
     */
    private static function formatInfo(array &$modules, int $mask, int $size): void
    {
        $data = (self::FORMAT_LEVEL_L << 3) | $mask;
        $remainder = $data << 10;
        for ($i = 0; $i < 5; ++$i) {
            if ($remainder & (1 << (14 - $i))) {
                $remainder ^= 0x537 << (4 - $i);
            }
        }
        // A mascara fixa 0x5412 impede que um formato todo zero vire um quadrado sem nada
        // escrito nos cantos -- que e o caso em que o leitor nao acha a orientacao.
        $bits = (($data << 10) | $remainder) ^ 0x5412;

        for ($i = 0; $i < 15; ++$i) {
            $dark = (bool) (($bits >> $i) & 1);
            if ($i < 6) {
                $modules[$i][8] = $dark;
            } elseif (6 === $i) {
                $modules[7][8] = $dark;
            } elseif (7 === $i) {
                $modules[8][8] = $dark;
            } elseif (8 === $i) {
                $modules[8][7] = $dark;
            } else {
                $modules[8][14 - $i] = $dark;
            }

            if ($i < 8) {
                $modules[8][$size - 1 - $i] = $dark;
            } else {
                $modules[$size - 15 + $i][8] = $dark;
            }
        }
    }

    /**
     * O zigue-zague de baixo para cima, em colunas de dois, pulando a coluna do
     * cronometro.
     *
     * @param list<list<bool>> $modules
     * @param list<list<bool>> $reserved
     * @param list<int>        $codewords
     */
    private static function placeData(array &$modules, array $reserved, array $codewords, int $size, int $mask): void
    {
        $bits = '';
        foreach ($codewords as $codeword) {
            $bits .= str_pad(decbin($codeword), 8, '0', STR_PAD_LEFT);
        }

        $index = 0;
        $upward = true;
        for ($right = $size - 1; $right > 0; $right -= 2) {
            if (6 === $right) {
                // A coluna 6 e o cronometro vertical inteiro: ela nao entra na contagem
                // de colunas de dados, e nao pula-la desalinha tudo dali para a esquerda.
                --$right;
            }
            for ($step = 0; $step < $size; ++$step) {
                $y = $upward ? $size - 1 - $step : $step;
                foreach ([$right, $right - 1] as $x) {
                    if ($reserved[$y][$x]) {
                        continue;
                    }
                    $dark = isset($bits[$index]) && '1' === $bits[$index];
                    ++$index;
                    $modules[$y][$x] = $dark !== self::maskAt($mask, $x, $y);
                }
            }
            $upward = !$upward;
        }
    }

    private static function maskAt(int $mask, int $x, int $y): bool
    {
        return match ($mask) {
            0 => 0 === ($y + $x) % 2,
            1 => 0 === $y % 2,
            2 => 0 === $x % 3,
            3 => 0 === ($y + $x) % 3,
            4 => 0 === (intdiv($y, 2) + intdiv($x, 3)) % 2,
            5 => 0 === ($y * $x) % 2 + ($y * $x) % 3,
            6 => 0 === ((($y * $x) % 2 + ($y * $x) % 3) % 2),
            7 => 0 === ((($y + $x) % 2 + ($y * $x) % 3) % 2),
            default => false,
        };
    }

    /**
     * As quatro penalidades da norma. Quem tem a menor vence.
     *
     * @param list<list<bool>> $modules
     */
    private static function penalty(array $modules): int
    {
        $size = count($modules);
        $score = 0;

        // 1: corridas de cinco ou mais na mesma cor, nas duas direcoes.
        for ($i = 0; $i < $size; ++$i) {
            foreach ([true, false] as $horizontal) {
                $run = 1;
                for ($j = 1; $j < $size; ++$j) {
                    $now = $horizontal ? $modules[$i][$j] : $modules[$j][$i];
                    $before = $horizontal ? $modules[$i][$j - 1] : $modules[$j - 1][$i];
                    if ($now === $before) {
                        ++$run;
                        continue;
                    }
                    if ($run >= 5) {
                        $score += $run - 2;
                    }
                    $run = 1;
                }
                if ($run >= 5) {
                    $score += $run - 2;
                }
            }
        }

        // 2: blocos 2x2 de uma cor so.
        for ($y = 0; $y < $size - 1; ++$y) {
            for ($x = 0; $x < $size - 1; ++$x) {
                $c = $modules[$y][$x];
                if ($c === $modules[$y][$x + 1] && $c === $modules[$y + 1][$x] && $c === $modules[$y + 1][$x + 1]) {
                    $score += 3;
                }
            }
        }

        // 3: o desenho que imita um padrao de busca -- e o que faria o leitor achar um
        // canto onde nao ha canto.
        $needles = ['10111010000', '00001011101'];
        for ($i = 0; $i < $size; ++$i) {
            $row = '';
            $column = '';
            for ($j = 0; $j < $size; ++$j) {
                $row .= $modules[$i][$j] ? '1' : '0';
                $column .= $modules[$j][$i] ? '1' : '0';
            }
            foreach ($needles as $needle) {
                $score += 40 * substr_count($row, $needle);
                $score += 40 * substr_count($column, $needle);
            }
        }

        // 4: desequilibrio entre claro e escuro.
        $dark = 0;
        foreach ($modules as $row) {
            $dark += count(array_filter($row));
        }
        $percent = (int) (100 * $dark / ($size * $size));
        $score += 10 * intdiv(abs($percent - 50), 5);

        return $score;
    }
}
