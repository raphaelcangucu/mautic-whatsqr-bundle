package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	// Driver SQLite em Go puro. E o mattn/go-sqlite3 que exige CGO, e com
	// CGO o binario deixa de ser estatico -- que e metade do motivo de este
	// servico ser em Go: ele e compilado no macOS e enviado pronto para um
	// Linux x86_64 que nao tem toolchain nenhuma.
	_ "modernc.org/sqlite"
)

// Este e o unico arquivo do pacote que conhece o whatsmeow, e e a borda:
// nao tem teste de unidade porque testa-lo exigiria um WhatsApp de verdade.
// O que da para errar aqui aparece nos testes de ponta a ponta, num numero
// que pode ser banido.

// filaDeEventos e o tamanho do buffer entre o whatsmeow e o gerente. O
// whatsmeow chama os handlers na propria goroutine que le o socket, entao
// segurar um handler segura a conexao inteira. O buffer da folga para o
// gerente aplicar a transicao sem que a leitura do socket espere.
const filaDeEventos = 64

// esperaDoPrimeiroQr e quanto se espera pelo primeiro QR antes de desistir.
// A rota POST /sessions responde dentro da requisicao: sem prazo aqui, um
// WhatsApp mudo deixaria o atendente olhando uma tela girando.
const esperaDoPrimeiroQr = 30 * time.Second

// WhatsmeowStore e o arquivo SQLite onde as sessoes moram. Um arquivo para
// todos os numeros: o whatsmeow guarda um aparelho por JID dentro dele.
//
// Quem tem este arquivo fala pelos numeros. O desenho manda trata-lo como
// credencial -- permissao restrita, fora de diretorio servido, cuidado no
// backup.
type WhatsmeowStore struct {
	container *sqlstore.Container
	log       waLog.Logger
}

// OpenStore abre (e migra) o arquivo de sessoes.
func OpenStore(ctx context.Context, caminho string, log waLog.Logger) (*WhatsmeowStore, error) {
	if log == nil {
		log = waLog.Noop
	}
	// O dialeto e "sqlite" e nao "sqlite3" porque esse e o nome com que o
	// modernc se registra no database/sql. As pragmas vao na propria URL,
	// no formato do modernc, que nao e o do mattn: foreign_keys porque o
	// esquema do whatsmeow depende delas para limpar sessao antiga, e
	// busy_timeout porque o arquivo e tocado por varias sessoes ao mesmo
	// tempo e "database is locked" aqui e uma sessao que cai.
	endereco := fmt.Sprintf(
		"file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate",
		caminho,
	)
	container, err := sqlstore.New(ctx, "sqlite", endereco, log)
	if err != nil {
		return nil, fmt.Errorf("session: abrindo o store: %w", err)
	}
	return &WhatsmeowStore{container: container, log: log}, nil
}

// Devices lista os JIDs ja pareados. E por aqui que o servico sabe quais
// sessoes religar depois de um reinicio, sem pedir QR de novo.
func (s *WhatsmeowStore) Devices(ctx context.Context) ([]string, error) {
	aparelhos, err := s.container.GetAllDevices(ctx)
	if err != nil {
		return nil, err
	}
	jids := make([]string, 0, len(aparelhos))
	for _, a := range aparelhos {
		if a.ID != nil {
			jids = append(jids, a.ID.String())
		}
	}
	return jids, nil
}

// Open devolve o cliente de um aparelho. JID vazio e aparelho novo, que vai
// pedir QR; JID preenchido e a sessao que ja estava no disco.
//
// O de-para entre o id de sessao do servico e o JID do aparelho nao esta
// aqui de proposito: e da camada que guarda a configuracao. Este arquivo so
// sabe abrir aparelho.
func (s *WhatsmeowStore) Open(ctx context.Context, jid string) (Cliente, error) {
	var aparelho *store.Device
	if jid == "" {
		aparelho = s.container.NewDevice()
	} else {
		parsed, err := types.ParseJID(jid)
		if err != nil {
			return nil, fmt.Errorf("session: jid %q: %w", jid, err)
		}
		aparelho, err = s.container.GetDevice(ctx, parsed)
		if err != nil {
			return nil, fmt.Errorf("session: buscando o aparelho %q: %w", jid, err)
		}
		if aparelho == nil {
			return nil, fmt.Errorf("session: aparelho %q nao esta no store", jid)
		}
	}
	return novoWhatsmeow(aparelho, s.log), nil
}

