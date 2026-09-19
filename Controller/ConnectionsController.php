<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Controller;

use Mautic\CoreBundle\Controller\CommonController;
use Mautic\CoreBundle\Security\Permissions\CorePermissions;
use MauticPlugin\MauticMetaBundle\Domain\AssetType;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticMetaBundle\Entity\MetaAssetRepository;
use MauticPlugin\MauticWhatsQrBundle\Application\ConnectionsOverview;
use MauticPlugin\MauticWhatsQrBundle\Application\PairingScreen;
use MauticPlugin\MauticWhatsQrBundle\Application\ServiceHealth;
use MauticPlugin\MauticWhatsQrBundle\Domain\PairingView;
use MauticPlugin\MauticWhatsQrBundle\Domain\SessionState;
use MauticPlugin\MauticWhatsQrBundle\Driver\SessionDriverFactory;
use MauticPlugin\MauticWhatsQrBundle\Infrastructure\QrEncoder;
use Symfony\Component\HttpFoundation\RedirectResponse;
use Symfony\Component\HttpFoundation\Request;
use Symfony\Component\HttpFoundation\Response;

/**
 * As duas telas deste plugin: a lista dos numeros e o cartao de parear um.
 *
 * O controlador nao decide nada. Ele junta as pecas -- quem conta a fila, quem pergunta
 * ao servico, quem escolhe o que o cartao mostra -- e entrega ao template. Tudo que e
 * decisao mora em ConnectionsOverview e PairingScreen, que tem teste; aqui so ha ordem de
 * chamada, e ordem de chamada e o que se le, nao o que se testa por unidade.
 *
 * As permissoes sao as do Meta bundle de proposito. Um numero por QR e um MetaAsset como
 * os outros; um conjunto proprio de permissoes criaria um usuario que ve a caixa com as
 * conversas deste numero e nao ve o numero, ou o contrario.
 */
final class ConnectionsController extends CommonController
{
    public function index(
        CorePermissions $permissions,
        MetaAssetRepository $assets,
        ConnectionsOverview $overview,
        ServiceHealth $health,
    ): Response {
        if (!$permissions->isGranted('meta:connections:view')) {
            throw $this->createAccessDeniedException();
        }

        $numbers = $assets->findBy(['type' => AssetType::WhatsAppQrSession->value], ['name' => 'ASC']);

        return $this->view('@MauticWhatsQr/Connections/index.html.twig', [
            'rows' => $overview->rows($health->forAssets($numbers)),
        ]);
    }

    public function pair(
        int $assetId,
        CorePermissions $permissions,
        MetaAssetRepository $assets,
        SessionDriverFactory $drivers,
        PairingScreen $screen,
    ): Response {
        if (!$permissions->isGranted('meta:connections:view')) {
            throw $this->createAccessDeniedException();
        }
        $asset = $this->qrAsset($assets, $assetId);
        $driver = $drivers->forAsset($asset);

        try {
            $live = $driver->serviceSessions()[$asset->getExternalId()] ?? null;
            // Sessao que o servico nao conhece e sessao que nunca abriu -- depois de um
            // reinicio, ou na primeira vez. Abrir aqui, e nao num POST separado, e o que
            // faz o QR estar na tela quando ela termina de carregar: um segundo clique
            // para "comecar" seria um passo que nao decide nada.
            $state = $live ?? $driver->openSession($asset);

            $view = $screen->view($state);
            if (PairingView::WAITING === $view->stage) {
                // O codigo e lido agora, e nao guardado da abertura: o WhatsApp o renova
                // durante o pareamento, e um QR velho na tela e um scan que nao funciona
                // sem nada explicando por que.
                $view = new PairingView(PairingView::WAITING, qr: $driver->pairingQr($asset) ?? $state->qr);
            }
        } catch (\RuntimeException $unreachable) {
            // ChannelTemporarilyUnavailable estende \RuntimeException, e a resposta
            // ilegivel do servico tambem chega assim. Os dois sao "nao deu para falar com
            // o servico", e nenhum diz nada sobre o numero em si.
            $view = $screen->serviceUnreachable($unreachable->getMessage());
        } catch (\DomainException $refused) {
            // Recusa do proprio servico que nao muda sozinha: id sem segredo configurado,
            // token errado, motor sem implementacao. Botao nenhum resolve isso.
            $view = new PairingView(
                PairingView::NOT_DONE,
                cause: PairingView::CAUSE_REFUSED,
                reason: $refused->getMessage(),
            );
        }

        return $this->view('@MauticWhatsQr/Connections/pair.html.twig', [
            'asset' => $asset,
            'view'  => $view,
            'qrSvg' => $this->qrSvg($view),
            'jid'   => trim((string) ($asset->getSettings()[SessionState::SETTING_JID] ?? '')) ?: null,
        ]);
    }

