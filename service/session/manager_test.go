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
type clienteFalso struct {
	nome    string
	eventos chan Evento

	mu           sync.Mutex
	qr           string
	jid          string
	enviadas     []enviada
	erroEnvio    error
	erroConectar error
	conectou     bool
	desconectou  bool
}

type enviada struct {
	para  string
	texto string
}

func novoCliente(nome, qr string) *clienteFalso {
	return &clienteFalso{nome: nome, qr: qr, eventos: make(chan Evento)}
}

// restaurado e a sessao que voltou do disco ja pareada: sem QR e com chip.
func restaurado(nome, jid string) *clienteFalso {
	c := novoCliente(nome, "")
	c.jid = jid
	return c
}

func (c *clienteFalso) Conectar(ctx context.Context) (<-chan Evento, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.erroConectar != nil {
		return nil, c.erroConectar
	}
	c.conectou = true
	return c.eventos, nil
}

func (c *clienteFalso) QrAtual() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.qr
}

func (c *clienteFalso) Jid() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.jid
}

func (c *clienteFalso) EnviarTexto(ctx context.Context, para, texto string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.erroEnvio != nil {
		return "", c.erroEnvio
	}
	c.enviadas = append(c.enviadas, enviada{para: para, texto: texto})
	// O id carrega o nome do cliente: e assim que o teste percebe uma
	// resposta saindo pelo numero errado.
	return fmt.Sprintf("%s-msg-%d", c.nome, len(c.enviadas)), nil
}

func (c *clienteFalso) Desconectar() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.desconectou = true
}

func (c *clienteFalso) mandou() []enviada {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]enviada(nil), c.enviadas...)
}

func (c *clienteFalso) caiu() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.desconectou
}

// emitir entrega um evento e so volta quando a goroutine da sessao o
// recebeu. Como o canal e sem buffer e quem le e um laco unico, qualquer
// consulta feita depois disto ja enxerga o efeito do evento -- nao ha
// espera arbitraria em teste nenhum abaixo.
func (c *clienteFalso) emitir(t *testing.T, ev Evento) {
	t.Helper()
	select {
	case c.eventos <- ev:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s: ninguem leu o evento %s", c.nome, ev.Kind)
	}
}

// relogio de mentira: o gerente e o unico que olha as horas, e nos testes
// quem manda nelas e o teste.
type relogio struct {
	mu sync.Mutex
	t  time.Time
}

func (r *relogio) agora() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.t
}

func (r *relogio) avancar(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.t = r.t.Add(d)
}

// coletor guarda os avisos que o gerente manda para fora.
type coletor struct {
	mu     sync.Mutex
	avisos []Notice
}

func (c *coletor) add(n Notice) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.avisos = append(c.avisos, n)
}

func (c *coletor) todos() []Notice {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Notice(nil), c.avisos...)
}

type bancada struct {
	m        *Manager
	relogio  *relogio
	avisos   *coletor
	clientes map[string]*clienteFalso
}

func montar(t *testing.T, clientes map[string]*clienteFalso) *bancada {
	t.Helper()
	b := &bancada{
		relogio:  &relogio{t: t0},
		avisos:   &coletor{},
		clientes: clientes,
	}
	b.m = NewManager(func(id string) (Cliente, error) {
		c, ok := clientes[id]
		if !ok {
			return nil, fmt.Errorf("nao ha cliente de mentira para %q", id)
		}
		return c, nil
	}, Options{
		Window: testWindow,
		Now:    b.relogio.agora,
		// Cobranca curta para o teste da janela nao esperar; quando ela
		// vence continua sendo decisao do state.go.
		Sweep:  time.Millisecond,
		Notify: b.avisos.add,
	})
	t.Cleanup(b.m.Shutdown)
	return b
}

func (b *bancada) abrir(t *testing.T, id string) Snapshot {
	t.Helper()
	snap, err := b.m.Open(context.Background(), id)
	assertOK(t, err, "Open "+id)
	return snap
}

// conectada abre e pareia, que e o ponto de partida de quase todo teste.
func (b *bancada) conectada(t *testing.T, id, jid string) {
	t.Helper()
	b.abrir(t, id)
	b.clientes[id].emitir(t, Evento{Kind: EventPaired, JID: jid})
	b.exigeEstado(t, id, Connected)
}