// Forget apaga o aparelho do disco. E o que faz DELETE /sessions nao deixar
// para tras uma credencial que ainda fala pelo numero.
func (s *WhatsmeowStore) Forget(ctx context.Context, jid string) error {
	parsed, err := types.ParseJID(jid)
	if err != nil {
		return fmt.Errorf("session: jid %q: %w", jid, err)
	}
	aparelho, err := s.container.GetDevice(ctx, parsed)
	if err != nil {
		return err
	}
	if aparelho == nil {
		return nil
	}
	return s.container.DeleteDevice(ctx, aparelho)
}

// Close fecha o arquivo.
func (s *WhatsmeowStore) Close() error { return s.container.Close() }

// whatsmeowCliente implementa Cliente com a biblioteca de verdade.
type whatsmeowCliente struct {
	cli     *whatsmeow.Client
	eventos chan Evento

	// fim e fechado por Desconectar. Todo envio para o canal de eventos
	// escuta ele tambem: sem isso, um handler do whatsmeow ficaria preso
	// tentando entregar evento para um gerente que ja foi embora, e com ele
	// a goroutine que le o socket.
	fim   chan struct{}
	fecha sync.Once

	mu sync.Mutex
	qr string
	// jid e copia nossa, e nao uma leitura de cli.Store.ID a cada chamada:
	// esse campo e escrito pelo whatsmeow quando o pareamento fecha,
	// enquanto o gerente pergunta o chip de outra goroutine. Guardar aqui
	// tira a corrida de cima da sincronizacao interna da biblioteca, que
	// nao e contrato nosso.
	jid string
	// pareando marca a janela entre o PairSuccess e a reconexao que o
	// proprio whatsmeow faz em seguida. Essa queda e do protocolo, nao da
	// linha do cliente, e anuncia-la faria a tela piscar "reconectando"
	// no segundo seguinte ao scan.
	pareando bool
	// caiu diz se houve queda de verdade desde a ultima conexao. E o que
	// separa o Connected que e volta do Connected que e so a conexao
	// inicial -- e e por isso que a distincao entre EventPaired e
	// EventResumed pode ser feita aqui, na borda, e nao no gerente.
	caiu bool
}

func novoWhatsmeow(aparelho *store.Device, log waLog.Logger) *whatsmeowCliente {
	cli := whatsmeow.NewClient(aparelho, log)
	// Reconexao automatica ligada: a queda e rotina neste canal, e o
	// whatsmeow volta sozinho. Quem decide quando desistir continua sendo a
	// janela do state.go, cobrada pelo gerente.
	cli.EnableAutoReconnect = true
	c := &whatsmeowCliente{
		cli:     cli,
		eventos: make(chan Evento, filaDeEventos),
		fim:     make(chan struct{}),
	}
	if aparelho.ID != nil {
		c.jid = aparelho.ID.ToNonAD().String()
	}
	return c
}

func (c *whatsmeowCliente) Conectar(ctx context.Context) (<-chan Evento, error) {
	var qrChan <-chan whatsmeow.QRChannelItem
	novo := c.cli.Store.ID == nil
	if novo {
		// GetQRChannel tem que vir antes de Connect: e ele que assina os
		// eventos de pareamento.
		var err error
		qrChan, err = c.cli.GetQRChannel(ctx)
		if err != nil {
			return nil, fmt.Errorf("session: canal de qr: %w", err)
		}
	}

	c.cli.AddEventHandler(c.traduzir)

	if err := c.cli.Connect(); err != nil {
		return nil, fmt.Errorf("session: conectando: %w", err)
	}

	if novo {
		if err := c.primeiroQr(ctx, qrChan); err != nil {
			c.cli.Disconnect()
			return nil, err
		}
		go c.seguirQr(qrChan)
	}
	return c.eventos, nil
}

