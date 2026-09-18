package session

import (
	"errors"
	"testing"
	"time"
)

// Dois chips diferentes: jidPaired e o numero com que a sessao pareou,
// jidOther e o chip que alguem tenta encaixar no lugar dele.
const (
	jidPaired = "5511999990000:12@s.whatsapp.net"
	jidOther  = "5511888880000:7@s.whatsapp.net"
)

const testWindow = 2 * time.Minute

// Relogio fixo: nenhuma regra desta camada chama time.Now, entao os testes
// escolhem o instante de cada evento.
var t0 = time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)

func assertState(t *testing.T, s *Session, want State) {
	t.Helper()
	if got := s.State(); got != want {
		t.Fatalf("estado = %q, esperado %q", got, want)
	}
}

func assertOK(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: erro inesperado: %v", what, err)
	}
}

// assertRefused cobre as duas metades de uma recusa: o motivo tem que chegar
// ao chamador e o estado nao pode ter se mexido.
func assertRefused(t *testing.T, s *Session, err error, want error, unchanged State, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: esperava recusa, veio nil", what)
	}
	if !errors.Is(err, want) {
		t.Fatalf("%s: erro = %v, esperava errors.Is(err, %v)", what, err, want)
	}
	if got := s.State(); got != unchanged {
		t.Fatalf("%s: recusa mexeu no estado: %q, esperado %q", what, got, unchanged)
	}
}

func inPairing(t *testing.T) *Session {
	t.Helper()
	return New(testWindow)
}

func inConnected(t *testing.T) *Session {
	t.Helper()
	s := inPairing(t)
	assertOK(t, s.Scanned(jidPaired), "Scanned")
	return s
}

func inReconnecting(t *testing.T) *Session {
	t.Helper()
	s := inConnected(t)
	assertOK(t, s.Dropped(t0), "Dropped")
	return s
}

func inLoggedOut(t *testing.T) *Session {
	t.Helper()
	s := inConnected(t)
	assertOK(t, s.Unpaired(), "Unpaired")
	return s
}

func inFailed(t *testing.T) *Session {
	t.Helper()
	s := inPairing(t)
	assertOK(t, s.Fail("teste"), "Fail")
	return s
}

// event embrulha cada transicao para os testes que varrem a matriz inteira.
type event struct {
	name  string
	apply func(*Session) error
}

func allEvents() []event {
	return []event{
		{"Scanned", func(s *Session) error { return s.Scanned(jidPaired) }},
		{"Dropped", func(s *Session) error { return s.Dropped(t0) }},
		{"Reconnected", func(s *Session) error { return s.Reconnected(t0.Add(time.Second), jidPaired) }},
		{"Unpaired", func(s *Session) error { return s.Unpaired() }},
		{"RestartPairing", func(s *Session) error { return s.RestartPairing() }},
		{"Fail", func(s *Session) error { return s.Fail("teste") }},
	}
}

// ---------------------------------------------------------------------------
// Caminhos aceitos
// ---------------------------------------------------------------------------

func TestPairingBecomesConnectedOnScan(t *testing.T) {
	s := New(testWindow)
	assertState(t, s, Pairing)
	if s.JID() != "" {
		t.Fatalf("sessao nova ja nasceu com jid %q", s.JID())
	}

	assertOK(t, s.Scanned(jidPaired), "Scanned")

	assertState(t, s, Connected)
	if s.JID() != jidPaired {
		t.Fatalf("jid gravado = %q, esperado %q", s.JID(), jidPaired)
	}
}

func TestConnectedBecomesReconnectingOnDrop(t *testing.T) {
	s := inConnected(t)

	assertOK(t, s.Dropped(t0), "Dropped")

	assertState(t, s, Reconnecting)
	if want := t0.Add(testWindow); !s.ReconnectDeadline().Equal(want) {
		t.Fatalf("prazo = %v, esperado %v", s.ReconnectDeadline(), want)
	}
	if s.JID() != jidPaired {
		t.Fatalf("queda apagou o jid: %q", s.JID())
	}
}

func TestReconnectingBecomesLoggedOutAfterLogout(t *testing.T) {
	s := inReconnecting(t)

	assertOK(t, s.Unpaired(), "Unpaired")

	assertState(t, s, LoggedOut)
	// O jid sobrevive ao logout de proposito: e ele que vai barrar a volta
	// com outro chip.
	if s.JID() != jidPaired {
		t.Fatalf("logout apagou o jid: %q", s.JID())
	}
	if !s.ReconnectDeadline().IsZero() {
		t.Fatalf("prazo de reconexao sobreviveu ao logout: %v", s.ReconnectDeadline())
	}
}

