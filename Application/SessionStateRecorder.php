<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Application;

use Doctrine\ORM\EntityManagerInterface;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticWhatsQrBundle\Domain\SessionState;
use Psr\Log\LoggerInterface;

/**
 * O evento de sessao virando o estado gravado do numero -- a unica coisa que separa tres
 * tarefas prontas de tres tarefas inertes.
 *
 * Nada mais grava `whatsqr_session_status`, e tres coisas o leem: a varredura que expira
 * resposta parada ha duas horas, que sem esta chave nunca acha numero caido e roda de
 * graca todo minuto; o compositor da caixa, que sem ela acha que esta tudo no ar e deixa
 * o atendente escrever para um numero no chao; e a tela de Conexoes, que sem ela nao tem
 * o que mostrar. Quem le decide olhando esta chave, e ninguem pergunta ao servico em Go
 * -- perguntar seria uma ida e volta HTTP de dez segundos justamente quando o servico
 * esta fora do ar.
 *
 * O preco disso e que o estado pode estar velho, e ele foi pago para o lado seguro: quem
 * le trata numero sem estado gravado como desconhecido, nao como caido.
 *
 * **O estado vai para `settings`, nunca para `status` do asset.** Sair de `active` fecha
 * o compositor da caixa e faz o retry devolver 409 -- e o desenho quer o oposto na
 * queda: compositor aberto e resposta indo para a fila, para sair sozinha quando o numero
 * voltar. Esta classe nao chama `setStatus()`, e nao e por esquecimento.
 */
final class SessionStateRecorder
{
    /**
     * A forma de uma palavra de estado, para o caso de chegar uma que este plugin nao
     * conhece.
     *
     * Existe porque o que vem no corpo e texto: ele foi assinado pelo segredo daquele
     * numero, entao veio do servico, mas "veio do servico" nao e "cabe numa coluna JSON".
     * Um campo de dez mil caracteres entraria em `settings` junto com o token e o segredo
     * do numero, e sairia de la em toda leitura de asset.
     */
    private const STATE_SHAPE = '/^[a-z][a-z0-9_]{0,31}$/';

    public function __construct(
        private readonly EntityManagerInterface $entityManager,
        private readonly LoggerInterface $logger,
    ) {
    }

    /**
     * Grava o que o evento disse sobre o numero. Devolve se algo mudou.
     *
     * O asset chega pronto do controlador, que provou pela assinatura de qual numero e o
     * corpo e ja recusou corpo que fala de outro. Reabrir essa pergunta aqui seria decidir
     * duas vezes com metade da informacao.
     *
     * @param array<string, mixed> $payload o corpo do servico, ja conferido
     */
    public function record(MetaAsset $asset, array $payload): bool
    {
        $before = $asset->getSettings();
        $settings = $before;

        $state = trim((string) ($payload['state'] ?? ''));
        if ('' !== $state && $this->isStorable($state, $asset)) {
            $settings[SessionState::SETTING_STATUS] = $state;
        }

        $jid = trim((string) ($payload['jid'] ?? ''));
        if ('' !== $jid) {
            // So grava o que veio, nunca apaga: `logged_out` e `reconnecting` chegam sem
            // JID, e apagar ali perderia o chip justamente quando a tela precisa dizer
            // "escaneie de novo com ESTE numero".
            $recorded = trim((string) ($settings[SessionState::SETTING_JID] ?? ''));
            if ('' !== $recorded && $recorded !== $jid) {
                // Nao deveria acontecer -- o servico recusa a volta com outro chip. Se
                // aconteceu, foi porque a sessao de la nasceu de novo, e entao o chip novo
                // e o que atende: manter o antigo faria a tela anunciar um numero que nao
                // responde mais. Mas passar calado apagaria a unica pista de que alguem
                // trocou o chip por baixo de uma fila que ainda tem resposta de cliente.
                $this->logger->warning(sprintf(
                    'whatsqr: o numero "%s" pareou com um chip diferente do gravado (era "%s", veio "%s") -- a fila que sobrou sairia por um numero que o cliente nunca viu',
                    $asset->getExternalId(),
                    $recorded,
                    $jid,
                ));
            }
            $settings[SessionState::SETTING_JID] = $jid;
        }

        if ($settings === $before) {
            // Evento repetido ou corpo sem nada que este plugin guarde. Sair sem tocar no
            // banco evita um UPDATE por evento de sessao -- e sao muitos: um numero
            // instavel cai e volta o dia inteiro.
            return false;
        }

        if (preg_match('/^(\d+)(?::\d+)?@s\.whatsapp\.net$/', $jid, $matched)) { $asset->setPhoneNumber('+'.$matched[1]); }
        $asset->setSettings($settings);
        $this->entityManager->persist($asset);
        $this->entityManager->flush();

        return true;
    }

    /**
     * Se esta palavra pode ser gravada por cima da que estava la.
     *
     * A decisao que importa e a do estado desconhecido, e ela e guardar, nao ignorar.
     * Guardar parece errado -- e uma palavra que nenhuma tela sabe traduzir --, mas quem
     * le faz a pergunta por exclusao: `ExpireQueuedCommand` conta como caido tudo que nao
     * e `connected`, de proposito, para que um estado novo entre como "nao da para
     * enviar", que e a leitura segura. Ignorar deixaria no lugar a ultima palavra
     * conhecida, e se ela fosse `connected` um estado novo que significasse "banido"
     * manteria o numero contado como no ar -- cego exatamente no caso em que a varredura
     * existe para ajudar. Guardar troca uma tela que mostra uma palavra estranha por uma
     * fila que para de mandar; a palavra estranha, alias, e a pista de que o servico esta
     * mais novo que o plugin.
     *
     * O que nao se guarda e o que nao tem forma de estado: ai nao ha leitura segura
     * possivel, e o que estava la -- que pelo menos veio de um evento entendido -- fica.
     * Nunca se apaga a chave: sem ela, quem le trata o numero como desconhecido e o pula.
     */
    private function isStorable(string $state, MetaAsset $asset): bool
    {
        if (SessionState::isKnown($state)) {
            return true;
        }

        if (1 !== preg_match(self::STATE_SHAPE, $state)) {
            $this->logger->error(sprintf(
                'whatsqr: estado de sessao com forma impropria descartado no numero "%s" -- o que ja estava gravado fica',
                $asset->getExternalId(),
            ));

            return false;
        }

        $this->logger->warning(sprintf(
            'whatsqr: o servico mandou o estado "%s", que este plugin nao conhece, para o numero "%s" -- gravado como caido ate alguem ensinar a palavra ao plugin',
            $state,
            $asset->getExternalId(),
        ));

        return true;
    }
}
