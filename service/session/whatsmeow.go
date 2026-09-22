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

// Este e o unico arquivo do pacote que conhece o whatsmeow, e e a borda.
// O cliente nao tem teste de unidade porque testa-lo exigiria um WhatsApp
// de verdade; o que da para errar nele aparece nos testes de ponta a ponta,
// num numero que pode ser banido. O store tem: abrir aparelho e achar
// credencial nao fala com o WhatsApp, e e o que o whatsmeow_test.go cobre.
//
// Aqui tambem moram os dois jeitos de escrever o mesmo numero, e essa e a
// razao de eles nao aparecerem em lugar nenhum acima desta linha. O
// whatsmeow indexa credenciais pelo JID COM aparelho dentro
// (5511999999999:12@s.whatsapp.net); os eventos, o Notice e o state.go
// falam a forma SEM aparelho (5511999999999@s.whatsapp.net), porque e ela
// que identifica o chip, e nao a instalacao. Qual das duas usar e detalhe
// do protocolo do WhatsApp, e protocolo mora na borda: o resto do servico
// entrega e recebe a forma sem aparelho, e a traducao acontece aqui.

// filaDeEventos e o tamanho do buffer entre o whatsmeow e o gerente. O
// whatsmeow chama os handlers na propria goroutine que le o socket, entao
// segurar um handler segura a conexao inteira. O buffer da folga para o
// gerente aplicar a transicao sem que a leitura do socket espere.
const eventBuffer = 64

// esperaDoPrimeiroQr e quanto se espera pelo primeiro QR antes de desistir.
// A rota POST /sessions responde dentro da requisicao: sem prazo aqui, um
// WhatsApp mudo deixaria o atendente olhando uma tela girando.
const firstQRTimeout = 30 * time.Second

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

// StdoutLogger monta o log da biblioteca a partir do nivel escrito na
// configuracao: DEBUG, INFO, WARN ou ERROR.
//
// Existe para que quem sobe o servico escolha o nivel sem precisar nomear o
// tipo de log do whatsmeow -- ou seja, sem importar a biblioteca. Sem ele,
// o main precisaria de um import so para construir este argumento, e a
// fronteira que este arquivo guarda cairia pelo lado mais bobo possivel.
//
// Sem cor: quem le isto e o journal do systemd, e la o codigo de escape
// vira lixo no meio da linha.
func StdoutLogger(level string) waLog.Logger {
	if level == "" {
		level = "WARN"
	}
	return waLog.Stdout("whatsmeow", level, false)
}

// OpenStore abre (e migra) o arquivo de sessoes.
func OpenStore(ctx context.Context, path string, log waLog.Logger) (*WhatsmeowStore, error) {
	if log == nil {
		log = waLog.Noop
	}
	// O dialeto e "sqlite" e nao "sqlite3" porque esse e o nome com que o
	// modernc se registra no database/sql. As pragmas vao na propria URL,
	// no formato do modernc, que nao e o do mattn: foreign_keys porque o
	// esquema do whatsmeow depende delas para limpar sessao antiga, e
	// busy_timeout porque o arquivo e tocado por varias sessoes ao mesmo
	// tempo e "database is locked" aqui e uma sessao que cai.
	address := fmt.Sprintf(
		"file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate",
		path,
	)
	container, err := sqlstore.New(ctx, "sqlite", address, log)
	if err != nil {
		return nil, fmt.Errorf("session: abrindo o store: %w", err)
	}
	return &WhatsmeowStore{container: container, log: log}, nil
}

// Devices lista os JIDs ja pareados. E por aqui que o servico sabe quais
// sessoes religar depois de um reinicio, sem pedir QR de novo.
func (s *WhatsmeowStore) Devices(ctx context.Context) ([]string, error) {
	devices, err := s.container.GetAllDevices(ctx)
	if err != nil {
		return nil, err
	}
	// Sai a forma sem aparelho, que e a que o servico fala: assim a lista
	// pode ser comparada direto com o que o de-para guardou, sem ninguem la
	// fora precisar saber que existe forma com aparelho.
	//
	// Repetido nao aparece duas vezes -- a lista e de numeros, e um numero
	// com duas credenciais continua sendo um numero. Quem cobra essa
	// duplicidade e devicesOf, que e quem precisa escolher uma.
	seen := make(map[string]struct{}, len(devices))
	jids := make([]string, 0, len(devices))
	for _, a := range devices {
		if a.ID == nil {
			continue
		}
		jid := a.ID.ToNonAD().String()
		if _, dup := seen[jid]; dup {
			continue
		}
		seen[jid] = struct{}{}
		jids = append(jids, jid)
	}
	return jids, nil
}