// primeiroQr espera o codigo que a resposta de POST /sessions precisa
// carregar. A interface promete que, quando Conectar volta, ou ha QR ou ha
// chip -- e e aqui que essa promessa e paga.
func (c *whatsmeowCliente) primeiroQr(ctx context.Context, qrChan <-chan whatsmeow.QRChannelItem) error {
	prazo, cancelar := context.WithTimeout(ctx, esperaDoPrimeiroQr)
	defer cancelar()
	for {
		select {
		case item, aberto := <-qrChan:
			if !aberto {
				return errors.New("session: o canal de qr fechou antes do primeiro codigo")
			}
			if item.Event == whatsmeow.QRChannelEventCode {
				c.guardarQr(item.Code)
				return nil
			}
			if item.Error != nil {
				return fmt.Errorf("session: pareamento: %w", item.Error)
			}
			return fmt.Errorf("session: pareamento terminou em %q antes do primeiro codigo", item.Event)
		case <-prazo.Done():
			return fmt.Errorf("session: sem qr depois de %s: %w", esperaDoPrimeiroQr, prazo.Err())
		case <-c.fim:
			return ErrSessionClosed
		}
	}
}

// seguirQr acompanha as renovacoes e o desfecho do pareamento.
func (c *whatsmeowCliente) seguirQr(qrChan <-chan whatsmeow.QRChannelItem) {
	for item := range qrChan {
		switch item.Event {
		case whatsmeow.QRChannelEventCode:
			c.guardarQr(item.Code)
		case whatsmeow.QRChannelSuccess.Event:
			// Quem muda o estado e o PairSuccess; aqui so se apaga o codigo
			// para a tela nao continuar oferecendo um QR ja usado.
			c.guardarQr("")
		default:
			c.guardarQr("")
			// A tela de pareamento distingue "expirou" de "o WhatsApp
			// recusou", e so o primeiro oferece tentar de novo. Por isso o
			// motivo viaja com o desfecho do whatsmeow dentro, em vez de um
			// "falhou" generico.
			motivo := item.Event
			if item.Error != nil {
				motivo = fmt.Sprintf("%s: %s", item.Event, item.Error)
			}
			c.mandar(Evento{Kind: EventFailed, Reason: motivo})
		}
	}
}

func (c *whatsmeowCliente) guardarQr(qr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.qr = qr
}

func (c *whatsmeowCliente) QrAtual() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.qr
}

func (c *whatsmeowCliente) Jid() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.jid
}

func (c *whatsmeowCliente) EnviarTexto(ctx context.Context, para, texto string) (string, error) {
	destino, err := paraJID(para)
	if err != nil {
		return "", err
	}
	resp, err := c.cli.SendMessage(ctx, destino, &waE2E.Message{Conversation: proto.String(texto)})
	if err != nil {
		return "", err
	}
	if resp.ID == "" {
		// Sem id nao ha como amarrar o status de entrega que chega depois a
		// bolha certa na caixa; melhor falhar agora que mentir um sucesso.
		return "", errors.New("session: o whatsmeow devolveu envio sem id")
	}
	return string(resp.ID), nil
}

// paraJID aceita tanto o JID inteiro quanto o telefone cru que o plugin
// normalizou, porque os dois aparecem: a resposta a uma conversa tem JID, e
// um envio novo tem numero.
func paraJID(para string) (types.JID, error) {
	if strings.Contains(para, "@") {
		jid, err := types.ParseJID(para)
		if err != nil {
			return types.EmptyJID, fmt.Errorf("session: destino %q: %w", para, err)
		}
		return jid, nil
	}
	numero := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, para)
	if numero == "" {
		return types.EmptyJID, fmt.Errorf("session: destino %q nao tem numero", para)
	}
	return types.NewJID(numero, types.DefaultUserServer), nil
}

func (c *whatsmeowCliente) Desconectar() {
	c.fecha.Do(func() { close(c.fim) })
	c.cli.Disconnect()
}

// mandar entrega o evento ao gerente sem nunca bloquear para sempre.
func (c *whatsmeowCliente) mandar(ev Evento) {
	select {
	case c.eventos <- ev:
	case <-c.fim:
	}
}