    /**
     * Gerar outro codigo.
     *
     * Apaga a sessao antes de abrir de novo, e isso precisa ser dito: apagar leva junto a
     * credencial daquele numero no disco do servico. E o certo nos dois casos em que o
     * botao aparece -- o codigo que expirou nao chegou a criar credencial nenhuma, e o
     * pareamento que o WhatsApp desfez deixou uma credencial que ja nao fala por ninguem.
     * Nos outros casos o botao nao existe, e e esta rota que nao pode ser a saida de uma
     * recusa: quem reabrisse aqui uma sessao recusada apagaria uma credencial viva.
     */
    public function restart(
        int $assetId,
        Request $request,
        CorePermissions $permissions,
        MetaAssetRepository $assets,
        SessionDriverFactory $drivers,
    ): RedirectResponse {
        if (!$permissions->isGranted('meta:connections:edit')
            || !$this->isCsrfTokenValid('whatsqr_pair_restart_'.$assetId, (string) $request->request->get('_token'))) {
            throw $this->createAccessDeniedException();
        }
        $asset = $this->qrAsset($assets, $assetId);

        try {
            $drivers->forAsset($asset)->closeSession($asset);
        } catch (\Throwable $failed) {
            // Nao apagou: seguir para a abertura deixaria o 409 de "essa sessao ja esta
            // aberta" como unica explicacao na tela seguinte, que nao diz nada a ninguem.
            $this->addFlash('error', $failed->getMessage());
        }

        return $this->redirectToRoute('mautic_whatsqr_pair', ['assetId' => $assetId], Response::HTTP_SEE_OTHER);
    }

    /**
     * O codigo desenhado, pronto para ir inteiro dentro da pagina.
     *
     * Vai desenhado no servidor e embutido no HTML: um QR com endereco proprio e um QR que
     * alguem abre sem sessao, e quem o abre pareia o numero. O texto que entra e o que o
     * servico devolveu, sem prefixo nenhum -- e a forma que o whatsmeow produz e a que o
     * aparelho espera; embrulha-la aqui seria este arquivo opinando sobre o protocolo do
     * WhatsApp, que mora na borda em Go.
     */
    private function qrSvg(PairingView $view): ?string
    {
        if (PairingView::WAITING !== $view->stage || null === $view->qr) {
            return null;
        }

        try {
            return QrEncoder::svg($view->qr, $this->translator->trans('mautic.whatsqr.pair.qr_alt'));
        } catch (\DomainException) {
            // Codigo maior do que cabe num QR desta faixa. Nao deveria acontecer -- o do
            // whatsmeow tem uns 250 bytes --, e se acontecer a tela mostra o codigo por
            // escrito em vez de um quadrado em branco que nao explica nada.
            return null;
        }
    }

    private function qrAsset(MetaAssetRepository $assets, int $assetId): MetaAsset
    {
        $asset = $assets->find($assetId);
        if (!$asset instanceof MetaAsset || AssetType::WhatsAppQrSession !== $asset->getType()) {
            // Um numero do Graph entrando aqui sairia pelo servico nao homologado sem que
            // ninguem tivesse pedido isso, e o risco central deste canal e banimento.
            throw $this->createNotFoundException();
        }

        return $asset;
    }

    /**
     * @param array<string, mixed> $parameters
     */
    private function view(string $template, array $parameters): Response
    {
        return $this->delegateView([
            'contentTemplate' => $template,
            'viewParameters'  => $parameters,
            // O mauticContent e "meta" e nao "whatsqr" porque estas telas vivem dentro da
            // navegacao do Meta bundle: um nome proprio faria o painel trocar de secao ao
            // abrir a lista de numeros, e o atendente perderia o menu de onde veio.
            'passthroughVars' => ['mauticContent' => 'meta', 'route' => $this->getCurrentRequest()->getRequestUri()],
        ]);
    }
}
