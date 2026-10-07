<?php

declare(strict_types=1);

namespace MauticPlugin\MauticWhatsQrBundle\Controller;

use Mautic\CoreBundle\Controller\CommonController;
use Mautic\CoreBundle\Security\Permissions\CorePermissions;
use MauticPlugin\MauticMetaBundle\Domain\AssetType;
use MauticPlugin\MauticMetaBundle\Entity\MetaAsset;
use MauticPlugin\MauticMetaBundle\Entity\MetaAssetRepository;
use MauticPlugin\MauticWhatsQrBundle\Application\ConnectionsOverview;
use MauticPlugin\MauticWhatsQrBundle\Application\ConnectionManagement;
use MauticPlugin\MauticWhatsQrBundle\Application\SessionStarter;
use MauticPlugin\MauticWhatsQrBundle\Application\PairingScreen;
use MauticPlugin\MauticWhatsQrBundle\Application\ServiceHealth;
use MauticPlugin\MauticWhatsQrBundle\Domain\PairingView;
use MauticPlugin\MauticWhatsQrBundle\Domain\SessionState;
use MauticPlugin\MauticWhatsQrBundle\Driver\SessionDriverFactory;
use MauticPlugin\MauticWhatsQrBundle\Infrastructure\QrEncoder;
use MauticPlugin\MauticWhatsQrBundle\Form\Type\QrConnectionType;
use Symfony\Component\Form\FormError;
use Symfony\Component\HttpFoundation\RedirectResponse;
use Symfony\Component\HttpFoundation\JsonResponse;
use Symfony\Component\HttpFoundation\Request;
use Symfony\Component\HttpFoundation\Response;
use Symfony\Component\HttpFoundation\StreamedResponse;
use Symfony\Component\Security\Csrf\CsrfTokenManagerInterface;

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
            'canCreate' => $permissions->isGranted('meta:connections:create'),
            'canEdit' => $permissions->isGranted('meta:connections:edit'),
        ]);
    }

    public function new(Request $request, CorePermissions $permissions, ConnectionManagement $manager): Response
    {
        if (!$permissions->isGranted('meta:connections:create')) {
            throw $this->createAccessDeniedException();
        }
        $sources = [];
        $choices = [];
        foreach ($manager->sources() as $source) {
            $sources[$source->getId()] = $source;
            $choices[$source->getName().' · #'.$source->getId()] = $source->getId();
        }
        $form = $this->createForm(QrConnectionType::class, ['source' => array_key_first($sources)], [
            'sources' => $choices, 'csrf_token_id' => 'whatsqr_connection_new',
            'action' => $this->generateUrl('mautic_whatsqr_connection_new'),
        ]);
        $form->handleRequest($request);
        if ($form->isSubmitted() && $form->isValid()) {
            $data = $form->getData();
            try {
                $source = $sources[$data['source']] ?? null;
                if (!$source instanceof MetaAsset) {
                    throw new \DomainException('mautic.whatsqr.form.source.unavailable');
                }
                $asset = $manager->create($data['name'], $source);
                $this->addFlash('notice', $this->translator->trans('mautic.whatsqr.connection.created'));

                return $this->redirectToRoute('mautic_whatsqr_pair', ['assetId' => $asset->getId()], Response::HTTP_SEE_OTHER);
            } catch (\InvalidArgumentException|\DomainException) {
                $form->addError(new FormError($this->translator->trans('mautic.whatsqr.form.save.failed')));
            }
        }

        return $this->view('@MauticWhatsQr/Connections/form.html.twig', [
            'form' => $form->createView(), 'editing' => false, 'hasSources' => [] !== $sources,
        ]);
    }

    public function edit(int $assetId, Request $request, CorePermissions $permissions, MetaAssetRepository $assets, ConnectionManagement $manager): Response
    {
        if (!$permissions->isGranted('meta:connections:edit')) {
            throw $this->createAccessDeniedException();
        }
        $asset = $this->qrAsset($assets, $assetId);
        $form = $this->createForm(QrConnectionType::class, ['name' => $asset->getName()], [
            'editing' => true, 'csrf_token_id' => 'whatsqr_connection_edit_'.$assetId,
            'action' => $this->generateUrl('mautic_whatsqr_connection_edit', ['assetId' => $assetId]),
        ]);
        $form->handleRequest($request);
        if ($form->isSubmitted() && $form->isValid()) {
            try {
                $manager->rename($asset, $form->getData()['name']);
                $this->addFlash('notice', $this->translator->trans('mautic.whatsqr.connection.updated'));

                return $this->redirectToRoute('mautic_whatsqr_connections', [], Response::HTTP_SEE_OTHER);
            } catch (\InvalidArgumentException|\DomainException) {
                $form->addError(new FormError($this->translator->trans('mautic.whatsqr.form.save.failed')));
            }
        }

        return $this->view('@MauticWhatsQr/Connections/form.html.twig', [
            'form' => $form->createView(), 'editing' => true, 'asset' => $asset, 'hasSources' => true,
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
        return $this->view('@MauticWhatsQr/Connections/pair.html.twig', $this->pairParameters($asset, $permissions, $drivers, $screen));
    }

    public function status(int $assetId, CorePermissions $permissions, MetaAssetRepository $assets, SessionDriverFactory $drivers, PairingScreen $screen): JsonResponse
    {
        if (!$permissions->isGranted('meta:connections:view')) { throw $this->createAccessDeniedException(); }
        $parameters = $this->pairParameters($this->qrAsset($assets, $assetId), $permissions, $drivers, $screen);
        return new JsonResponse([
            'stage' => $parameters['view']->stage,
            'html' => $this->renderView('@MauticWhatsQr/Connections/_pair_status.html.twig', $parameters),
        ], headers: ['Cache-Control' => 'private, no-store, max-age=0', 'X-Content-Type-Options' => 'nosniff']);
    }

    public function events(int $assetId, Request $request, CorePermissions $permissions, MetaAssetRepository $assets, SessionDriverFactory $drivers, PairingScreen $screen, CsrfTokenManagerInterface $tokens): StreamedResponse
    {
        if (!$permissions->isGranted('meta:connections:view')) { throw $this->createAccessDeniedException(); }
        $asset = $this->qrAsset($assets, $assetId);
        $driver = $drivers->forAsset($asset);
        // Resolve access, lazy entities and CSRF before releasing the session lock.
        $parameters = [
            'asset' => $asset, 'canEdit' => $permissions->isGranted('meta:connections:edit'),
            'jid' => trim((string) ($asset->getSettings()[SessionState::SETTING_JID] ?? '')) ?: null,
            'startToken' => $tokens->getToken('whatsqr_pair_start_'.$assetId)->getValue(),
            'restartToken' => $tokens->getToken('whatsqr_pair_restart_'.$assetId)->getValue(),
            'historyToken' => $tokens->getToken('whatsqr_history_'.$assetId)->getValue(),
        ];
        if ($request->hasSession()) { $request->getSession()->save(); }
        $previous = (string) $request->headers->get('Last-Event-ID', $request->query->get('version', ''));
        return new StreamedResponse(function () use ($driver, $asset, $screen, $parameters, $previous): void {
            $flush = static function (): bool {
                if (ob_get_level() > 0) { @ob_flush(); }
                flush();
                return !connection_aborted();
            };
            $live = false;
            $emit = function (PairingView $view) use ($parameters, &$previous, &$live, $flush): bool {
                if (!$live) { echo "event: live\ndata: {}\n\n"; $live = true; }
                $version = hash('sha256', json_encode($view, JSON_THROW_ON_ERROR));
                if ($version === $previous) { return $flush(); }
                $html = $this->renderView('@MauticWhatsQr/Connections/_pair_status.html.twig', $parameters + [
                    'view' => $view, 'qrSvg' => $this->qrSvg($view),
                ]);
                if ($version !== $previous) {
                    echo 'id: '.$version."\nevent: pairing\ndata: ".json_encode(['stage' => $view->stage, 'html' => $html], JSON_THROW_ON_ERROR)."\n\n";
                    $previous = $version;
                }
                return $flush();
            };
            echo "retry: 5000\n\n";
            if (!$flush()) { return; }
            try {
                $driver->watchSession($asset,
                    static fn (?SessionState $state): bool => $emit(null === $state ? new PairingView(PairingView::READY) : $screen->view($state)),
                    static function () use ($flush): bool { echo ": heartbeat\n\n"; return $flush(); },
                );
                echo "event: rotate\ndata: {}\n\n";
            } catch (\Throwable) {
                // Do not disclose private endpoints, tokens or raw transport errors.
                echo "event: unavailable\ndata: {}\n\n";
            }
            $flush();
        }, 200, [
            'Content-Type' => 'text/event-stream', 'Cache-Control' => 'private, no-cache, no-store, max-age=0',
            'X-Accel-Buffering' => 'no', 'X-Content-Type-Options' => 'nosniff',
        ]);
    }

    public function start(int $assetId, Request $request, CorePermissions $permissions, MetaAssetRepository $assets, SessionStarter $starter): RedirectResponse
    {
        if (!$permissions->isGranted('meta:connections:edit')
            || !$this->isCsrfTokenValid('whatsqr_pair_start_'.$assetId, (string) $request->request->get('_token'))) {
            throw $this->createAccessDeniedException();
        }
        $asset = $this->qrAsset($assets, $assetId);
        try {
            $starter->start($asset);
        } catch (\Throwable $failed) {
            $this->addFlash('error', $this->translator->trans('mautic.whatsqr.connection.start.failed'));
        }
        return $this->redirectToRoute('mautic_whatsqr_pair', ['assetId' => $assetId], Response::HTTP_SEE_OTHER);
    }

    public function syncHistory(int $assetId, Request $request, CorePermissions $permissions, MetaAssetRepository $assets, SessionDriverFactory $drivers, \MauticPlugin\MauticWhatsQrBundle\Application\HistoryAnchorProvider $history): RedirectResponse
    {
        if (!$permissions->isGranted('meta:connections:edit')
            || !$this->isCsrfTokenValid('whatsqr_history_'.$assetId, (string) $request->request->get('_token'))) {
            throw $this->createAccessDeniedException();
        }
        $asset = $this->qrAsset($assets, $assetId);
        try {
            $driver = $drivers->forAsset($asset);
            if (!$driver instanceof \MauticPlugin\MauticWhatsQrBundle\Driver\HistoryDriverInterface) { throw new \DomainException('History is unavailable for this driver.'); }
            $driver->requestHistory($asset, $history->forAsset($asset));
            $this->addFlash('notice', $this->translator->trans('mautic.whatsqr.history.requested'));
        } catch (\Throwable) {
            $this->addFlash('error', $this->translator->trans('mautic.whatsqr.history.failed'));
        }
        return $this->redirectToRoute('mautic_whatsqr_pair', ['assetId' => $assetId], Response::HTTP_SEE_OTHER);
    }

    private function pairParameters(MetaAsset $asset, CorePermissions $permissions, SessionDriverFactory $drivers, PairingScreen $screen): array
    {
        try {
            $driver = $drivers->forAsset($asset);
            $state = $driver->serviceSessions()[$asset->getExternalId()] ?? null;
            $view = null === $state ? new PairingView(PairingView::READY) : $screen->view($state);
            if (PairingView::WAITING === $view->stage) {
                $view = new PairingView(PairingView::WAITING, qr: $driver->pairingQr($asset) ?? $state->qr);
            }
        } catch (\RuntimeException $failed) {
            $view = $screen->serviceUnreachable($failed->getMessage());
        } catch (\DomainException $failed) {
            $view = new PairingView(PairingView::NOT_DONE, cause: PairingView::CAUSE_REFUSED, reason: $failed->getMessage());
        }
        return ['asset' => $asset, 'view' => $view, 'version' => hash('sha256', json_encode($view, JSON_THROW_ON_ERROR)), 'qrSvg' => $this->qrSvg($view),
            'canEdit' => $permissions->isGranted('meta:connections:edit'),
            'jid' => trim((string) ($asset->getSettings()[SessionState::SETTING_JID] ?? '')) ?: null];
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
        PairingScreen $screen,
    ): RedirectResponse {
        if (!$permissions->isGranted('meta:connections:edit')
            || !$this->isCsrfTokenValid('whatsqr_pair_restart_'.$assetId, (string) $request->request->get('_token'))) {
            throw $this->createAccessDeniedException();
        }
        $asset = $this->qrAsset($assets, $assetId);

        try {
            $driver = $drivers->forAsset($asset);
            $live = $driver->serviceSessions()[$asset->getExternalId()] ?? null;
            // A crafted request must never erase a connected session's credentials.
            if (null !== $live) {
                if (!$screen->canReset($live)) {
                    throw new \DomainException($this->translator->trans('mautic.whatsqr.restart.refused'));
                }
                $driver->closeSession($asset);
            }
            $driver->openSession($asset);
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
        $response = $this->delegateView([
            'contentTemplate' => $template,
            'viewParameters'  => $parameters,
            // O mauticContent e "meta" e nao "whatsqr" porque estas telas vivem dentro da
            // navegacao do Meta bundle: um nome proprio faria o painel trocar de secao ao
            // abrir a lista de numeros, e o atendente perderia o menu de onde veio.
            'passthroughVars' => ['mauticContent' => 'whatsqr', 'route' => $this->getCurrentRequest()->getRequestUri()],
        ]);
        $response->headers->set('Cache-Control', 'private, no-store, max-age=0');
        return $response;
    }
}
