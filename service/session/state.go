// Package session guarda a maquina de estados de uma sessao de WhatsApp
// pareada por QR Code. Aqui nao ha rede, HTTP, banco nem whatsmeow: so a
// regra de quais transicoes valem e por que uma volta pode ser recusada.
package session

import (
	"errors"
	"fmt"
	"time"
)

// State e o estado de uma sessao.
//
// O tipo e um string nomeado, e nao um inteiro, porque esses mesmos valores
// atravessam a fronteira do processo: vao para a coluna do SQLite e para o
// JSON que o Mautic le. Com string o valor gravado ontem continua legivel
// hoje mesmo que a ordem das constantes mude, e um log ou um dump de tabela
// ja diz o estado sem tabela de traducao. O buraco conhecido do string
// nomeado -- aceitar uma constante solta como State("qualquer coisa") -- nao
// alcanca o chamador, porque o campo da sessao e privado e so as transicoes
// deste arquivo escrevem nele.
type State string

const (
	// Pairing: esperando alguem escanear o QR.
	Pairing State = "pairing"
	// Connected: pareado e funcionando.
	Connected State = "connected"
	// Reconnecting: caiu, tentando voltar sozinho.
	Reconnecting State = "reconnecting"
	// LoggedOut: o WhatsApp desfez o pareamento; precisa escanear de novo.
	LoggedOut State = "logged_out"
	// Failed: nao deu, e nao adianta esperar. Estado terminal.
	Failed State = "failed"
)

// DefaultReconnectWindow e quanto tempo uma sessao pode ficar em
// Reconnecting antes de ser dada como perdida.
const DefaultReconnectWindow = 5 * time.Minute

// Motivos de recusa. Sao sentinelas para o chamador distinguir com
// errors.Is: o servico precisa dizer ao Mautic por que recusou, e cada um
// destes vira uma mensagem diferente na tela do atendente.
var (
	ErrInvalidTransition = errors.New("session: transicao invalida")
	ErrJIDMismatch       = errors.New("session: jid diferente do pareado")
	ErrEmptyJID          = errors.New("session: jid vazio")
	ErrReconnectExpired  = errors.New("session: janela de reconexao vencida")
)

// InvalidTransitionError diz qual evento chegou em qual estado. O par
// origem/evento e o que permite logar a recusa sem adivinhar o contexto.
type InvalidTransitionError struct {
	From  State
	Event string
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("session: evento %s nao vale em %s", e.Event, e.From)
}

func (e *InvalidTransitionError) Unwrap() error { return ErrInvalidTransition }

// JIDMismatchError carrega os dois numeros. O atendente precisa ver qual
// chip a sessao espera e qual apareceu; "recusado" sozinho nao resolve o
// problema de ninguem.
type JIDMismatchError struct {
	Paired string
	Got    string
}

func (e *JIDMismatchError) Error() string {
	return fmt.Sprintf("session: sessao pareou com %s, veio %s", e.Paired, e.Got)
}

func (e *JIDMismatchError) Unwrap() error { return ErrJIDMismatch }

// Session e a maquina de estados de uma sessao.
//
// Nao e segura para uso concorrente: quem usa serializa o acesso (no
// servico, uma goroutine por sessao). Por um lado o mutex nao cabe aqui,
// porque a decisao de como serializar e de quem e dono da sessao; por
// outro, sem lock esta camada continua sendo so a regra.
type Session struct {
	state State
	// jid mora na sessao, e nao vai como parametro de fora a cada
	// transicao, porque a regra e sobre a sessao continuar sendo o mesmo
	// numero a vida toda. Quem chama e justamente a camada que recebe o
	// pareamento novo do whatsmeow -- deixar a memoria do numero com ela
	// seria pedir que o suspeito guardasse a propria ficha.
	jid      string
	window   time.Duration
	deadline time.Time // so vale em Reconnecting
	reason   string    // so vale em Failed
}

// New cria uma sessao em Pairing. Window <= 0 usa DefaultReconnectWindow.
func New(window time.Duration) *Session {
	if window <= 0 {
		window = DefaultReconnectWindow
	}
	return &Session{state: Pairing, window: window}
}

