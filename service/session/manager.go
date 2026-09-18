package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// DefaultSweep e de quanto em quanto o gerente cobra a janela de reconexao
// das sessoes. Nada aqui decide quando a janela vence -- isso e do
// state.go; o gerente so aparece com o relogio na mao, porque a maquina de
// estados nao chama time.Now e alguem precisa chamar.
const DefaultSweep = 10 * time.Second

// Motivos de recusa do gerente. Sao sentinelas pelo mesmo motivo dos do
// state.go: cada um vira uma resposta HTTP diferente na tarefa das rotas.
var (
	ErrUnknownSession = errors.New("session: sessao desconhecida")
	ErrSessionExists  = errors.New("session: sessao ja aberta")
	ErrSessionClosed  = errors.New("session: sessao encerrada")
	// ErrNotConnected e a recusa que a rota de envio traduz para a falha
	// temporaria. Confundi-la com uma permanente mostra "nao saiu" ao
	// atendente enquanto a sessao ainda esta tentando voltar.
	ErrNotConnected = errors.New("session: sessao nao esta conectada")
	ErrNoQR         = errors.New("session: sessao sem qr para mostrar")
	ErrEmptyID      = errors.New("session: id de sessao vazio")
	// ErrNotReady e o cliente que conectou sem QR e sem chip: nao da para
	// mostrar pareamento nem dar a sessao por pareada.
	ErrNotReady = errors.New("session: cliente conectou sem qr e sem jid")
)

// Snapshot e uma sessao vista de fora, num instante. Copia, e nao ponteiro:
// quem le nao pode alcancar a maquina de estados, que pertence a uma
// goroutine so.
type Snapshot struct {
	ID     string
	State  State
	JID    string
	QR     string
	Reason string // preenchido em Failed
	// LastRefusal e a ultima transicao que a maquina de estados recusou.
	// Existe porque um evento recusado nao tem a quem devolver erro -- ele
	// veio de um canal, nao de uma chamada -- e engoli-lo em silencio deixa
	// uma sessao presa sem que nada explique por que.
	LastRefusal string
}

// NoticeKind e o tipo do aviso que sai do servico. Os tres valores sao os
// tres tipos de evento que o webhook do desenho ja carrega.
type NoticeKind string

const (
	NoticeSession NoticeKind = "session"
	NoticeMessage NoticeKind = "message"
	NoticeStatus  NoticeKind = "status"
)

// Notice e o que o gerente conta para fora. Quem escuta, na tarefa do
// webhook, e quem decide o que fazer com isso.
type Notice struct {
	SessionID string
	Kind      NoticeKind
	State     State
	JID       string
	Reason    string
	Message   *Inbound
	Delivery  *Delivery
}

// Options sao os ajustes do gerente. Todos tem padrao; o teste mexe no
// relogio e na cobranca para nao esperar cinco minutos.
type Options struct {
	// Window e a janela de reconexao passada a cada sessao nova.
	Window time.Duration
	// Now e o relogio. Injetavel pelo mesmo motivo que o state.go recebe o
	// tempo por parametro: teste de prazo nao pode depender de esperar.
	Now func() time.Time
	// Sweep e o intervalo entre cobrancas da janela.
	Sweep time.Duration
	// Notify recebe os avisos. E chamado na goroutine da sessao, entao quem
	// escuta nao pode bloquear -- e nao pode chamar o gerente de volta de
	// dentro dele, que seria a goroutine da sessao esperando por si mesma.
	Notify func(Notice)
}

// Manager e o dono das sessoes: abre, guarda, encaminha evento para a
// maquina de estados e fecha.
//
// Regra de transicao nenhuma mora aqui. O gerente traduz evento em chamada
// e aceita a resposta que o state.go der, inclusive quando e nao.
type Manager struct {
	dial func(id string) (Cliente, error)
	opts Options

	// mu guarda so o mapa de sessoes, e nunca e segurado durante uma
	// chamada de rede: uma sessao demorando para parear nao pode travar o
	// /health das outras quatro.
	mu       sync.RWMutex
	sessions map[string]*live
	opening  map[string]struct{}
	closed   bool
}