// ErrAmbiguousJID e o numero que tem mais de uma credencial no disco.
// Sentinela porque nao e falta de credencial nem erro de digitacao: e uma
// escolha que este arquivo se recusa a fazer sozinho.
var ErrAmbiguousJID = errors.New("session: mais de uma credencial para o mesmo numero")

// Open devolve o cliente de um numero. JID vazio e aparelho novo, que vai
// pedir QR; JID preenchido e a sessao que ja estava no disco.
//
// O JID entra na forma sem aparelho -- a mesma que sai nos eventos e que o
// de-para guarda. A forma com aparelho tambem e aceita, porque ela ainda e
// o indice de verdade e ha um caminho direto para ela.
//
// O de-para entre o id de sessao do servico e o JID nao esta aqui de
// proposito: e da camada que guarda a configuracao. Este arquivo so sabe
// achar credencial e abrir aparelho.
func (s *WhatsmeowStore) Open(ctx context.Context, jid string) (Client, error) {
	if jid == "" {
		return newWhatsmeowClient(s.container.NewDevice(), s.log), nil
	}
	found, err := s.devicesOf(ctx, jid)
	if err != nil {
		return nil, err
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("session: o numero %q nao tem credencial no store", jid)
	case 1:
		return newWhatsmeowClient(found[0], s.log), nil
	}
	// Escolher uma das duas aqui seria abrir a credencial errada em metade
	// das vezes, calado. Recusar e a unica saida que nao inventa uma
	// decisao que nao e deste arquivo.
	return nil, fmt.Errorf("%w: %s tem %d credenciais no disco (%s)",
		ErrAmbiguousJID, jid, len(found), strings.Join(deviceIDs(found), ", "))
}

// devicesOf acha as credenciais daquele numero. E a traducao de dialeto
// propriamente dita: um JID com aparelho e uma busca direta no indice; um
// sem aparelho e uma varredura comparando os dois lados na forma sem
// aparelho, que e a definicao que a propria biblioteca da de "mesmo
// numero".
//
// A varredura custa a lista inteira de credenciais. Sao ate cinco numeros,
// e o alternativo seria um LIKE sobre a coluna jid -- consulta que depende
// do formato do JID em texto e que quebra calada no dia em que a biblioteca
// mudar esse formato.
func (s *WhatsmeowStore) devicesOf(ctx context.Context, jid string) ([]*store.Device, error) {
	parsed, err := types.ParseJID(jid)
	if err != nil {
		return nil, fmt.Errorf("session: jid %q: %w", jid, err)
	}
	if parsed.Device != 0 || parsed.RawAgent != 0 {
		device, err := s.container.GetDevice(ctx, parsed)
		if err != nil {
			return nil, fmt.Errorf("session: buscando o aparelho %q: %w", jid, err)
		}
		if device == nil {
			return nil, nil
		}
		return []*store.Device{device}, nil
	}

	all, err := s.container.GetAllDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("session: listando os aparelhos: %w", err)
	}
	wanted := parsed.ToNonAD().String()
	var found []*store.Device
	for _, device := range all {
		if device.ID != nil && device.ID.ToNonAD().String() == wanted {
			found = append(found, device)
		}
	}
	return found, nil
}

func deviceIDs(devices []*store.Device) []string {
	ids := make([]string, 0, len(devices))
	for _, device := range devices {
		ids = append(ids, device.ID.String())
	}
	return ids
}

// Forget apaga do disco a credencial daquele numero. E o que faz DELETE
// /sessions nao deixar para tras uma credencial que ainda fala pelo numero.
//
// Ao contrario de Open, aqui um numero com duas credenciais nao vira
// recusa: apaga as duas. A frase acima e o contrato, e deixar a segunda no
// disco seria cumprir metade dele -- justamente a metade que importa.
//
// Apagar o que nao esta la nao e erro: o DELETE precisa poder ser repetido
// depois de uma falha no meio.
func (s *WhatsmeowStore) Forget(ctx context.Context, jid string) error {
	found, err := s.devicesOf(ctx, jid)
	if err != nil {
		return err
	}
	for _, device := range found {
		if err := s.container.DeleteDevice(ctx, device); err != nil {
			return fmt.Errorf("session: apagando o aparelho %s: %w", device.ID, err)
		}
	}
	return nil
}

