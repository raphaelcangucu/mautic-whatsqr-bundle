package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// jidPaired, jidOther, testWindow e t0 vem do state_test.go: mesmo pacote,
// mesmos numeros de mentira nos dois lugares.

// clienteFalso e o WhatsApp de mentira. Tem mutex proprio porque a
// interface diz que o cliente e chamado de varias goroutines ao mesmo tempo
// -- e um teste que so passa por o cliente ser bem comportado nao testaria
// o gerente.
type fakeClient struct {
	name   string
	events chan Event

	mu           sync.Mutex
	qr           string
	jid          string
	sent         []sentMessage
	sendErr      error
	connectErr   error
	connected    bool
	disconnected bool
}

type sentMessage struct {
	to   string
	text string
}

func newFakeClient(name, qr string) *fakeClient {
	return &fakeClient{name: name, qr: qr, events: make(chan Event)}
}

// restaurado e a sessao que voltou do disco ja pareada: sem QR e com chip.
func newRestoredClient(name, jid string) *fakeClient {
	c := newFakeClient(name, "")
	c.jid = jid
	return c
}

func (c *fakeClient) Connect(ctx context.Context) (<-chan Event, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.connectErr != nil {
		return nil, c.connectErr
	}
	c.connected = true
	return c.events, nil
}

func (c *fakeClient) CurrentQR() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.qr
}

func (c *fakeClient) JID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.jid
}

func (c *fakeClient) SendText(ctx context.Context, to, text string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sendErr != nil {
		return "", c.sendErr
	}
	c.sent = append(c.sent, sentMessage{to: to, text: text})
	// O id carrega o nome do cliente: e assim que o teste percebe uma
	// resposta saindo pelo numero errado.
	return fmt.Sprintf("%s-msg-%d", c.name, len(c.sent)), nil
}

func (c *fakeClient) Disconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disconnected = true
}

func (c *fakeClient) allSent() []sentMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]sentMessage(nil), c.sent...)
}

func (c *fakeClient) wasDisconnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.disconnected
}

// emitir entrega um evento e so volta quando a goroutine da sessao o
// recebeu. Como o canal e sem buffer e quem le e um laco unico, qualquer
// consulta feita depois disto ja enxerga o efeito do evento -- nao ha
// espera arbitraria em teste nenhum abaixo.
func (c *fakeClient) emit(t *testing.T, ev Event) {
	t.Helper()
	select {
	case c.events <- ev:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s: ninguem leu o evento %s", c.name, ev.Kind)
	}
}

// relogio de mentira: o gerente e o unico que olha as horas, e nos testes
// quem manda nelas e o teste.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (r *clock) now() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.t
}

func (r *clock) advance(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.t = r.t.Add(d)
}

// coletor guarda os avisos que o gerente manda para fora.
type collector struct {
	mu      sync.Mutex
	notices []Notice
}

func (c *collector) add(n Notice) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notices = append(c.notices, n)
}

func (c *collector) all() []Notice {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Notice(nil), c.notices...)
}

type bench struct {
	m       *Manager
	clock   *clock
	notices *collector
	clients map[string]*fakeClient
}

func newBench(t *testing.T, clients map[string]*fakeClient) *bench {
	t.Helper()
	b := &bench{
		clock:   &clock{t: t0},
		notices: &collector{},
		clients: clients,
	}
	b.m = NewManager(func(id string) (Client, error) {
		c, ok := clients[id]
		if !ok {
			return nil, fmt.Errorf("nao ha cliente de mentira para %q", id)
		}
		return c, nil
	}, Options{
		Window: testWindow,
		Now:    b.clock.now,
		// Cobranca curta para o teste da janela nao esperar; quando ela
		// vence continua sendo decisao do state.go.
		Sweep:  time.Millisecond,
		Notify: b.notices.add,
	})
	t.Cleanup(b.m.Shutdown)
	return b
}

func (b *bench) open(t *testing.T, id string) Snapshot {
	t.Helper()
	snap, err := b.m.Open(context.Background(), id)
	assertOK(t, err, "Open "+id)
	return snap
}