// live e uma sessao aberta, com a goroutine que a possui.
//
// Como a concorrencia foi resolvida, e por que: o state.go diz, de
// proposito, que a Session nao e segura para uso concorrente, e deixa a
// serializacao para o dono. O dono e este. Cada sessao tem uma goroutine
// so, e essa goroutine e a unica que toca em state -- venha o toque de um
// evento do WhatsApp, de um tique do relogio ou de uma requisicao HTTP.
//
// A escolha foi por goroutine e nao por mutex porque os eventos ja chegam
// por um canal e a janela ja precisa ser cobrada por um tique: os dois
// caminhos exigem um laco de select de qualquer jeito. Com o laco no lugar,
// um mutex seria um segundo mecanismo dizendo a mesma coisa -- e dois
// mecanismos e como nasce a ordem de aquisicao que ninguem documentou.
type live struct {
	id      string
	cliente Cliente
	cmds    chan func()
	quit    chan struct{}
	done    chan struct{}
	once    sync.Once

	// state e lastRefusal so sao tocados pela goroutine dona.
	state       *Session
	lastRefusal error
}

// NewManager monta o gerente. dial abre o cliente de um id -- no servico e
// o whatsmeow; no teste, a mentira.
func NewManager(dial func(id string) (Cliente, error), opts Options) *Manager {
	if dial == nil {
		dial = func(string) (Cliente, error) {
			return nil, errors.New("session: gerente sem fabrica de cliente")
		}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Sweep <= 0 {
		opts.Sweep = DefaultSweep
	}
	return &Manager{
		dial:     dial,
		opts:     opts,
		sessions: map[string]*live{},
		opening:  map[string]struct{}{},
	}
}

// Open abre uma sessao e devolve o que a tela de pareamento precisa. Uma
// sessao restaurada do disco volta ja conectada, sem QR.
func (m *Manager) Open(ctx context.Context, id string) (Snapshot, error) {
	if id == "" {
		return Snapshot{}, ErrEmptyID
	}
	if err := m.reserve(id); err != nil {
		return Snapshot{}, err
	}
	// A reserva sai do caminho de qualquer jeito: sem isto, um pareamento
	// que falhou deixaria o id ocupado ate reiniciar o servico.
	defer m.release(id)

	cliente, err := m.dial(id)
	if err != nil {
		return Snapshot{}, err
	}
	eventos, err := cliente.Conectar(ctx)
	if err != nil {
		cliente.Desconectar()
		return Snapshot{}, err
	}
	if eventos == nil {
		cliente.Desconectar()
		return Snapshot{}, ErrNotReady
	}

	lv := &live{
		id:      id,
		cliente: cliente,
		cmds:    make(chan func()),
		quit:    make(chan struct{}),
		done:    make(chan struct{}),
		state:   New(m.opts.Window),
	}

	qr := cliente.QrAtual()
	if qr == "" {
		// Sessao que voltou do disco ja pareada: nao havera scan nenhum, e
		// sem isto ela ficaria em pairing para sempre mostrando um QR que
		// nao existe. O chip e o do disco, entao a regra do JID continua
		// valendo a partir dele.
		jid := cliente.Jid()
		if jid == "" {
			cliente.Desconectar()
			return Snapshot{}, ErrNotReady
		}
		if err := lv.state.Scanned(jid); err != nil {
			cliente.Desconectar()
			return Snapshot{}, err
		}
	}

	// O retrato sai antes da goroutine subir: depois disso, ler state daqui
	// seria justamente a corrida que este arquivo existe para nao ter.
	snap := Snapshot{ID: id, State: lv.state.State(), JID: lv.state.JID(), QR: qr}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cliente.Desconectar()
		return Snapshot{}, ErrSessionClosed
	}
	m.sessions[id] = lv
	m.mu.Unlock()

	go m.run(lv, eventos)
	return snap, nil
}

// reserve marca o id como em abertura e solta o lock. Conectar fala com o
// WhatsApp e pode demorar; segurar o lock do mapa ate la faria uma sessao
// lenta travar todas as outras.
func (m *Manager) reserve(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrSessionClosed
	}
	if _, ok := m.sessions[id]; ok {
		return ErrSessionExists
	}
	if _, ok := m.opening[id]; ok {
		return ErrSessionExists
	}
	m.opening[id] = struct{}{}
	return nil
}

