// Package webhook leva ao Mautic o que o gerente de sessoes avisa, e o
// entrega assinado.
//
// O encaixe e o Options.Notify do gerente: Sender.Notify tem exatamente
// aquela assinatura, entao o servico liga os dois passando o metodo. Dali
// saem os tres tipos que o Mautic espera -- message, status e session.
//
// O que este pacote deliberadamente NAO e: uma fila. O buffer e de memoria
// e tem fim. O Mautic ja tem fila duravel, com claim atomico, recuo e
// dedupe por chave; construir uma segunda aqui, com maquina de estados e
// testes proprios, seria duplicar infraestrutura que existe -- numa lingua
// que, como o desenho admite, ninguem naquela casa le. Se o Mautic ficar
// fora do ar mais tempo que o buffer, o que se perde e um evento de entrada
// que o proprio WhatsApp ainda tem.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/macro-markets/whatsqr/session"
)

// Os tres cabecalhos do desenho. Sao constantes exportadas porque o teste
// do outro lado da fronteira -- e quem for ler este arquivo para escrever o
// lado PHP -- precisa do nome exato, e nome de cabecalho digitado duas
// vezes e nome de cabecalho digitado errado uma vez.
const (
	// HeaderKey diz QUAL chave assinou. Nao e segredo: ele so seleciona.
	// Quem prova e a assinatura. E o id da conexao, que nao e sequencial de
	// proposito -- id sequencial entregaria a topologia dos numeros a
	// qualquer um que bata na rota.
	HeaderKey = "X-WhatsQr-Key"
	// HeaderTimestamp e o epoch em segundos, base 10.
	HeaderTimestamp = "X-WhatsQr-Timestamp"
	// HeaderSignature e o HMAC. Formato em sign(), abaixo.
	HeaderSignature = "X-WhatsQr-Signature"
)

const (
	// DefaultBuffer e quantos eventos esperam a vez. Memoria, e nao disco:
	// ver o cabecalho do pacote.
	DefaultBuffer = 256
	// DefaultAttempts conta a primeira tentativa. 5 com recuo dobrando a
	// partir de meio segundo da cerca de sete segundos e meio por evento --
	// o bastante para atravessar um deploy do Mautic, e pouco o bastante
	// para nao prender atras de si a fila inteira do buffer.
	DefaultAttempts = 5
	// DefaultBackoff e o primeiro recuo; ele dobra a cada tentativa.
	DefaultBackoff = 500 * time.Millisecond
	// DefaultTimeout e o prazo de um POST. Sem prazo, um Mautic que aceita
	// a conexao e nunca responde trava o remetente inteiro para sempre.
	DefaultTimeout = 10 * time.Second
)

// Options sao os ajustes do remetente. Todos tem padrao, menos URL e
// Secret, que so quem monta o servico sabe.
type Options struct {
	// URL e a rota do webhook no Mautic.
	URL string

	// Secret devolve o segredo daquela sessao, e false se nao houver.
	//
	// E funcao, e nao string, porque o segredo e por numero e nao global.
	// O que o segredo compra se vazar nao e "mensagem falsa na tela": uma
	// entrada forjada dispara campanha e aciona a IA da caixa, ou seja,
	// compra fazer um numero enviar para um destinatario escolhido pelo
	// atacante. Segredo global faria um vazamento valer por todos os
	// numeros da instalacao de uma vez.
	Secret func(sessionID string) (secret string, ok bool)

	// Buffer e quantos eventos cabem esperando. Cheio, o mais velho sai.
	Buffer int
	// Attempts conta a primeira tentativa, entao 1 significa sem retentativa.
	Attempts int
	// Backoff e o primeiro recuo, que dobra a cada tentativa.
	Backoff time.Duration

	// HTTP e o cliente. Injetavel para o teste apontar para o httptest.
	HTTP *http.Client

	// Now e o relogio, pelo mesmo motivo que o gerente e o state.go recebem
	// o tempo de fora: o timestamp que vai assinado e conferido do outro
	// lado contra uma janela de cinco minutos, e um teste disso nao pode
	// depender do relogio de parede da maquina que roda a suite.
	Now func() time.Time

	// Wait e a espera do recuo; devolve false quando o remetente esta
	// fechando. Injetavel pelo mesmo motivo de Now, e por mais um: sem
	// isso, o teste do recuo demoraria o proprio recuo. E assim que uma
	// suite passa a levar minutos e a falhar por acaso.
	Wait func(ctx context.Context, d time.Duration) bool

	// Logf recebe o que foi descartado e por que. Nao ha descarte em
	// silencio neste pacote: descarte em silencio e alguem perdendo
	// mensagem sem nunca saber que perdeu.
	Logf func(format string, args ...any)
}