func (b *bancada) exigeEstado(t *testing.T, id string, want State) Snapshot {
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
func (b *bancada) esperarEstado(t *testing.T, id string, want State) Snapshot {
	t.Helper()
	limite := time.Now().Add(3 * time.Second)
	for {
		snap, err := b.m.Snapshot(id)
		assertOK(t, err, "Snapshot "+id)
		if snap.State == want {
			return snap
		}
		if time.Now().After(limite) {
			t.Fatalf("sessao %q: estado = %q, esperava chegar a %q", id, snap.State, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestOpeningASessionReturnsAQr(t *testing.T) {
	b := montar(t, map[string]*clienteFalso{"a": novoCliente("a", "qr-da-a")})

	snap := b.abrir(t, "a")

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
	b.clientes["a"].mu.Lock()
	b.clientes["a"].qr = "qr-renovado"
	b.clientes["a"].mu.Unlock()

	qr, err := b.m.QR("a")
	assertOK(t, err, "QR")
	if qr != "qr-renovado" {
		t.Fatalf("qr = %q, esperado %q", qr, "qr-renovado")
	}
}

func TestSendingOnADroppedSessionFails(t *testing.T) {
	b := montar(t, map[string]*clienteFalso{"a": novoCliente("a", "qr-da-a")})
	b.conectada(t, "a", jidPaired)

	// Conectada, envia. Sem isto o teste de baixo passaria com um gerente
	// que nao envia nunca.
	id, err := b.m.Send(context.Background(), "a", "5511777770000", "oi")
	assertOK(t, err, "Send conectada")
	if id != "a-msg-1" {
		t.Fatalf("message id = %q, esperado %q", id, "a-msg-1")
	}

	b.clientes["a"].emitir(t, Evento{Kind: EventDropped})
	b.exigeEstado(t, "a", Reconnecting)

	// A falha tem que ser esta, e nao uma qualquer: e ela que a rota traduz
	// para a falha temporaria, que e o que segura a mensagem na fila em vez
	// de mostrar "nao saiu" ao atendente dois segundos depois.
	_, err = b.m.Send(context.Background(), "a", "5511777770000", "e agora")
	if !errors.Is(err, ErrNotConnected) {
		t.Fatalf("erro = %v, esperava errors.Is(err, ErrNotConnected)", err)
	}
	if n := len(b.clientes["a"].mandou()); n != 1 {
		t.Fatalf("o cliente recebeu %d envios, esperado 1", n)
	}
}

func TestTwoSessionsDoNotShareState(t *testing.T) {
	b := montar(t, map[string]*clienteFalso{
		"a": novoCliente("a", "qr-da-a"),
		"b": novoCliente("b", "qr-da-b"),
	})
	b.conectada(t, "a", jidPaired)
	b.conectada(t, "b", jidOther)

	// A cai. B nao tem nada com isso.
	b.clientes["a"].emitir(t, Evento{Kind: EventDropped})
	b.exigeEstado(t, "a", Reconnecting)
	snapB := b.exigeEstado(t, "b", Connected)

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
	if n := len(b.clientes["a"].mandou()); n != 0 {
		t.Fatalf("a mensagem de b saiu pela sessao a: %v", b.clientes["a"].mandou())
	}
	if enviadas := b.clientes["b"].mandou(); len(enviadas) != 1 || enviadas[0].texto != "so pela b" {
		t.Fatalf("envios de b = %v", enviadas)
	}

	// E o que chega numa nao pode sair com o id da outra.
	b.clientes["a"].emitir(t, Evento{Kind: EventMessage, Message: &Inbound{ID: "e1", From: jidPaired, Text: "chegou na a"}})
	b.exigeEstado(t, "a", Reconnecting)

	var mensagens []Notice
	for _, n := range b.avisos.todos() {
		if n.Kind == NoticeMessage {
			mensagens = append(mensagens, n)
		}
	}
	if len(mensagens) != 1 {
		t.Fatalf("avisos de mensagem = %d, esperado 1", len(mensagens))
	}
	if mensagens[0].SessionID != "a" || mensagens[0].Message.Text != "chegou na a" {
		t.Fatalf("aviso = %+v, esperado da sessao a", mensagens[0])
	}
}

// TestConcurrentTrafficStaysWithItsOwnSession e o teste que so tem sentido
// com -race: cinco numeros no mesmo processo significa que consulta, envio
// e evento chegam ao mesmo tempo, e um estado compartilhado por engano so
// aparece assim.
func TestConcurrentTrafficStaysWithItsOwnSession(t *testing.T) {
	b := montar(t, map[string]*clienteFalso{
		"a": novoCliente("a", "qr-da-a"),
		"b": novoCliente("b", "qr-da-b"),
	})
	b.conectada(t, "a", jidPaired)
	b.conectada(t, "b", jidOther)

	const rodadas = 50
	var wg sync.WaitGroup
	for _, id := range []string{"a", "b"} {
		wg.Add(3)
		go func(id string) {
			defer wg.Done()
			for i := 0; i < rodadas; i++ {
				b.clientes[id].emitir(t, Evento{Kind: EventMessage, Message: &Inbound{ID: fmt.Sprintf("%s-%d", id, i), From: id, Text: id}})
			}
		}(id)
		go func(id string) {
			defer wg.Done()
			for i := 0; i < rodadas; i++ {
				if _, err := b.m.Send(context.Background(), id, "5511777770000", id); err != nil {
					t.Errorf("Send %s: %v", id, err)
					return
				}
			}
		}(id)
		go func(id string) {
			defer wg.Done()
			for i := 0; i < rodadas; i++ {
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
		enviadas := b.clientes[id].mandou()
		if len(enviadas) != rodadas {
			t.Fatalf("sessao %s mandou %d, esperado %d", id, len(enviadas), rodadas)
		}
		for _, e := range enviadas {
			if e.texto != id {
				t.Fatalf("sessao %s mandou texto %q da outra sessao", id, e.texto)
			}
		}
	}

	contagem := map[string]int{}
	for _, n := range b.avisos.todos() {
		if n.Kind != NoticeMessage {
			continue
		}
		if n.Message.Text != n.SessionID {
			t.Fatalf("aviso da sessao %q carregou mensagem de %q", n.SessionID, n.Message.Text)
		}
		contagem[n.SessionID]++
	}
	for _, id := range []string{"a", "b"} {
		if contagem[id] != rodadas {
			t.Fatalf("sessao %s avisou %d mensagens, esperado %d", id, contagem[id], rodadas)
		}
	}
}

func TestAReconnectingSessionExpiresWithoutAnyoneAsking(t *testing.T) {
	b := montar(t, map[string]*clienteFalso{"a": novoCliente("a", "qr-da-a")})
	b.conectada(t, "a", jidPaired)

	b.clientes["a"].emitir(t, Evento{Kind: EventDropped})
	b.exigeEstado(t, "a", Reconnecting)

	// Ninguem vai chamar nada: quem cobra o prazo e o gerente.
	b.relogio.avancar(testWindow + time.Second)
	snap := b.esperarEstado(t, "a", Failed)

	if !strings.Contains(snap.Reason, "reconexao") {
		t.Fatalf("motivo = %q, esperava falar da janela de reconexao", snap.Reason)
	}
	var visto bool
	for _, n := range b.avisos.todos() {
		if n.Kind == NoticeSession && n.State == Failed {
			visto = true
		}
	}
	if !visto {
		t.Fatal("a sessao venceu e ninguem foi avisado")
	}
}

func TestAComingBackAsAnotherChipIsRefusedAndRecorded(t *testing.T) {
	b := montar(t, map[string]*clienteFalso{"a": novoCliente("a", "qr-da-a")})
	b.conectada(t, "a", jidPaired)
	b.clientes["a"].emitir(t, Evento{Kind: EventDropped})
	b.exigeEstado(t, "a", Reconnecting)

	b.clientes["a"].emitir(t, Evento{Kind: EventResumed, JID: jidOther})

	snap := b.exigeEstado(t, "a", Reconnecting)
	if snap.LastRefusal == "" {
		t.Fatal("a recusa foi engolida: LastRefusal vazio")
	}
	if !strings.Contains(snap.LastRefusal, jidOther) {
		t.Fatalf("LastRefusal = %q, esperava citar o chip recusado", snap.LastRefusal)
	}
}

func TestARestoredSessionComesBackConnectedWithoutAQr(t *testing.T) {
	b := montar(t, map[string]*clienteFalso{"a": restaurado("a", jidPaired)})

	snap := b.abrir(t, "a")
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
	b := montar(t, map[string]*clienteFalso{"a": novoCliente("a", "qr-da-a")})
	b.conectada(t, "a", jidPaired)

	// Abrir o mesmo id duas vezes seria duas goroutines mandando pelo mesmo
	// numero.
	if _, err := b.m.Open(context.Background(), "a"); !errors.Is(err, ErrSessionExists) {
		t.Fatalf("erro = %v, esperava errors.Is(err, ErrSessionExists)", err)
	}

	assertOK(t, b.m.Close("a"), "Close")
	if !b.clientes["a"].caiu() {
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
	b := montar(t, map[string]*clienteFalso{
		"a": novoCliente("a", "qr-da-a"),
		"b": novoCliente("b", "qr-da-b"),
	})
	b.conectada(t, "a", jidPaired)
	b.abrir(t, "b")

	estados := map[string]State{}
	for _, snap := range b.m.Status() {
		estados[snap.ID] = snap.State
	}
	if len(estados) != 2 || estados["a"] != Connected || estados["b"] != Pairing {
		t.Fatalf("status = %v, esperado a conectada e b pareando", estados)
	}
}