func TestLoggedOutNeedsPairingAgain(t *testing.T) {
	s := inLoggedOut(t)

	// Voltar sozinho nao e opcao: o WhatsApp desfez o pareamento.
	err := s.Reconnected(t0, jidPaired)
	assertRefused(t, s, err, ErrInvalidTransition, LoggedOut, "Reconnected apos logout")

	assertOK(t, s.RestartPairing(), "RestartPairing")
	assertState(t, s, Pairing)

	assertOK(t, s.Scanned(jidPaired), "Scanned")
	assertState(t, s, Connected)
}

func TestReconnectAfterDropRestoresConnected(t *testing.T) {
	s := inReconnecting(t)

	assertOK(t, s.Reconnected(t0.Add(30*time.Second), jidPaired), "Reconnected")

	assertState(t, s, Connected)
	if !s.ReconnectDeadline().IsZero() {
		t.Fatalf("prazo continuou de pe apos reconectar: %v", s.ReconnectDeadline())
	}
}

// ---------------------------------------------------------------------------
// A regra do chip: a sessao pertence a um numero so
// ---------------------------------------------------------------------------

func TestReconnectWithADifferentJidIsRefused(t *testing.T) {
	s := inReconnecting(t)

	err := s.Reconnected(t0.Add(time.Second), jidOther)

	assertRefused(t, s, err, ErrJIDMismatch, Reconnecting, "Reconnected com outro chip")

	var mismatch *JIDMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("erro = %v, esperava *JIDMismatchError para o servico explicar ao Mautic", err)
	}
	if mismatch.Paired != jidPaired || mismatch.Got != jidOther {
		t.Fatalf("erro nao carrega os dois numeros: pareado=%q recebido=%q", mismatch.Paired, mismatch.Got)
	}
	if s.JID() != jidPaired {
		t.Fatalf("recusa mesmo assim trocou o jid: %q", s.JID())
	}
}

// O caso que motiva a regra: numero banido na sexta, outro chip pareado na
// segunda. Sem isso, a fila de respostas sai por um numero que o cliente
// nunca viu.
func TestRepairingWithADifferentJidIsRefused(t *testing.T) {
	s := inLoggedOut(t)
	assertOK(t, s.RestartPairing(), "RestartPairing")

	err := s.Scanned(jidOther)

	assertRefused(t, s, err, ErrJIDMismatch, Pairing, "Scanned com outro chip")
	if s.JID() != jidPaired {
		t.Fatalf("jid virou %q, deveria continuar %q", s.JID(), jidPaired)
	}

	// O chip original continua sendo aceito depois da recusa.
	assertOK(t, s.Scanned(jidPaired), "Scanned com o chip original")
	assertState(t, s, Connected)
}

// Um jid vazio anularia a regra: a sessao pararia com "nenhum numero
// gravado" e passaria a aceitar qualquer chip depois.
func TestScanWithEmptyJidIsRefused(t *testing.T) {
	s := New(testWindow)

	err := s.Scanned("")

	assertRefused(t, s, err, ErrEmptyJID, Pairing, "Scanned com jid vazio")
	if s.JID() != "" {
		t.Fatalf("jid vazio foi gravado como %q", s.JID())
	}
}

func TestReconnectWithEmptyJidIsRefused(t *testing.T) {
	s := inReconnecting(t)

	err := s.Reconnected(t0.Add(time.Second), "")

	assertRefused(t, s, err, ErrEmptyJID, Reconnecting, "Reconnected com jid vazio")
}

// ---------------------------------------------------------------------------
// O tempo: reconnecting nao dura para sempre
// ---------------------------------------------------------------------------

func TestReconnectingExpiresIntoFailed(t *testing.T) {
	s := inReconnecting(t)

	if s.ExpireReconnect(t0.Add(testWindow - time.Second)) {
		t.Fatal("venceu antes da hora")
	}
	assertState(t, s, Reconnecting)

	if !s.ExpireReconnect(t0.Add(testWindow)) {
		t.Fatal("nao venceu no instante do prazo")
	}
	assertState(t, s, Failed)
	if s.FailureReason() == "" {
		t.Fatal("venceu sem registrar motivo")
	}
	if !s.ReconnectDeadline().IsZero() {
		t.Fatalf("prazo continuou de pe apos vencer: %v", s.ReconnectDeadline())
	}
}

func TestReconnectAfterWindowIsRefused(t *testing.T) {
	s := inReconnecting(t)

	// O socket voltou tarde demais. A essa altura o servico ja contou ao
	// Mautic que a sessao caiu, entao aceitar aqui seria desmentir a tela.
	err := s.Reconnected(t0.Add(testWindow+time.Second), jidPaired)

	if !errors.Is(err, ErrReconnectExpired) {
		t.Fatalf("erro = %v, esperava ErrReconnectExpired", err)
	}
	assertState(t, s, Failed)
}