// Sender pega o que o gerente notifica e entrega ao Mautic.
//
// Uma goroutine, e nao uma por sessao nem uma por evento. Uma por evento
// faria um Mautic fora do ar virar tantas goroutines em recuo quantos
// eventos chegassem -- e o buffer, que existe justamente para limitar isso,
// nao limitaria nada. Uma por sessao custaria um mapa de goroutines vivas e
// a pergunta de quando matar cada uma. Com uma so, a ordem em que os
// eventos saem e a ordem em que entraram, que importa: um session logged_out
// ultrapassando o pairing que veio depois deixaria o canal marcado como
// caido com a sessao no ar.
//
// O preco esta escrito: um evento em recuo segura os que estao atras. E o
// preco certo, porque o que esta atras vai para o mesmo Mautic que acabou
// de recusar o da frente.
type Sender struct {
	opts   Options
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once

	// mu guarda so a fila. Nunca e segurado durante um POST: uma entrega
	// demorando nao pode fazer Notify -- que roda na goroutine da sessao --
	// esperar por ela.
	mu      sync.Mutex
	pending []*event
	// wake tem capacidade 1 e so leva aviso. Envio nao bloqueante: se ja
	// houver um aviso por ler, o laco ja vai olhar a fila de novo.
	wake chan struct{}
}

// event e o que sai: o corpo ja pronto e a chave que o identifica.
//
// O corpo e montado uma vez, no Notify, e nao a cada tentativa. Duas
// tentativas com corpos diferentes seriam dois eventos para o dedupe do
// Mautic, e a segunda entraria de novo na caixa.
type event struct {
	id        string
	sessionID string
	kind      session.NoticeKind
	body      []byte
}