func (m *Manager) release(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.opening, id)
}

// QR devolve o QR de agora. Fora do pareamento nao ha QR, e dizer isso e
// diferente de devolver vazio: a tela distingue "ainda nao chegou" de "esta
// sessao nao esta pareando".
func (m *Manager) QR(id string) (string, error) {
	snap, err := m.Snapshot(id)
	if err != nil {
		return "", err
	}
	if snap.QR == "" {
		return "", ErrNoQR
	}
	return snap.QR, nil
}

// Snapshot e o retrato de uma sessao.
func (m *Manager) Snapshot(id string) (Snapshot, error) {
	lv, err := m.find(id)
	if err != nil {
		return Snapshot{}, err
	}
	return m.retrato(lv)
}

// Status e o retrato de todas, para o /health.
func (m *Manager) Status() []Snapshot {
	m.mu.RLock()
	vivas := make([]*live, 0, len(m.sessions))
	for _, lv := range m.sessions {
		vivas = append(vivas, lv)
	}
	m.mu.RUnlock()

	snaps := make([]Snapshot, 0, len(vivas))
	for _, lv := range vivas {
		// Uma sessao que fechou entre o mapa e o retrato simplesmente nao
		// aparece: /health responde o que existe agora, e nao um erro por
		// causa de quem saiu no meio.
		if snap, err := m.retrato(lv); err == nil {
			snaps = append(snaps, snap)
		}
	}
	return snaps
}

// Send confere o estado dentro da goroutine dona e envia fora dela.
func (m *Manager) Send(ctx context.Context, id, para, texto string) (string, error) {
	lv, err := m.find(id)
	if err != nil {
		return "", err
	}

	var cliente Cliente
	if err := lv.ask(func() {
		if lv.state.State() == Connected {
			cliente = lv.cliente
		}
	}); err != nil {
		return "", err
	}
	if cliente == nil {
		return "", ErrNotConnected
	}

	// A rede acontece fora da goroutine da sessao de proposito: segurar a
	// sessao durante um envio faria a queda que chega no meio esperar o
	// envio terminar -- e e justamente a queda que precisa ser vista
	// depressa. O preco e a corrida entre a conferencia e o envio; se a
	// sessao cair nesse intervalo, quem recusa e o WhatsApp, que e quem
	// sabe a verdade nesse instante.
	return cliente.EnviarTexto(ctx, para, texto)
}

// Close desconecta e esquece a sessao.
func (m *Manager) Close(id string) error {
	m.mu.Lock()
	lv, ok := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if !ok {
		return ErrUnknownSession
	}
	lv.shutdown()
	return nil
}

// Shutdown fecha todas. Depois dele o gerente nao abre mais nada.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.closed = true
	vivas := make([]*live, 0, len(m.sessions))
	for id, lv := range m.sessions {
		vivas = append(vivas, lv)
		delete(m.sessions, id)
	}
	m.mu.Unlock()

	for _, lv := range vivas {
		lv.shutdown()
	}
}

func (m *Manager) find(id string) (*live, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	lv, ok := m.sessions[id]
	if !ok {
		return nil, ErrUnknownSession
	}
	return lv, nil
}

func (m *Manager) retrato(lv *live) (Snapshot, error) {
	var snap Snapshot
	err := lv.ask(func() {
		snap = Snapshot{
			ID:     lv.id,
			State:  lv.state.State(),
			JID:    lv.state.JID(),
			Reason: lv.state.FailureReason(),
		}
		if snap.State == Pairing {
			// O QR e lido do cliente, e nao de uma copia guardada na
			// abertura: ele e renovado durante o pareamento, e um QR velho
			// na tela e um scan que nao funciona sem explicacao.
			snap.QR = lv.cliente.QrAtual()
		}
		if lv.lastRefusal != nil {
			snap.LastRefusal = lv.lastRefusal.Error()
		}
	})
	return snap, err
}