// traduzir e a fronteira propriamente dita: entra tipo do whatsmeow, sai
// Evento deste pacote. Nenhuma regra de transicao mora aqui -- o que se
// decide e apenas qual evento aconteceu, nao o que ele significa para a
// sessao.
func (c *whatsmeowCliente) traduzir(raw any) {
	switch evt := raw.(type) {
	case *events.PairSuccess:
		jid := evt.ID.ToNonAD().String()
		c.mu.Lock()
		c.pareando = true
		c.caiu = false
		c.jid = jid
		c.mu.Unlock()
		c.mandar(Evento{Kind: EventPaired, JID: jid})

	case *events.Connected:
		c.mu.Lock()
		pareando, caiu := c.pareando, c.caiu
		c.pareando = false
		c.caiu = false
		c.mu.Unlock()
		if pareando || !caiu {
			// A primeira conexao de uma sessao ja pareada nao e uma volta:
			// o gerente ja a abriu como conectada. Anuncia-la como volta
			// seria uma transicao que o state.go recusa com razao.
			return
		}
		c.mandar(Evento{Kind: EventResumed, JID: c.Jid()})

	case *events.Disconnected:
		c.mu.Lock()
		pareando := c.pareando
		if !pareando {
			c.caiu = true
		}
		c.mu.Unlock()
		if pareando {
			// Queda do proprio handshake de pareamento, nao da linha.
			return
		}
		c.mandar(Evento{Kind: EventDropped})

	case *events.LoggedOut:
		c.mandar(Evento{Kind: EventLoggedOut, Reason: evt.Reason.String()})

	case *events.StreamReplaced:
		// Outro cliente assumiu a sessao. Nao adianta esperar: enquanto o
		// outro estiver de pe, este nao volta.
		c.mandar(Evento{Kind: EventFailed, Reason: "a sessao foi assumida por outro cliente"})

	case *events.TemporaryBan:
		c.mandar(Evento{Kind: EventFailed, Reason: evt.String()})

	case *events.ClientOutdated:
		c.mandar(Evento{Kind: EventFailed, Reason: "versao do cliente recusada pelo WhatsApp"})

	case *events.ConnectFailure:
		c.mandar(Evento{Kind: EventFailed, Reason: fmt.Sprintf("%s: %s", evt.Reason, evt.Message)})

	case *events.Message:
		if msg := traduzirMensagem(evt); msg != nil {
			c.mandar(Evento{Kind: EventMessage, Message: msg})
		}

	case *events.Receipt:
		for _, ev := range traduzirRecibo(evt) {
			c.mandar(ev)
		}
	}
}

func traduzirMensagem(evt *events.Message) *Inbound {
	// O que o proprio numero mandou volta pelo espelho dos outros
	// aparelhos; deixar passar criaria uma conversa de entrada para cada
	// mensagem que o servico acabou de enviar.
	if evt.Info.IsFromMe {
		return nil
	}
	// Grupo esta fora do escopo por desenho: a caixa nao tem conceito de
	// conversa com varios participantes, e criar uma seria pior que nao
	// criar.
	if evt.Info.IsGroup || evt.Info.Chat.Server == types.BroadcastServer || evt.Info.Chat.Server == types.NewsletterServer {
		return nil
	}
	// Revogacao, reacao e chave de sessao nao sao mensagem para ninguem
	// ler; sem este corte elas virariam "conteudo nao suportado" na tela.
	m := evt.Message
	if m == nil || m.GetProtocolMessage() != nil || m.GetReactionMessage() != nil || m.GetSenderKeyDistributionMessage() != nil {
		return nil
	}

	texto := m.GetConversation()
	if texto == "" {
		texto = m.GetExtendedTextMessage().GetText()
	}
	return &Inbound{
		ID:   evt.Info.ID,
		From: evt.Info.Sender.ToNonAD().String(),
		Text: texto,
		// Midia nao entra nesta etapa, mas tem que aparecer: o cliente
		// manda a foto do boleto e escreve "e esse aqui", e sem a marca o
		// atendente le so o "e esse aqui".
		Unsupported: texto == "",
		Timestamp:   evt.Info.Timestamp,
	}
}

func traduzirRecibo(evt *events.Receipt) []Evento {
	var status DeliveryStatus
	switch evt.Type {
	case types.ReceiptTypeDelivered:
		status = DeliveryDelivered
	case types.ReceiptTypeRead, types.ReceiptTypeReadSelf, types.ReceiptTypePlayed:
		status = DeliveryRead
	case types.ReceiptTypeServerError:
		status = DeliveryFailed
	default:
		return nil
	}
	eventos := make([]Evento, 0, len(evt.MessageIDs))
	for _, id := range evt.MessageIDs {
		eventos = append(eventos, Evento{Kind: EventDelivery, Delivery: &Delivery{
			ID:        string(id),
			To:        evt.MessageSource.Chat.ToNonAD().String(),
			Status:    status,
			Timestamp: evt.Timestamp,
		}})
	}
	return eventos
}