// Close fecha o arquivo.
func (s *WhatsmeowStore) Close() error { return s.container.Close() }

// whatsmeowClient implementa Client com a biblioteca de verdade.
type whatsmeowClient struct {
	cli    *whatsmeow.Client
	outbox chan Event

	// stopped e fechado por Disconnect. Todo envio para o canal de eventos
	// escuta ele tambem: sem isso, um handler do whatsmeow ficaria preso
	// tentando entregar evento para um gerente que ja foi embora, e com ele
	// a goroutine que le o socket.
	stopped  chan struct{}
	stopOnce sync.Once

	// fecharJanela solta o contexto do pareamento. Guardado aqui, e nao
	// esquecido num defer, porque a janela tem que sobreviver a funcao que a
	// abriu -- e sem alguem para fecha-la sobraria uma goroutine por sessao.
	fecharJanela context.CancelFunc

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
	pairing bool
	// caiu diz se houve queda de verdade desde a ultima conexao. E o que
	// separa o Connected que e volta do Connected que e so a conexao
	// inicial -- e e por isso que a distincao entre EventPaired e
	// EventResumed pode ser feita aqui, na borda, e nao no gerente.
	dropped bool
}

func newWhatsmeowClient(device *store.Device, log waLog.Logger) *whatsmeowClient {
	cli := whatsmeow.NewClient(device, log)
	// Reconexao automatica ligada: a queda e rotina neste canal, e o
	// whatsmeow volta sozinho. Quem decide quando desistir continua sendo a
	// janela do state.go, cobrada pelo gerente.
	cli.EnableAutoReconnect = true
	c := &whatsmeowClient{
		cli:     cli,
		outbox:  make(chan Event, eventBuffer),
		stopped: make(chan struct{}),
	}
	if device.ID != nil {
		c.jid = device.ID.ToNonAD().String()
	}
	return c
}

func (c *whatsmeowClient) Connect(ctx context.Context) (<-chan Event, error) {
	var qrChan <-chan whatsmeow.QRChannelItem
	isNew := c.cli.Store.ID == nil
	if isNew {
		// GetQRChannel tem que vir antes de Connect: e ele que assina os
		// eventos de pareamento.
		var err error
		// O contexto do pareamento NAO pode ser o do pedido HTTP. Quem
		// escaneia e uma pessoa, e ela chega depois de a resposta do POST ter
		// sido escrita -- momento em que o contexto do pedido e cancelado e o
		// emissor do whatsmeow para na mesma linha de log em que comecou.
		janela, fecharJanela := qrContext(ctx, c.stopped)
		c.fecharJanela = fecharJanela
		qrChan, err = c.cli.GetQRChannel(janela)
		if err != nil {
			fecharJanela()
			return nil, fmt.Errorf("session: canal de qr: %w", err)
		}
	}

	c.cli.AddEventHandler(c.translate)

	if err := c.cli.Connect(); err != nil {
		return nil, fmt.Errorf("session: conectando: %w", err)
	}

	if isNew {
		if err := c.firstQR(ctx, qrChan); err != nil {
			c.cli.Disconnect()
			return nil, err
		}
		go c.followQR(qrChan)
	}
	return c.outbox, nil
}

// primeiroQr espera o codigo que a resposta de POST /sessions precisa
// carregar. A interface promete que, quando Connect volta, ou ha QR ou ha
// chip -- e e aqui que essa promessa e paga.
func (c *whatsmeowClient) firstQR(ctx context.Context, qrChan <-chan whatsmeow.QRChannelItem) error {
	deadline, cancel := context.WithTimeout(ctx, firstQRTimeout)
	defer cancel()
	for {
		select {
		case item, ok := <-qrChan:
			if !ok {
				return errors.New("session: o canal de qr fechou antes do primeiro codigo")
			}
			if item.Event == whatsmeow.QRChannelEventCode {
				c.setQR(item.Code)
				return nil
			}
			if item.Error != nil {
				return fmt.Errorf("session: pareamento: %w", item.Error)
			}
			return fmt.Errorf("session: pareamento terminou em %q antes do primeiro codigo", item.Event)
		case <-deadline.Done():
			return fmt.Errorf("session: sem qr depois de %s: %w", firstQRTimeout, deadline.Err())
		case <-c.stopped:
			return ErrSessionClosed
		}
	}
}