// New monta o remetente e sobe a goroutine dele. Feche com Close.
func New(opts Options) *Sender {
	if opts.Buffer <= 0 {
		opts.Buffer = DefaultBuffer
	}
	if opts.Attempts <= 0 {
		opts.Attempts = DefaultAttempts
	}
	if opts.Backoff <= 0 {
		opts.Backoff = DefaultBackoff
	}
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{Timeout: DefaultTimeout}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Wait == nil {
		opts.Wait = sleep
	}
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	if opts.Secret == nil {
		// Sem fonte de segredo nada pode sair: assinar com segredo vazio
		// produz um POST que o Mautic recusa de qualquer jeito, e recusado
		// la e um evento perdido sem explicacao aqui.
		opts.Secret = func(string) (string, bool) { return "", false }
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := &Sender{
		opts:   opts,
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
		wake:   make(chan struct{}, 1),
	}
	go s.run()
	return s
}

// Notify e a costura com o gerente: session.Options.Notify = sender.Notify.
//
// Nao bloqueia nunca, e isso e requisito e nao cortesia -- o gerente chama
// isto de dentro da goroutine dona da sessao, e uma sessao parada esperando
// o Mautic e uma sessao que parou de ver queda, volta e mensagem chegando.
func (s *Sender) Notify(n session.Notice) {
	ev, err := newEvent(n)
	if err != nil {
		s.opts.Logf("webhook: aviso da sessao %s descartado: %v", n.SessionID, err)
		return
	}
	s.push(ev)
}

// Close para a goroutine e abandona o que estava esperando.
//
// Abandona de proposito: esperar a fila esvaziar no desligamento faria o
// servico levar minutos para morrer justamente quando o Mautic esta fora do
// ar -- e o que se perde ai e o mesmo que se perde quando o buffer enche,
// que o desenho ja aceitou perder.
func (s *Sender) Close() {
	s.once.Do(func() {
		s.cancel()
		<-s.done
	})
}

// push coloca na fila e, se ela estiver cheia, joga fora o mais velho.
//
// Por que uma fatia sob mutex e nao um canal com buffer: canal cheio so
// deixa descartar o que esta chegando, que e o evento mais novo. Aqui o
// descarte e do mais velho -- o que ja teve mais chance e o que menos
// interessa a tela, porque a tela quer o estado de agora.
func (s *Sender) push(ev *event) {
	var dropped *event

	s.mu.Lock()
	if len(s.pending) >= s.opts.Buffer {
		dropped = s.pending[0]
		// Zerar antes de andar com a fatia solta o evento descartado para o
		// coletor; sem isso ele fica preso no array por baixo ate a
		// proxima realocacao.
		s.pending[0] = nil
		s.pending = s.pending[1:]
	}
	s.pending = append(s.pending, ev)
	s.mu.Unlock()

	if dropped != nil {
		s.opts.Logf("webhook: buffer cheio (%d), descartando o evento mais velho %s da sessao %s -- o Mautic esta fora do ar ou lento demais",
			s.opts.Buffer, dropped.id, dropped.sessionID)
	}

	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Sender) take() *event {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return nil
	}
	ev := s.pending[0]
	s.pending[0] = nil
	s.pending = s.pending[1:]
	return ev
}

// run e a goroutine dona da entrega.
func (s *Sender) run() {
	defer close(s.done)
	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		ev := s.take()
		if ev == nil {
			select {
			case <-s.wake:
				// Aviso podendo ser falso: quem avisou pode ter sido um
				// push cujo evento ja foi levado na volta anterior. Custa
				// uma olhada na fila, e o contrario -- perder um aviso --
				// custaria um evento parado ate o proximo chegar.
			case <-s.ctx.Done():
				return
			}
			continue
		}
		s.deliver(ev)
	}
}

// deliver tenta ate o 200, ate a recusa que nao muda, ou ate acabar a
// paciencia.
func (s *Sender) deliver(ev *event) {
	secret, ok := s.opts.Secret(ev.sessionID)
	if !ok || secret == "" {
		s.opts.Logf("webhook: evento %s descartado: a sessao %s nao tem segredo, e assinar sem segredo e um POST que o Mautic recusa", ev.id, ev.sessionID)
		return
	}

	backoff := s.opts.Backoff
	for attempt := 1; ; attempt++ {
		status, err := s.post(ev, secret)
		switch {
		case err == nil && status >= 200 && status < 300:
			s.opts.Logf("webhook: delivered kind=%s session=%s status=%d", ev.kind, ev.sessionID, status)
			return
		case err == nil && !retryable(status):
			// Tentar de novo um 401 para sempre nao e a mesma coisa que
			// tentar de novo um 503: o 401 nao melhora esperando, e
			// insistir nele gasta as cinco tentativas e depois o buffer --
			// os eventos bons atras dele e que pagariam a conta.
			s.opts.Logf("webhook: evento %s da sessao %s descartado: o Mautic respondeu %d, que nao muda com retentativa", ev.id, ev.sessionID, status)
			return
		}
		if attempt >= s.opts.Attempts {
			s.opts.Logf("webhook: evento %s da sessao %s: desistiu depois de %d tentativas (ultimo resultado: %s)",
				ev.id, ev.sessionID, attempt, outcome(status, err))
			return
		}
		if !s.opts.Wait(s.ctx, backoff) {
			// Fechando: nao adianta recuar para tentar de novo depois.
			return
		}
		backoff *= 2
	}
}