// run e a goroutine dona da sessao. Um select, tres origens: o que o
// WhatsApp mandou, o que o servico pediu e o relogio.
func (m *Manager) run(lv *live, eventos <-chan Evento) {
	sweep := time.NewTicker(m.opts.Sweep)
	defer sweep.Stop()
	defer close(lv.done)

	for {
		select {
		case fn := <-lv.cmds:
			fn()
		case ev, ok := <-eventos:
			if !ok {
				// O cliente fechou o canal: desistiu. O laco continua de pe
				// para que /health e DELETE ainda respondam -- uma sessao
				// que some do mapa e uma tela que nao explica nada.
				eventos = nil
				m.transicao(lv, func() error { return lv.state.Fail("cliente encerrou os eventos") })
				continue
			}
			m.apply(lv, ev)
		case <-sweep.C:
			if lv.state.ExpireReconnect(m.opts.Now()) {
				m.avisarEstado(lv)
			}
		case <-lv.quit:
			return
		}
	}
}

// apply traduz evento em chamada da maquina de estados. Traduz e so: qual
// transicao vale em qual estado esta no state.go, e e la que continua.
func (m *Manager) apply(lv *live, ev Evento) {
	switch ev.Kind {
	case EventMessage:
		// O que chega nao mexe no estado da sessao; so sai pelo aviso.
		m.avisar(Notice{SessionID: lv.id, Kind: NoticeMessage, State: lv.state.State(), JID: lv.state.JID(), Message: ev.Message})
		return
	case EventDelivery:
		m.avisar(Notice{SessionID: lv.id, Kind: NoticeStatus, State: lv.state.State(), JID: lv.state.JID(), Delivery: ev.Delivery})
		return
	}

	m.transicao(lv, func() error {
		switch ev.Kind {
		case EventPaired:
			return lv.state.Scanned(ev.JID)
		case EventDropped:
			return lv.state.Dropped(m.opts.Now())
		case EventResumed:
			return lv.state.Reconnected(m.opts.Now(), ev.JID)
		case EventLoggedOut:
			return lv.state.Unpaired()
		case EventFailed:
			return lv.state.Fail(ev.Reason)
		default:
			return fmt.Errorf("session: evento desconhecido %q", ev.Kind)
		}
	})
}

func (m *Manager) transicao(lv *live, chamar func() error) {
	antes := lv.state.State()
	if err := chamar(); err != nil {
		lv.lastRefusal = err
	}
	// O aviso sai pela mudanca de estado, e nao pelo erro ser nil, porque
	// ha uma transicao que recusa e muda o estado ao mesmo tempo: a volta
	// fora do prazo devolve ErrReconnectExpired e deixa a sessao em Failed.
	// Olhar so o erro esconderia dos outros a sessao que acabou de morrer.
	if lv.state.State() != antes {
		m.avisarEstado(lv)
	}
}

func (m *Manager) avisarEstado(lv *live) {
	m.avisar(Notice{
		SessionID: lv.id,
		Kind:      NoticeSession,
		State:     lv.state.State(),
		JID:       lv.state.JID(),
		Reason:    lv.state.FailureReason(),
	})
}

func (m *Manager) avisar(n Notice) {
	if m.opts.Notify != nil {
		m.opts.Notify(n)
	}
}

// ask entrega fn para a goroutine dona e espera terminar. E por aqui que
// toda leitura e escrita de fora passa -- e e por isso que a Session nao
// precisa de mutex nenhum.
func (lv *live) ask(fn func()) error {
	pronto := make(chan struct{})
	select {
	case lv.cmds <- func() { defer close(pronto); fn() }:
	case <-lv.done:
		return ErrSessionClosed
	}
	select {
	case <-pronto:
		return nil
	case <-lv.done:
		return ErrSessionClosed
	}
}

func (lv *live) shutdown() {
	lv.once.Do(func() {
		close(lv.quit)
		// Desconectar so depois que o laco parou: assim o cliente nao esta
		// sendo usado pela goroutine da sessao quando cai. Um envio em voo
		// de outra goroutine ainda pode estar dentro dele, e a interface diz
		// que isso e permitido e faz o envio falhar.
		<-lv.done
		lv.cliente.Desconectar()
	})
}
