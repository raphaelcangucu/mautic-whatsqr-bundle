// Comando whatsqr mantem sessoes de WhatsApp vivas por QR Code e as poe
// atras de cinco rotas em 127.0.0.1.
//
// Este arquivo e a costura, e so isso: nenhuma regra mora aqui. Ele carrega
// a configuracao, abre o disco, religa o que ja estava pareado, liga o
// remetente de webhook no aviso do gerente e sobe o HTTP. Cada uma dessas
// pecas tem teste proprio no pacote dela; o que nao tem teste automatizado
// e justamente a ordem em que elas sao ligadas, e e por isso que ela esta
// escrita por extenso abaixo.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/macro-markets/whatsqr/api"
	"github.com/macro-markets/whatsqr/session"
	"github.com/macro-markets/whatsqr/webhook"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	if err := run(); err != nil {
		log.Fatalf("whatsqr: %v", err)
	}
}

func run() error {
	configPath := flag.String("config", "whatsqr.json", "caminho do arquivo de configuracao (permissao 0600)")
	flag.Parse()

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		return err
	}

	// O sinal vira cancelamento antes de qualquer coisa abrir: um Ctrl-C no
	// meio da religacao das sessoes tem que parar a religacao, e nao
	// esperar as cinco.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := session.OpenStore(ctx, cfg.StorePath, session.StdoutLogger(cfg.LogLevel))
	if err != nil {
		return err
	}
	defer store.Close()
	protectStore(cfg.StorePath)

	dir, err := openDirectory(ctx, cfg.StorePath)
	if err != nil {
		return err
	}
	defer dir.Close()

	sender := webhook.New(webhook.Options{
		URL:    cfg.WebhookURL,
		Secret: cfg.Secret,
	})
	defer sender.Close()

	manager := session.NewManager(
		dialer(ctx, dir, store),
		session.Options{Notify: notifier(dir, sender)},
	)
	defer manager.Shutdown()

	// Antes de escutar, e nao depois: subir a porta primeiro deixaria uma
	// janela em que /health responde com a lista vazia e a tela de Conexoes
	// mostra cinco numeros desligados que na verdade estao voltando.
	restore(ctx, manager, store, dir)

	server := api.NewServer(api.Options{
		Manager:     manager,
		Token:       cfg.Token,
		Credentials: store,
		Directory:   dir,
		HasSecret:   cfg.HasSecret,
	})

	listener, err := api.Listen(cfg.Listen)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Handler: server.Handler(),
		// Prazos em tudo: sem eles, uma conexao aberta e muda segura um
		// descritor e uma goroutine para sempre, e bastam algumas para
		// derrubar um servico que atende cinco numeros.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// A escrita e mais larga que a leitura porque POST /sessions espera
		// o primeiro QR chegar do WhatsApp antes de ter o que responder.
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  2 * time.Minute,
	}

	go func() {
		<-ctx.Done()
		grace, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpServer.Shutdown(grace)
	}()

	log.Printf("whatsqr: escutando em %s, com %d sessoes configuradas", listener.Addr(), len(cfg.Sessions))
	if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Printf("whatsqr: desligando")
	return nil
}

// notifier e a costura entre o gerente e o resto: primeiro grava de quem e
// o chip, depois conta ao Mautic.
//
// A gravacao e sincrona, dentro da goroutine dona da sessao, e isso merece
// justificativa porque o gerente pede que Notify nao bloqueie. E uma
// escrita de uma linha num SQLite local; o que se compra com ela e o E2E 6.
// Feita de forma assincrona, ela poderia se perder num desligamento -- e
// perder isto e o proximo reinicio pedindo QR de novo, que e exatamente o
// que derruba o atendimento.
func notifier(dir *directory, sender *webhook.Sender) func(session.Notice) {
	return func(n session.Notice) {
		if n.Kind == session.NoticeSession && n.JID != "" {
			if err := dir.Bind(n.SessionID, n.JID); err != nil {
				// Recusa aqui e a regra do chip valendo depois do reinicio,
				// quando o objeto Session em memoria nao existe mais. Nao
				// ha a quem devolver erro -- isto veio de um evento, nao de
				// uma chamada -- entao o log e o unico lugar onde ela
				// aparece.
				log.Printf("whatsqr: nao gravei o de-para de %s: %v", n.SessionID, err)
			}
		}
		sender.Notify(n)
	}
}