// seguirQr acompanha as renovacoes e o desfecho do pareamento.
func (c *whatsmeowClient) followQR(qrChan <-chan whatsmeow.QRChannelItem) {
	// anunciado diz se a janela ja teve desfecho proprio. Sem isso, o fim do
	// canal seria indistinguivel de um pareamento que deu certo.
	anunciado := false
	for item := range qrChan {
		switch item.Event {
		case whatsmeow.QRChannelEventCode:
			c.setQR(item.Code)
		case whatsmeow.QRChannelSuccess.Event:
			// Quem muda o estado e o PairSuccess; aqui so se apaga o codigo
			// para a tela nao continuar oferecendo um QR ja usado.
			c.setQR("")
			anunciado = true
		default:
			c.setQR("")
			anunciado = true
			// A tela de pareamento distingue "expirou" de "o WhatsApp
			// recusou", e so o primeiro oferece tentar de novo. Por isso o
			// motivo viaja com o desfecho do whatsmeow dentro, em vez de um
			// "falhou" generico.
			reason := item.Event
			if item.Error != nil {
				reason = fmt.Sprintf("%s: %s", item.Event, item.Error)
			}
			c.emit(Event{Kind: EventFailed, Reason: reason})
		}
	}

	if anunciado {
		return
	}

	// O canal acabou sem dizer por que. Acontece: o emissor do whatsmeow tem
	// caminhos em que ele so fecha a saida, e a janela inteira dura menos de
	// tres minutos -- seis codigos, o primeiro de 60 segundos e os outros de 20.
	//
	// Sem este fecho, o ultimo codigo fica guardado para sempre: o servico
	// continua entregando um QR que nao pareia mais e continua dizendo
	// "pareando". A tela pede para escanear, o atendente escaneia, o WhatsApp
	// recusa, e nao ha nada escrito em lugar nenhum dizendo que a janela fechou
	// ha horas. Foi exatamente assim que um numero ficou dezenove horas parado
	// oferecendo um codigo morto.
	c.setQR("")
	c.emit(Event{Kind: EventFailed, Reason: "a janela de pareamento expirou sem que ninguem escaneasse"})
}

func (c *whatsmeowClient) setQR(qr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.qr = qr
}

func (c *whatsmeowClient) CurrentQR() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.qr
}

func (c *whatsmeowClient) JID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.jid
}

func (c *whatsmeowClient) SendText(ctx context.Context, to, text string) (string, error) {
	dest, err := toJID(to)
	if err != nil {
		return "", err
	}
	resp, err := c.cli.SendMessage(ctx, dest, &waE2E.Message{Conversation: proto.String(text)})
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
func toJID(to string) (types.JID, error) {
	if strings.Contains(to, "@") {
		jid, err := types.ParseJID(to)
		if err != nil {
			return types.EmptyJID, fmt.Errorf("session: destino %q: %w", to, err)
		}
		return jid, nil
	}
	number := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, to)
	if number == "" {
		return types.EmptyJID, fmt.Errorf("session: destino %q nao tem numero", to)
	}
	return types.NewJID(number, types.DefaultUserServer), nil
}

func (c *whatsmeowClient) Disconnect() {
	c.stopOnce.Do(func() { close(c.stopped) })
	if nil != c.fecharJanela {
		c.fecharJanela()
	}
	c.cli.Disconnect()
}

// qrContext da ao pareamento um tempo de vida proprio: o do pedido que abriu a
// sessao nao serve, e o de fundo sozinho vazaria.
//
// O emissor de codigos do whatsmeow observa o contexto que recebe. Recebendo o
// do pedido HTTP, ele para no instante em que a resposta e escrita -- medido em
// zero milissegundo entre "Emitting QR code" e "Context is done, stopping QR
// emitter". O codigo entregue na resposta ja nascia morto, e de fora isso e
// indistinguivel do WhatsApp recusando o numero.
func qrContext(pedido context.Context, parada <-chan struct{}) (context.Context, context.CancelFunc) {
	// WithoutCancel mantem os valores do pedido (log, rastreamento) e larga o
	// cancelamento, que e justamente a parte que nao serve aqui.
	janela, fechar := context.WithCancel(context.WithoutCancel(pedido))
	go func() {
		select {
		case <-parada:
			fechar()
		case <-janela.Done():
		}
	}()

	return janela, fechar
}