func (s *Session) State() State                 { return s.state }
func (s *Session) JID() string                  { return s.jid }
func (s *Session) FailureReason() string        { return s.reason }
func (s *Session) ReconnectDeadline() time.Time { return s.deadline }

// Scanned registra que alguem escaneou o QR (PairSuccess no whatsmeow).
// No primeiro scan grava o jid; num scan depois de um logout, confere.
func (s *Session) Scanned(jid string) error {
	if s.state != Pairing {
		return s.refuse("Scanned")
	}
	if err := s.checkJID(jid); err != nil {
		return err
	}
	s.jid = jid
	s.state = Connected
	s.deadline = time.Time{}
	return nil
}

// Dropped registra que a conexao caiu e abre a janela de reconexao.
func (s *Session) Dropped(now time.Time) error {
	if s.state != Connected {
		return s.refuse("Dropped")
	}
	s.state = Reconnecting
	s.deadline = now.Add(s.window)
	return nil
}

// Reconnected registra que o socket voltou. So aceita o mesmo chip e so
// dentro da janela aberta por Dropped.
func (s *Session) Reconnected(now time.Time, jid string) error {
	if s.state != Reconnecting {
		return s.refuse("Reconnected")
	}
	// O chip vem antes do prazo de proposito: chegar com outro numero e um
	// problema maior que chegar tarde, e nao pode ser mascarado por ele.
	if err := s.checkJID(jid); err != nil {
		return err
	}
	if !now.Before(s.deadline) {
		// O prazo ja tinha vencido quando o socket voltou. A essa altura o
		// servico ja reportou a sessao como perdida e provavelmente alguem
		// ja foi avisado; aceitar em silencio desmentiria a tela.
		s.failExpired()
		return ErrReconnectExpired
	}
	s.state = Connected
	s.deadline = time.Time{}
	return nil
}

// Unpaired registra que o WhatsApp desfez o pareamento (LoggedOut no
// whatsmeow). O jid e mantido: e ele que vai barrar a volta com outro chip.
func (s *Session) Unpaired() error {
	if s.state != Connected && s.state != Reconnecting {
		return s.refuse("Unpaired")
	}
	s.state = LoggedOut
	s.deadline = time.Time{}
	return nil
}

// RestartPairing devolve a sessao para a tela de QR depois de um logout.
func (s *Session) RestartPairing() error {
	if s.state != LoggedOut {
		return s.refuse("RestartPairing")
	}
	s.state = Pairing
	return nil
}

// Fail encerra a sessao. Terminal: daqui nao se sai, e uma sessao nova e que
// resolve.
func (s *Session) Fail(reason string) error {
	if s.state == Failed {
		return s.refuse("Fail")
	}
	s.state = Failed
	s.reason = reason
	s.deadline = time.Time{}
	return nil
}

// ExpireReconnect cobra o prazo da janela de reconexao e devolve true se a
// sessao acabou de vencer. E consulta, nao comando: chamar em qualquer
// estado e legitimo e nao devolve erro, porque quem vigia e um laco que nao
// sabe (nem precisa saber) em que estado a sessao esta neste instante.
func (s *Session) ExpireReconnect(now time.Time) bool {
	if s.state != Reconnecting || now.Before(s.deadline) {
		return false
	}
	s.failExpired()
	return true
}

func (s *Session) failExpired() {
	s.state = Failed
	s.reason = ErrReconnectExpired.Error()
	s.deadline = time.Time{}
}

func (s *Session) refuse(event string) error {
	return &InvalidTransitionError{From: s.state, Event: event}
}

// checkJID e a regra central: a sessao pertence a um numero so, do
// pareamento ate o fim. Vale tanto na reconexao quanto num scan depois de
// logout -- e o segundo caso que aparece na pratica, quando o numero e
// banido e alguem pareia outro chip na mesma sessao. As respostas que
// estavam na fila sairiam por um numero que o cliente nunca viu. Para um
// chip novo, o certo e uma sessao nova, com a fila dela.
func (s *Session) checkJID(jid string) error {
	if jid == "" {
		// Um jid vazio anularia a regra: a sessao ficaria sem numero
		// gravado e passaria a aceitar qualquer chip depois.
		return ErrEmptyJID
	}
	if s.jid != "" && s.jid != jid {
		return &JIDMismatchError{Paired: s.jid, Got: jid}
	}
	return nil
}