// dialer e a fabrica de clientes do gerente. E aqui que o id de sessao vira
// credencial: o de-para diz qual numero e daquele id, e o store abre a
// credencial daquele numero.
//
// O JID viaja na forma que o resto do servico fala -- a mesma que sai nos
// eventos e que o de-para guarda. Traduzi-la para o indice do whatsmeow e
// trabalho do session/whatsmeow.go, e e por isso que este arquivo nao
// importa nada da biblioteca.
func dialer(ctx context.Context, dir *directory, store *session.WhatsmeowStore) func(string) (session.Client, error) {
	return func(id string) (session.Client, error) {
		jid, bound, err := dir.JID(id)
		if err != nil {
			return nil, fmt.Errorf("lendo o de-para de %s: %w", id, err)
		}
		if !bound {
			// Sessao que nunca pareou: aparelho novo, que vai pedir QR.
			return store.Open(ctx, "")
		}

		client, err := store.Open(ctx, jid)
		if err != nil {
			// O de-para diz que esta sessao e de um numero, e a credencial
			// dele nao esta la para ser aberta. Cair para aparelho novo
			// aqui seria a sessao voltando a pedir QR e aceitando qualquer
			// chip -- o buraco que a regra do JID existe para tapar,
			// reaberto pelo caminho de tras. Quem quer parear outro chip
			// apaga a sessao primeiro, que e um gesto explicito.
			return nil, fmt.Errorf(
				"a sessao %s pareou com %s e essa credencial nao esta disponivel (%w); apague a sessao (DELETE /sessions/%s) antes de parear outro chip",
				id, jid, err, id)
		}
		return client, nil
	}
}

// restore religa o que ja estava pareado. E o que faz o reinicio nao pedir
// QR de novo: cada sessao do de-para abre com a credencial do disco, e o
// gerente, ao ver um cliente sem QR e com chip, ja a da por conectada.
func restore(ctx context.Context, manager *session.Manager, store *session.WhatsmeowStore, dir *directory) {
	bindings, err := dir.All()
	if err != nil {
		log.Printf("whatsqr: nao consegui ler o de-para; nenhuma sessao foi religada: %v", err)
		return
	}
	claimed := make(map[string]bool, len(bindings))
	for _, b := range bindings {
		claimed[b.JID] = true
	}
	for _, b := range bindings {
		if ctx.Err() != nil {
			return
		}
		snap, err := manager.Open(ctx, b.SessionID)
		if err != nil {
			// Uma sessao que nao volta nao pode impedir as outras quatro de
			// voltarem: cada numero atende um cliente diferente.
			log.Printf("whatsqr: a sessao %s (%s) nao voltou: %v", b.SessionID, b.JID, err)
			continue
		}
		if snap.State != session.Connected {
			log.Printf("whatsqr: a sessao %s voltou em %s, e nao conectada", b.SessionID, snap.State)
			continue
		}
		log.Printf("whatsqr: sessao %s religada em %s, sem pedir QR", b.SessionID, snap.JID)
	}

	// Credencial sem dono e um numero que ainda pode falar e que sessao
	// nenhuma reivindica -- sobra de um DELETE que falhou no meio, ou de um
	// de-para apagado a mao. Ninguem notaria sem este aviso.
	devices, err := store.Devices(ctx)
	if err != nil {
		log.Printf("whatsqr: nao consegui listar as credenciais do disco: %v", err)
		return
	}
	for _, jid := range devices {
		if !claimed[jid] {
			log.Printf("whatsqr: ha credencial no disco para %s e nenhuma sessao a reivindica -- ela ainda fala por esse numero", jid)
		}
	}
}

// protectStore fecha a permissao do arquivo de sessoes.
//
// Aqui o servico conserta em vez de recusar, ao contrario do que faz com a
// configuracao, e a diferenca nao e descuido: a configuracao foi escrita
// por gente, e uma permissao larga nela e um engano que precisa ser dito a
// quem o cometeu. Este arquivo e criado pelo proprio whatsmeow, no primeiro
// pareamento; recusar subir por causa dele seria um servico que nunca sobe
// a primeira vez numa maquina sem UMask configurado.
func protectStore(path string) {
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		perm := info.Mode().Perm()
		if perm == 0o600 {
			continue
		}
		if err := os.Chmod(p, 0o600); err != nil {
			log.Printf("whatsqr: %s esta %04o e nao consegui fecha-lo: %v -- quem tem esse arquivo fala pelos numeros", p, perm, err)
			continue
		}
		log.Printf("whatsqr: %s estava %04o e foi para 0600 -- quem tem esse arquivo fala pelos numeros", p, perm)
	}
}