// mandar entrega o evento ao gerente sem nunca bloquear para sempre.
func (c *whatsmeowClient) emit(ev Event) {
	select {
	case c.outbox <- ev:
	case <-c.stopped:
	}
}

// traduzir e a fronteira propriamente dita: entra tipo do whatsmeow, sai
// Event deste pacote. Nenhuma regra de transicao mora aqui -- o que se
// decide e apenas qual evento aconteceu, nao o que ele significa para a
// sessao.
func (c *whatsmeowClient) translate(raw any) {
	switch evt := raw.(type) {
	case *events.PairSuccess:
		jid := evt.ID.ToNonAD().String()
		c.mu.Lock()
		c.pairing = true
		c.dropped = false
		c.jid = jid
		c.mu.Unlock()
		c.emit(Event{Kind: EventPaired, JID: jid})

	case *events.Connected:
		c.mu.Lock()
		pairing, dropped := c.pairing, c.dropped
		c.pairing = false
		c.dropped = false
		c.mu.Unlock()
		if pairing || !dropped {
			// A primeira conexao de uma sessao ja pareada nao e uma volta:
			// o gerente ja a abriu como conectada. Anuncia-la como volta
			// seria uma transicao que o state.go recusa com razao.
			return
		}
		c.emit(Event{Kind: EventResumed, JID: c.JID()})

	case *events.Disconnected:
		c.mu.Lock()
		pairing := c.pairing
		if !pairing {
			c.dropped = true
		}
		c.mu.Unlock()
		if pairing {
			// Queda do proprio handshake de pareamento, nao da linha.
			return
		}
		c.emit(Event{Kind: EventDropped})

	case *events.LoggedOut:
		c.emit(Event{Kind: EventLoggedOut, Reason: evt.Reason.String()})

	case *events.StreamReplaced:
		// Outro cliente assumiu a sessao. Nao adianta esperar: enquanto o
		// outro estiver de pe, este nao volta.
		c.emit(Event{Kind: EventFailed, Reason: "a sessao foi assumida por outro cliente"})

	case *events.TemporaryBan:
		c.emit(Event{Kind: EventFailed, Reason: evt.String()})

	case *events.ClientOutdated:
		c.emit(Event{Kind: EventFailed, Reason: "versao do cliente recusada pelo WhatsApp"})

	case *events.ConnectFailure:
		c.emit(Event{Kind: EventFailed, Reason: fmt.Sprintf("%s: %s", evt.Reason, evt.Message)})

	case *events.Message:
		if msg := translateMessage(evt); msg != nil {
			c.emit(Event{Kind: EventMessage, Message: msg})
		}

	case *events.Receipt:
		for _, ev := range translateReceipt(evt) {
			c.emit(ev)
		}
	}
}

func translateMessage(evt *events.Message) *Inbound {
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

	text := m.GetConversation()
	if text == "" {
		text = m.GetExtendedTextMessage().GetText()
	}
	return &Inbound{
		ID:   evt.Info.ID,
		From: evt.Info.Sender.ToNonAD().String(),
		Text: text,
		// Midia nao entra nesta etapa, mas tem que aparecer: o cliente
		// manda a foto do boleto e escreve "e esse aqui", e sem a marca o
		// atendente le so o "e esse aqui".
		Unsupported: text == "",
		Timestamp:   evt.Info.Timestamp,
	}
}

func translateReceipt(evt *events.Receipt) []Event {
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
	out := make([]Event, 0, len(evt.MessageIDs))
	for _, id := range evt.MessageIDs {
		out = append(out, Event{Kind: EventDelivery, Delivery: &Delivery{
			ID:        string(id),
			To:        evt.MessageSource.Chat.ToNonAD().String(),
			Status:    status,
			Timestamp: evt.Timestamp,
		}})
	}
	return out
}