// conectada abre e pareia, que e o ponto de partida de quase todo teste.
func (b *bench) paired(t *testing.T, id, jid string) {
	t.Helper()
	b.open(t, id)
	b.clients[id].emit(t, Event{Kind: EventPaired, JID: jid})
	b.requireState(t, id, Connected)
}

func (b *bench) requireState(t *testing.T, id string, want State) Snapshot {
	t.Helper()
	snap, err := b.m.Snapshot(id)
	assertOK(t, err, "Snapshot "+id)
	if snap.State != want {
		t.Fatalf("sessao %q: estado = %q, esperado %q", id, snap.State, want)
	}
	return snap
}

// esperarEstado e para o unico caminho que nao e sincrono: a cobranca da
// janela de reconexao, que acontece num tique e nao num evento.
func (b *bench) waitForState(t *testing.T, id string, want State) Snapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		snap, err := b.m.Snapshot(id)
		assertOK(t, err, "Snapshot "+id)
		if snap.State == want {
			return snap
		}
		if time.Now().After(deadline) {
			t.Fatalf("sessao %q: estado = %q, esperava chegar a %q", id, snap.State, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestOpeningASessionReturnsAQr(t *testing.T) {
	b := newBench(t, map[string]*fakeClient{"a": newFakeClient("a", "qr-da-a")})

	snap := b.open(t, "a")

	if snap.ID != "a" {
		t.Fatalf("id = %q, esperado %q", snap.ID, "a")
	}
	if snap.State != Pairing {
		t.Fatalf("estado = %q, esperado %q", snap.State, Pairing)
	}
	if snap.QR != "qr-da-a" {
		t.Fatalf("qr = %q, esperado %q", snap.QR, "qr-da-a")
	}

	// A rota do QR renovado le o mesmo cliente, e nao uma copia guardada na
	// abertura: o QR muda sozinho durante o pareamento.
	b.clients["a"].mu.Lock()
	b.clients["a"].qr = "qr-renovado"
	b.clients["a"].mu.Unlock()

	qr, err := b.m.QR("a")
	assertOK(t, err, "QR")
	if qr != "qr-renovado" {
		t.Fatalf("qr = %q, esperado %q", qr, "qr-renovado")
	}
}

func TestSendingOnADroppedSessionFails(t *testing.T) {
	b := newBench(t, map[string]*fakeClient{"a": newFakeClient("a", "qr-da-a")})
	b.paired(t, "a", jidPaired)

	// Conectada, envia. Sem isto o teste de baixo passaria com um gerente
	// que nao envia nunca.
	id, err := b.m.Send(context.Background(), "a", "5511777770000", "oi")
	assertOK(t, err, "Send conectada")
	if id != "a-msg-1" {
		t.Fatalf("message id = %q, esperado %q", id, "a-msg-1")
	}

	b.clients["a"].emit(t, Event{Kind: EventDropped})
	b.requireState(t, "a", Reconnecting)

	// A falha tem que ser esta, e nao uma qualquer: e ela que a rota traduz
	// para a falha temporaria, que e o que segura a mensagem na fila em vez
	// de mostrar "nao saiu" ao atendente dois segundos depois.
	_, err = b.m.Send(context.Background(), "a", "5511777770000", "e agora")
	if !errors.Is(err, ErrNotConnected) {
		t.Fatalf("erro = %v, esperava errors.Is(err, ErrNotConnected)", err)
	}
	if n := len(b.clients["a"].allSent()); n != 1 {
		t.Fatalf("o cliente recebeu %d envios, esperado 1", n)
	}
}

func TestTwoSessionsDoNotShareState(t *testing.T) {
	b := newBench(t, map[string]*fakeClient{
		"a": newFakeClient("a", "qr-da-a"),
		"b": newFakeClient("b", "qr-da-b"),
	})
	b.paired(t, "a", jidPaired)
	b.paired(t, "b", jidOther)

	// A cai. B nao tem nada com isso.
	b.clients["a"].emit(t, Event{Kind: EventDropped})
	b.requireState(t, "a", Reconnecting)
	snapB := b.requireState(t, "b", Connected)

	if snapB.JID != jidOther {
		t.Fatalf("jid de b = %q, esperado %q", snapB.JID, jidOther)
	}
	if snapA, _ := b.m.Snapshot("a"); snapA.JID != jidPaired {
		t.Fatalf("jid de a = %q, esperado %q", snapA.JID, jidPaired)
	}

	// O pior caso: a resposta de um cliente saindo pelo numero do outro.
	msg, err := b.m.Send(context.Background(), "b", "5511777770000", "so pela b")
	assertOK(t, err, "Send b")
	if !strings.HasPrefix(msg, "b-") {
		t.Fatalf("message id = %q, esperava vir da sessao b", msg)
	}
	if n := len(b.clients["a"].allSent()); n != 0 {
		t.Fatalf("a mensagem de b saiu pela sessao a: %v", b.clients["a"].allSent())
	}
	if sent := b.clients["b"].allSent(); len(sent) != 1 || sent[0].text != "so pela b" {
		t.Fatalf("envios de b = %v", sent)
	}

	// E o que chega numa nao pode sair com o id da outra.
	b.clients["a"].emit(t, Event{Kind: EventMessage, Message: &Inbound{ID: "e1", From: jidPaired, Text: "chegou na a"}})
	b.requireState(t, "a", Reconnecting)

	var messages []Notice
	for _, n := range b.notices.all() {
		if n.Kind == NoticeMessage {
			messages = append(messages, n)
		}
	}
	if len(messages) != 1 {
		t.Fatalf("avisos de mensagem = %d, esperado 1", len(messages))
	}
	if messages[0].SessionID != "a" || messages[0].Message.Text != "chegou na a" {
		t.Fatalf("aviso = %+v, esperado da sessao a", messages[0])
	}
}

// TestConcurrentTrafficStaysWithItsOwnSession e o teste que so tem sentido
// com -race: cinco numeros no mesmo processo significa que consulta, envio
// e evento chegam ao mesmo tempo, e um estado compartilhado por engano so
// aparece assim.
func TestConcurrentTrafficStaysWithItsOwnSession(t *testing.T) {
	b := newBench(t, map[string]*fakeClient{
		"a": newFakeClient("a", "qr-da-a"),
		"b": newFakeClient("b", "qr-da-b"),
	})
	b.paired(t, "a", jidPaired)
	b.paired(t, "b", jidOther)

	const rounds = 50
	var wg sync.WaitGroup
	for _, id := range []string{"a", "b"} {
		wg.Add(3)
		go func(id string) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				b.clients[id].emit(t, Event{Kind: EventMessage, Message: &Inbound{ID: fmt.Sprintf("%s-%d", id, i), From: id, Text: id}})
			}
		}(id)
		go func(id string) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				if _, err := b.m.Send(context.Background(), id, "5511777770000", id); err != nil {
					t.Errorf("Send %s: %v", id, err)
					return
				}
			}
		}(id)
		go func(id string) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				b.m.Status()
				if _, err := b.m.Snapshot(id); err != nil {
					t.Errorf("Snapshot %s: %v", id, err)
					return
				}
			}
		}(id)
	}
	wg.Wait()

	for _, id := range []string{"a", "b"} {
		sent := b.clients[id].allSent()
		if len(sent) != rounds {
			t.Fatalf("sessao %s mandou %d, esperado %d", id, len(sent), rounds)
		}
		for _, e := range sent {
			if e.text != id {
				t.Fatalf("sessao %s mandou texto %q da outra sessao", id, e.text)
			}
		}
	}

	counts := map[string]int{}
	for _, n := range b.notices.all() {
		if n.Kind != NoticeMessage {
			continue
		}
		if n.Message.Text != n.SessionID {
			t.Fatalf("aviso da sessao %q carregou mensagem de %q", n.SessionID, n.Message.Text)
		}
		counts[n.SessionID]++
	}
	for _, id := range []string{"a", "b"} {
		if counts[id] != rounds {
			t.Fatalf("sessao %s avisou %d mensagens, esperado %d", id, counts[id], rounds)
		}
	}
}