// post assina e manda uma vez. Devolve o codigo, ou o erro de rede.
func (s *Sender) post(ev *event, secret string) (int, error) {
	// O timestamp e refeito a cada tentativa de proposito. Carimbado uma
	// vez so, um recuo longo entregaria ao Mautic um timestamp fora da
	// janela de cinco minutos, e a ultima tentativa -- justo a que teria
	// chance de dar certo -- seria recusada por velha.
	ts := strconv.FormatInt(s.opts.Now().Unix(), 10)

	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, s.opts.URL, bytes.NewReader(ev.body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderKey, ev.sessionID)
	req.Header.Set(HeaderTimestamp, ts)
	req.Header.Set(HeaderSignature, sign(secret, ts, ev.body))

	resp, err := s.opts.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	// Drenar antes de fechar e o que permite reusar a conexao; sem isso,
	// cada evento abre um socket novo para o mesmo Mautic.
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	resp.Body.Close()
	return resp.StatusCode, nil
}

// sign e o formato da assinatura, escrito por extenso porque quem for
// escrever o lado PHP vai ler daqui:
//
//	X-WhatsQr-Signature: "sha256=" + hex(HMAC_SHA256(segredo, timestamp + corpo))
//
// onde timestamp sao os mesmos bytes que vao em X-WhatsQr-Timestamp (epoch
// em segundos, base 10, sem zeros a esquerda) e corpo sao os bytes exatos
// do JSON enviado -- nao o JSON reserializado depois de ler, que reordena
// campo e muda espaco. Em hexadecimal minusculo, que e o que hash_hmac
// devolve. Do outro lado:
//
//	$esperado = 'sha256=' . hash_hmac('sha256', $timestamp . $body, $secret);
//	hash_equals($esperado, $header);   // nunca ===
//
// A assinatura cobre o timestamp E o corpo, juntos. So o corpo faria um
// POST assinado capturado uma vez valer para sempre, bastando reenvia-lo em
// laco; o Mautic recusa timestamp fora de cinco minutos, e e a assinatura
// sobre os dois que impede trocar so o timestamp.
//
// A emenda dos dois sem separador nao e ambigua, e vale dizer por que: o
// corpo e sempre um objeto JSON, entao comeca por '{', que nunca e digito.
// Nao ha como mover a fronteira entre o timestamp e o corpo e obter outro
// par valido.
func sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// retryable separa o que muda sozinho do que nao muda.
//
// 5xx e erro de rede: o Mautic caiu, esta reiniciando, o proxy engasgou.
// Muda sozinho. 429 e 408 sao 4xx que tambem mudam sozinhos -- sao
// justamente o Mautic pedindo para esperar. O resto dos 4xx (401, 403, 404,
// 400, 422) e configuracao errada ou corpo que ele nao aceita: esperar nao
// conserta nenhum dos dois.
func retryable(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	}
	return status >= 500
}

func outcome(status int, err error) string {
	if err != nil {
		return err.Error()
	}
	return strconv.Itoa(status)
}

// sleep e o recuo de verdade, interrompivel pelo fechamento.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// ---------------------------------------------------------------------
// O corpo
// ---------------------------------------------------------------------

// payload e o que o Mautic le. Os nomes sao em snake_case porque do outro
// lado quem le e PHP.
type payload struct {
	// ID e a chave do dedupe do Mautic. Primeiro campo porque e a primeira
	// coisa que o outro lado olha.
	ID        string             `json:"id"`
	Type      session.NoticeKind `json:"type"`
	SessionID string             `json:"session_id"`
	State     session.State      `json:"state,omitempty"`
	JID       string             `json:"jid,omitempty"`
	Reason    string             `json:"reason,omitempty"`
	Message   *inboundBody       `json:"message,omitempty"`
	Status    *statusBody        `json:"status,omitempty"`
}

type inboundBody struct {
	ID          string `json:"id"`
	From        string `json:"from"`
	Name        string `json:"name,omitempty"`
	Text        string `json:"text"`
	Timestamp   int64  `json:"timestamp"`
	Unsupported bool   `json:"unsupported"`
}