func TestExpireReconnectIgnoresOtherStates(t *testing.T) {
	late := t0.Add(24 * time.Hour)
	cases := []struct {
		name  string
		build func(*testing.T) *Session
		state State
	}{
		{"pairing", inPairing, Pairing},
		{"connected", inConnected, Connected},
		{"logged_out", inLoggedOut, LoggedOut},
		{"failed", inFailed, Failed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := c.build(t)
			if s.ExpireReconnect(late) {
				t.Fatalf("venceu em %q, onde nao ha prazo correndo", c.state)
			}
			assertState(t, s, c.state)
		})
	}
}

// ---------------------------------------------------------------------------
// Transicoes proibidas
// ---------------------------------------------------------------------------

// Um socket que volta sem que ninguem tenha escaneado nada nao pode virar
// uma sessao conectada: seria uma caixa "funcionando" sem numero nenhum.
func TestConnectingWithoutPairingIsRefused(t *testing.T) {
	s := inPairing(t)
	err := s.Reconnected(t0, jidPaired)
	assertRefused(t, s, err, ErrInvalidTransition, Pairing, "Reconnected antes do primeiro scan")

	// Pelo mesmo motivo a queda tambem nao vale: pairing -> reconnecting
	// abriria justamente o caminho de reconnecting -> connected.
	s2 := inPairing(t)
	err = s2.Dropped(t0)
	assertRefused(t, s2, err, ErrInvalidTransition, Pairing, "Dropped antes do primeiro scan")
}

// O scan e o evento de pareamento, nao um jeito de reentrar numa sessao viva:
// aceita-lo em reconnecting deixaria a tela de QR sequestrar uma sessao que
// so estava se recuperando de uma queda.
func TestScanIsRefusedOnALiveSession(t *testing.T) {
	for _, c := range []struct {
		name  string
		build func(*testing.T) *Session
		state State
	}{
		{"connected", inConnected, Connected},
		{"reconnecting", inReconnecting, Reconnecting},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := c.build(t)
			err := s.Scanned(jidPaired)
			assertRefused(t, s, err, ErrInvalidTransition, c.state, "Scanned")
		})
	}
}

// Voltar para a tela de QR por conta propria derrubaria um pareamento que
// esta de pe. So o logout do WhatsApp abre esse caminho.
func TestRestartPairingIsRefusedOutsideLoggedOut(t *testing.T) {
	for _, c := range []struct {
		name  string
		build func(*testing.T) *Session
		state State
	}{
		{"pairing", inPairing, Pairing},
		{"connected", inConnected, Connected},
		{"reconnecting", inReconnecting, Reconnecting},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := c.build(t)
			err := s.RestartPairing()
			assertRefused(t, s, err, ErrInvalidTransition, c.state, "RestartPairing")
		})
	}
}

func TestFailedIsTerminal(t *testing.T) {
	for _, ev := range allEvents() {
		t.Run(ev.name, func(t *testing.T) {
			s := inFailed(t)
			err := ev.apply(s)
			assertRefused(t, s, err, ErrInvalidTransition, Failed, ev.name)
		})
	}
}

// Varredura da matriz inteira: tudo que nao esta na lista de permitidos tem
// que ser recusado com o estado intacto. Sem isso, uma transicao nova entra
// sem ninguem perceber.
func TestOnlyListedTransitionsAreAccepted(t *testing.T) {
	allowed := map[State]map[string]bool{
		Pairing:      {"Scanned": true, "Fail": true},
		Connected:    {"Dropped": true, "Unpaired": true, "Fail": true},
		Reconnecting: {"Reconnected": true, "Unpaired": true, "Fail": true},
		LoggedOut:    {"RestartPairing": true, "Fail": true},
		Failed:       {},
	}
	builders := map[State]func(*testing.T) *Session{
		Pairing:      inPairing,
		Connected:    inConnected,
		Reconnecting: inReconnecting,
		LoggedOut:    inLoggedOut,
		Failed:       inFailed,
	}

	for state, okEvents := range allowed {
		for _, ev := range allEvents() {
			if okEvents[ev.name] {
				continue
			}
			t.Run(string(state)+"/"+ev.name, func(t *testing.T) {
				s := builders[state](t)
				err := ev.apply(s)
				assertRefused(t, s, err, ErrInvalidTransition, state, ev.name)

				var invalid *InvalidTransitionError
				if !errors.As(err, &invalid) {
					t.Fatalf("erro = %v, esperava *InvalidTransitionError", err)
				}
				if invalid.From != state {
					t.Fatalf("erro aponta origem %q, esperado %q", invalid.From, state)
				}
			})
		}
	}
}

func TestNewUsesDefaultWindowWhenUnset(t *testing.T) {
	s := New(0)
	assertState(t, s, Pairing)
	assertOK(t, s.Scanned(jidPaired), "Scanned")
	assertOK(t, s.Dropped(t0), "Dropped")

	if want := t0.Add(DefaultReconnectWindow); !s.ReconnectDeadline().Equal(want) {
		t.Fatalf("prazo = %v, esperado %v", s.ReconnectDeadline(), want)
	}
}