func TestAReconnectingSessionExpiresWithoutAnyoneAsking(t *testing.T) {
	b := newBench(t, map[string]*fakeClient{"a": newFakeClient("a", "qr-da-a")})
	b.paired(t, "a", jidPaired)

	b.clients["a"].emit(t, Event{Kind: EventDropped})
	b.requireState(t, "a", Reconnecting)

	// Ninguem vai chamar nada: quem cobra o prazo e o gerente.
	b.clock.advance(testWindow + time.Second)
	snap := b.waitForState(t, "a", Failed)

	if !strings.Contains(snap.Reason, "reconexao") {
		t.Fatalf("motivo = %q, esperava falar da janela de reconexao", snap.Reason)
	}
	var seen bool
	for _, n := range b.notices.all() {
		if n.Kind == NoticeSession && n.State == Failed {
			seen = true
		}
	}
	if !seen {
		t.Fatal("a sessao venceu e ninguem foi avisado")
	}
}

func TestAComingBackAsAnotherChipIsRefusedAndRecorded(t *testing.T) {
	b := newBench(t, map[string]*fakeClient{"a": newFakeClient("a", "qr-da-a")})
	b.paired(t, "a", jidPaired)
	b.clients["a"].emit(t, Event{Kind: EventDropped})
	b.requireState(t, "a", Reconnecting)

	b.clients["a"].emit(t, Event{Kind: EventResumed, JID: jidOther})

	snap := b.requireState(t, "a", Reconnecting)
	if snap.LastRefusal == "" {
		t.Fatal("a recusa foi engolida: LastRefusal vazio")
	}
	if !strings.Contains(snap.LastRefusal, jidOther) {
		t.Fatalf("LastRefusal = %q, esperava citar o chip recusado", snap.LastRefusal)
	}
}