type statusBody struct {
	ID        string                 `json:"id"`
	To        string                 `json:"to"`
	Status    session.DeliveryStatus `json:"status"`
	Timestamp int64                  `json:"timestamp"`
}

func newEvent(n session.Notice) (*event, error) {
	if n.SessionID == "" {
		return nil, errors.New("aviso sem sessao")
	}

	body := payload{
		Type:      n.Kind,
		SessionID: n.SessionID,
		State:     n.State,
		JID:       n.JID,
		Reason:    n.Reason,
	}

	switch n.Kind {
	case session.NoticeMessage:
		if n.Message == nil {
			// Deixar passar viraria um corpo com message nulo, e a caixa
			// ganharia uma conversa com mensagem vazia dentro. Recusar aqui
			// e o unico lugar onde ainda da para dizer de qual sessao veio.
			return nil, errors.New("aviso de mensagem sem mensagem")
		}
		body.Message = &inboundBody{
			ID:          n.Message.ID,
			From:        n.Message.From,
			Name:        n.Message.Name,
			Text:        n.Message.Text,
			Timestamp:   n.Message.Timestamp.Unix(),
			Unsupported: n.Message.Unsupported,
		}
	case session.NoticeStatus:
		if n.Delivery == nil {
			return nil, errors.New("aviso de status sem status")
		}
		body.Status = &statusBody{
			ID:        n.Delivery.ID,
			To:        n.Delivery.To,
			Status:    n.Delivery.Status,
			Timestamp: n.Delivery.Timestamp.Unix(),
		}
	case session.NoticeSession:
	default:
		return nil, fmt.Errorf("tipo de aviso desconhecido %q", n.Kind)
	}

	body.ID = eventID(n)

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return &event{id: body.ID, sessionID: n.SessionID, kind: n.Kind, body: raw}, nil
}

// eventID da a cada evento a chave pela qual o Mautic deduplica.
//
// Sem chave propria nao ha dedupe para status e session, e sem dedupe
// reenviar em laco um session logged_out capturado mantem o canal marcado
// como caido indefinidamente -- negacao de servico sem tocar no servico. A
// deduplicacao por id de mensagem, que o desenho ja tinha, so cobre message.
//
// A chave e calculada uma vez, aqui, e viaja com o evento: recalcula-la a
// cada tentativa faria as retentativas do mesmo evento entrarem como
// eventos diferentes, que e exatamente o que o dedupe existe para evitar.
func eventID(n session.Notice) string {
	switch n.Kind {
	case session.NoticeMessage:
		if n.Message != nil && n.Message.ID != "" {
			// O WhatsApp ja da um id estavel. Usa-lo faz o dedupe valer
			// tambem para a reentrega que o whatsmeow faz na volta de uma
			// queda -- a mesma mensagem chegando de novo nao vira uma
			// segunda bolha na conversa.
			return "msg:" + n.Message.ID
		}
	case session.NoticeStatus:
		if n.Delivery != nil && n.Delivery.ID != "" {
			// O andar da entrega entra na chave porque "entregue" e "lido"
			// da mesma mensagem sao dois eventos: so o id os faria colidir,
			// e o segundo sumiria no dedupe.
			return "status:" + n.Delivery.ID + ":" + string(n.Delivery.Status)
		}
	}
	// session nao tem nada estavel para derivar -- e nem poderia ter. Uma
	// sessao cai, volta e cai de novo, e as duas quedas sao eventos
	// diferentes; chave derivada do estado faria a segunda sumir no dedupe
	// do Mautic e a tela mostraria a sessao no ar depois de ela ter caido.
	return string(n.Kind) + ":" + randomID()
}

// fallbackSeq so entra em cena se o crypto/rand falhar, o que na pratica
// significa o sistema sem entropia. Mesmo ai a chave precisa ser unica no
// processo, porque duas chaves iguais somem uma na outra no dedupe.
var fallbackSeq atomic.Uint64

func randomID() string {
	var b [16]byte
	if _, err := crand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d-%d", time.Now().UnixNano(), fallbackSeq.Add(1))
	}
	return hex.EncodeToString(b[:])
}