func TestARestoredSessionComesBackConnectedWithoutAQr(t *testing.T) {
	b := newBench(t, map[string]*fakeClient{"a": newRestoredClient("a", jidPaired)})

	snap := b.open(t, "a")
	if snap.State != Connected {
		t.Fatalf("estado = %q, esperado %q", snap.State, Connected)
	}
	if snap.JID != jidPaired {
		t.Fatalf("jid = %q, esperado %q", snap.JID, jidPaired)
	}
	if snap.QR != "" {
		t.Fatalf("qr = %q, esperava vazio numa sessao ja pareada", snap.QR)
	}
	if _, err := b.m.QR("a"); !errors.Is(err, ErrNoQR) {
		t.Fatalf("erro = %v, esperava errors.Is(err, ErrNoQR)", err)
	}
}

func TestClosingASessionDropsItsClientAndForgetsIt(t *testing.T) {
	b := newBench(t, map[string]*fakeClient{"a": newFakeClient("a", "qr-da-a")})
	b.paired(t, "a", jidPaired)

	// Abrir o mesmo id duas vezes seria duas goroutines mandando pelo mesmo
	// numero.
	if _, err := b.m.Open(context.Background(), "a"); !errors.Is(err, ErrSessionExists) {
		t.Fatalf("erro = %v, esperava errors.Is(err, ErrSessionExists)", err)
	}

	assertOK(t, b.m.Close("a"), "Close")
	if !b.clients["a"].wasDisconnected() {
		t.Fatal("a sessao fechou e o cliente continuou de pe")
	}
	if _, err := b.m.Snapshot("a"); !errors.Is(err, ErrUnknownSession) {
		t.Fatalf("erro = %v, esperava errors.Is(err, ErrUnknownSession)", err)
	}
	if _, err := b.m.Send(context.Background(), "a", "5511777770000", "oi"); !errors.Is(err, ErrUnknownSession) {
		t.Fatalf("erro = %v, esperava errors.Is(err, ErrUnknownSession)", err)
	}
	if err := b.m.Close("a"); !errors.Is(err, ErrUnknownSession) {
		t.Fatalf("erro = %v, esperava errors.Is(err, ErrUnknownSession)", err)
	}
}

func TestStatusShowsEverySession(t *testing.T) {
	b := newBench(t, map[string]*fakeClient{
		"a": newFakeClient("a", "qr-da-a"),
		"b": newFakeClient("b", "qr-da-b"),
	})
	b.paired(t, "a", jidPaired)
	b.open(t, "b")

	states := map[string]State{}
	for _, snap := range b.m.Status() {
		states[snap.ID] = snap.State
	}
	if len(states) != 2 || states["a"] != Connected || states["b"] != Pairing {
		t.Fatalf("status = %v, esperado a conectada e b pareando", states)
	}
}
