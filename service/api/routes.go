// Package api sao as cinco rotas do servico, e o token que as protege.
//
// Duas decisoes moram aqui e valem ser lidas antes do codigo.
//
// A primeira: o token envolve o mux inteiro, e nao cada rota. Proteger rota
// por rota funciona ate o dia em que alguem acrescenta a sexta e esquece a
// linha -- e a rota esquecida e justamente a que o problema encontra. Com o
// envelope, uma rota nova nasce protegida, e o caminho que nao existe
// tambem responde 401: a diferenca entre 401 e 404 diria a quem bate na
// porta quais rotas existem.
//
// A segunda: o servico escuta so em loopback, e isso nao e configuravel.
// Este processo fala por um numero de WhatsApp de verdade; exposto, quem
// chegar nele manda mensagem como voce. Um endereco de escuta fora do
// loopback nao vira aviso no log -- vira recusa de subir, em Listen.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/macro-markets/whatsqr/media"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"

	"github.com/macro-markets/whatsqr/session"
)

// DefaultMaxBody e o teto do corpo de um pedido. Uma mensagem de texto do
// WhatsApp nao chega perto disto; o teto existe para um corpo torto nao
// virar memoria do processo que fala pelo numero.
const DefaultMaxBody int64 = 64 << 10

// ErrNotLoopback e a recusa do endereco de escuta. Sentinela porque quem
// sobe o servico precisa distinguir "endereco exposto" de "porta ocupada":
// o primeiro e um erro de configuracao que ninguem deve tentar contornar.
var ErrNotLoopback = errors.New("api: endereco de escuta fora do loopback")

// Credentials e o minimo que a rota de apagar precisa do store: sumir com a
// credencial daquele chip. Interface, e nao o *session.WhatsmeowStore, para
// que este pacote continue sem saber que existe whatsmeow -- e para que o
// teste possa conferir por qual JID o DELETE mandou apagar.
type Credentials interface {
	Forget(ctx context.Context, jid string) error
}

// Directory e o de-para entre o id de sessao do servico e o JID do
// aparelho. Aqui so se le e se apaga; quem escreve e quem ve o pareamento
// fechar, que e o main.
//
// A rota de apagar precisa dele porque o JID pode nao estar em lugar nenhum
// na memoria: depois de um reinicio, uma sessao que nem chegou a ser
// religada continua com credencial no disco, e DELETE tem que alcanca-la.
type Directory interface {
	JID(sessionID string) (jid string, ok bool, err error)
	Unbind(sessionID string) error
}

// Options sao as pecas que o servidor precisa. So Manager e Token sao
// obrigatorios; sem os outros as rotas continuam respondendo, apenas sem
// apagar credencial nem conferir segredo.
type Options struct {
	Manager *session.Manager

	// Token e o que vai em "Authorization: Bearer". Vazio recusa tudo: um
	// servidor montado sem token nao pode virar um servidor sem
	// autenticacao nenhuma.
	Token string

	Credentials Credentials
	Directory   Directory

	// HasSecret diz se aquele id tem segredo de webhook configurado. Nil
	// aceita qualquer id -- e o padrao do teste; o servico sempre preenche.
	HasSecret func(sessionID string) bool
	// Authenticated additive provisioning; never replaces an existing secret.
	RegisterSession func(sessionID, secret string) error

	// MaxBody e o teto do corpo de um pedido.
	MaxBody int64
	Media   *media.Store

	Logf func(format string, args ...any)
}

// Server serve as cinco rotas.
type Server struct {
	opts    Options
	avatars *avatarCache
}

func NewServer(opts Options) *Server {
	if opts.MaxBody <= 0 {
		opts.MaxBody = DefaultMaxBody
	}
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	return &Server{opts: opts, avatars: newAvatarCache()}
}

// route e uma linha da tabela de rotas. A tabela existe como dado, e nao
// como uma sequencia de mux.Handle, porque e ela que o teste do token
// percorre: uma rota nova entra na tabela e ja nasce coberta, sem ninguem
// lembrar de acrescentar um caso ao teste.
type route struct {
	// pattern e o padrao do net/http, com metodo: "POST /sessions".
	pattern string
	handle  http.HandlerFunc
}

func (s *Server) routes() []route {
	return []route{
		{"POST /sessions/{id}/configuration", s.configureSession},
		{"POST /sessions", s.openSession},
		{"GET /sessions/{id}/qr", s.sessionQR},
		{"GET /sessions/{id}/events", s.sessionEvents},
		{"GET /sessions/{id}/avatar", s.profileImage},
		{"GET /sessions/{id}/media/{mediaID}", s.attachment},
		{"DELETE /sessions/{id}", s.closeSession},
		{"POST /sessions/{id}/messages", s.sendMessage},
		{"GET /health", s.health},
	}
}

func (s *Server) configureSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WebhookSecret string `json:"webhook_secret"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if s.opts.RegisterSession == nil {
		writeError(w, http.StatusServiceUnavailable, false, "mobile session registration is unavailable")
		return
	}
	if err := s.opts.RegisterSession(r.PathValue("id"), body.WebhookSecret); err != nil {
		writeError(w, http.StatusConflict, false, "session configuration could not be registered")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "configured": true})
}

// Handler monta o mux a partir da tabela e o envolve no token.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, r := range s.routes() {
		mux.Handle(r.pattern, r.handle)
	}
	return s.requireToken(mux)
}

// requireToken e o envelope. Compara em tempo constante: um "==" de string
// para no primeiro byte diferente, e a diferenca de tempo entre parar no
// primeiro byte e parar no decimo entrega o token a quem medir as
// respostas.
func (s *Server) requireToken(next http.Handler) http.Handler {
	want := []byte(s.opts.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(want) == 0 {
			s.opts.Logf("api: pedido recusado: o servidor subiu sem token")
			unauthorized(w)
			return
		}
		header := r.Header.Get("Authorization")
		value, ok := strings.CutPrefix(header, "Bearer ")
		if !ok {
			unauthorized(w)
			return
		}
		if subtle.ConstantTimeCompare([]byte(value), want) != 1 {
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func unauthorized(w http.ResponseWriter) {
	// O WWW-Authenticate diz ao cliente qual esquema usar sem dizer nada
	// sobre o token. O corpo nao distingue "sem cabecalho" de "token
	// errado": as duas coisas sao a mesma recusa para quem esta do lado de
	// fora, e separa-las seria um oraculo.
	w.Header().Set("WWW-Authenticate", `Bearer realm="whatsqr"`)
	writeError(w, http.StatusUnauthorized, false, "token ausente ou invalido")
}

// ---------------------------------------------------------------------
// As rotas
// ---------------------------------------------------------------------

type openRequest struct {
	ID string `json:"id"`
}

type openResponse struct {
	ID string `json:"id"`
	// status, e nao state, porque e o nome que o desenho combinou com o
	// lado PHP; o valor e o mesmo da maquina de estados.
	Status string `json:"status"`
	QR     string `json:"qr,omitempty"`
	JID    string `json:"jid,omitempty"`
}

// openSession abre uma sessao e devolve o que a tela de pareamento precisa.
func (s *Server) openSession(w http.ResponseWriter, r *http.Request) {
	var body openRequest
	if !s.decode(w, r, &body) {
		return
	}
	if body.ID == "" {
		writeError(w, http.StatusBadRequest, false, "o corpo precisa do id da sessao")
		return
	}
	// O id vem de fora, e nao e sorteado aqui, porque ele ja existe antes
	// desta chamada: e a chave pela qual a configuracao guarda o segredo
	// daquele numero e pela qual o Mautic reconhece o webhook.
	if s.opts.HasSecret != nil && !s.opts.HasSecret(body.ID) {
		// Sem segredo nada sai pelo webhook. Abrir assim daria uma sessao
		// que pareia, recebe, e perde tudo o que chega em silencio -- o
		// atendente veria a conexao verde e a caixa vazia.
		writeError(w, http.StatusBadRequest, false,
			fmt.Sprintf("a sessao %q nao tem segredo de webhook configurado", body.ID))
		return
	}

	snap, err := s.opts.Manager.Open(r.Context(), body.ID)
	if err != nil {
		switch {
		case errors.Is(err, session.ErrSessionExists):
			writeError(w, http.StatusConflict, false, "essa sessao ja esta aberta")
		case errors.Is(err, session.ErrEmptyID):
			writeError(w, http.StatusBadRequest, false, "o corpo precisa do id da sessao")
		case errors.Is(err, session.ErrSessionClosed):
			writeError(w, http.StatusServiceUnavailable, true, "o servico esta desligando")
		default:
			// Abrir e um gesto de gente: alguem esta na tela de pareamento
			// esperando o QR. Nao ha fila retentando isto, entao a
			// distincao temporaria/permanente nao muda o que acontece a
			// seguir -- o que muda e o motivo aparecer na tela.
			s.opts.Logf("api: abrindo a sessao %s: %v", body.ID, err)
			writeError(w, http.StatusBadGateway, false, err.Error())
		}
		return
	}

	writeJSON(w, http.StatusCreated, openResponse{
		ID:     snap.ID,
		Status: string(snap.State),
		QR:     snap.QR,
		JID:    snap.JID,
	})
}

// sessionQR devolve o codigo de agora. Fora do pareamento nao ha QR, e
// dizer isso e diferente de devolver vazio: a tela distingue "ainda nao
// chegou" de "esta sessao nao esta pareando".
func (s *Server) sessionQR(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	qr, err := s.opts.Manager.QR(id)
	if err != nil {
		switch {
		case errors.Is(err, session.ErrUnknownSession), errors.Is(err, session.ErrSessionClosed):
			writeError(w, http.StatusNotFound, false, "essa sessao nao esta aberta")
		case errors.Is(err, session.ErrNoQR):
			writeError(w, http.StatusConflict, false, "essa sessao nao esta em pareamento")
		default:
			writeError(w, http.StatusBadGateway, false, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, struct {
		ID string `json:"id"`
		QR string `json:"qr"`
	}{ID: id, QR: qr})
}

// closeSession desconecta, apaga a credencial e apaga o de-para.
//
// A ordem importa e nao e arbitraria: primeiro para o socket, depois some
// com a credencial, e so por ultimo esquece qual era o JID. Se apagar a
// credencial falhar, o de-para fica -- sem ele, a retentativa nao saberia
// mais qual credencial ainda esta no disco falando pelo numero.
func (s *Server) closeSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	jid := ""
	if snap, err := s.opts.Manager.Snapshot(id); err == nil {
		jid = snap.JID
	}
	if jid == "" && s.opts.Directory != nil {
		// A sessao pode nem ter sido religada depois de um reinicio, e
		// mesmo assim a credencial dela continua no disco.
		if bound, ok, err := s.opts.Directory.JID(id); err != nil {
			s.opts.Logf("api: lendo o de-para de %s: %v", id, err)
		} else if ok {
			jid = bound
		}
	}

	closeErr := s.opts.Manager.Close(id)
	if closeErr != nil && !errors.Is(closeErr, session.ErrUnknownSession) {
		writeError(w, http.StatusBadGateway, false, closeErr.Error())
		return
	}
	if closeErr != nil && jid == "" {
		// Nao esta no gerente nem no de-para: nao existe em lugar nenhum.
		writeError(w, http.StatusNotFound, false, "essa sessao nao existe")
		return
	}

	if jid != "" && s.opts.Credentials != nil {
		if err := s.opts.Credentials.Forget(r.Context(), jid); err != nil {
			// Responder 204 aqui seria dizer ao atendente que o numero foi
			// desligado quando a credencial dele continua no disco.
			s.opts.Logf("api: apagando a credencial de %s (%s): %v", id, jid, err)
			writeError(w, http.StatusInternalServerError, true,
				fmt.Sprintf("a sessao foi desconectada, mas a credencial de %s continua no disco: %v", jid, err))
			return
		}
	}
	if s.opts.Directory != nil {
		if err := s.opts.Directory.Unbind(id); err != nil {
			s.opts.Logf("api: apagando o de-para de %s: %v", id, err)
			writeError(w, http.StatusInternalServerError, true, err.Error())
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

type sendRequest struct {
	To   string `json:"to"`
	Text string `json:"text"`
	// RequestID e do chamador: e por ele que o Mautic amarra esta resposta
	// ao job que a pediu. O servico so o devolve e o poe no log.
	//
	// Nao ha dedupe por ele aqui de proposito. Um dedupe em memoria
	// esqueceria tudo no reinicio e prometeria uma garantia que nao
	// sobrevive a um deploy; a fila duravel, com claim atomico e dedupe por
	// chave, ja existe do lado do Mautic, e e la que ela funciona.
	RequestID string `json:"request_id"`
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body sendRequest
	if !s.decode(w, r, &body) {
		return
	}
	if body.To == "" || body.Text == "" {
		writeError(w, http.StatusBadRequest, false, "o corpo precisa de to e text")
		return
	}

	messageID, err := s.opts.Manager.Send(r.Context(), id, body.To, body.Text)
	if err != nil {
		switch {
		case errors.Is(err, session.ErrUnknownSession):
			writeError(w, http.StatusNotFound, false, "essa sessao nao esta aberta")
		case errors.Is(err, session.ErrNotConnected), errors.Is(err, session.ErrSessionClosed):
			// Temporaria, e e esta a distincao que o desenho cobra: a
			// sessao ainda esta tentando voltar, e marcar a mensagem como
			// falha definitiva mostra "nao saiu" ao atendente de algo que
			// sairia dali a dez segundos.
			w.Header().Set("Retry-After", "5")
			writeError(w, http.StatusServiceUnavailable, true, err.Error())
		default:
			// O WhatsApp recusou: destino torto, mensagem recusada. Marcar
			// isto como temporario poria a fila do Mautic a retentar para
			// sempre um envio que nunca vai dar certo.
			s.opts.Logf("api: envio da sessao %s (request_id %q): %v", id, body.RequestID, err)
			writeError(w, http.StatusBadGateway, false, err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, struct {
		MessageID string `json:"message_id"`
		RequestID string `json:"request_id,omitempty"`
	}{MessageID: messageID, RequestID: body.RequestID})
}

type healthSession struct {
	ID string `json:"id"`
	// Status e uma das cinco palavras da maquina de estados, mais
	// session.AmbiguousCredential -- o numero que tem duas credenciais no
	// disco e cuja sessao o servico se recusa a abrir. Essa sexta nao sai
	// de sessao nenhuma, e e por isso que ela existe: sem ela o numero some
	// da lista e a tela nao tem o que dizer sobre ele.
	Status string `json:"status"`
	JID    string `json:"jid,omitempty"`
	// Reason e o motivo por escrito. Em Failed vem da maquina de estados;
	// na credencial duplicada vem da propria recusa da abertura, e e o
	// unico lugar onde os aparelhos que brigam pelo numero aparecem.
	Reason string `json:"reason,omitempty"`
	// LastRefusal e a ultima transicao que a maquina de estados recusou.
	// Aparece aqui porque um evento recusado nao tem a quem devolver erro,
	// e uma sessao presa sem nada que explique por que e uma hora de
	// alguem lendo log.
	LastRefusal string `json:"last_refusal,omitempty"`
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	snaps := s.opts.Manager.Status()
	out := make([]healthSession, 0, len(snaps))
	for _, snap := range snaps {
		// O QR nao sai daqui: ele vive so na rota de pareamento. Um QR numa
		// tela de estado e um QR num log, e quem le o log pareia o numero.
		out = append(out, healthSession{
			ID:          snap.ID,
			Status:      string(snap.State),
			JID:         snap.JID,
			Reason:      snap.Reason,
			LastRefusal: snap.LastRefusal,
		})
	}
	// Ordem estavel: uma tela que troca a ordem das linhas a cada
	// atualizacao nao e legivel.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	writeJSON(w, http.StatusOK, struct {
		Sessions []healthSession `json:"sessions"`
	}{Sessions: out})
}

// ---------------------------------------------------------------------
// Encanamento
// ---------------------------------------------------------------------

// decode le o corpo com teto e recusa campo desconhecido. Campo
// desconhecido e quase sempre um nome digitado errado -- "texto" em vez de
// "text" -- e deixar passar manda uma mensagem vazia em vez de dizer o que
// aconteceu.
func (s *Server) decode(w http.ResponseWriter, r *http.Request, into any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, s.opts.MaxBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, false,
				fmt.Sprintf("corpo maior que %d bytes", s.opts.MaxBody))
			return false
		}
		writeError(w, http.StatusBadRequest, false, "corpo invalido: "+err.Error())
		return false
	}
	return true
}

type errorBody struct {
	Error string `json:"error"`
	// Temporary e o que separa "tente de novo" de "nao adianta". E do lado
	// de la que a distincao vira uma mensagem marcada como falha
	// permanente, ou um job que volta para a fila.
	Temporary bool `json:"temporary"`
}

func writeError(w http.ResponseWriter, status int, temporary bool, message string) {
	writeJSON(w, status, errorBody{Error: message, Temporary: temporary})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	raw, err := json.Marshal(body)
	if err != nil {
		http.Error(w, `{"error":"resposta impossivel de serializar","temporary":false}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(raw)
}

// ---------------------------------------------------------------------
// A escuta
// ---------------------------------------------------------------------

// CheckLoopback recusa qualquer endereco que nao seja de loopback.
//
// Nome nao e aceito de proposito, so IP literal e "localhost": um nome
// resolvido por DNS pode apontar para 127.0.0.1 hoje e para outra coisa
// depois, e nesse dia o servico passaria a escutar de fora sem que a
// configuracao tivesse mudado uma letra.
func CheckLoopback(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("api: endereco de escuta %q: %w", addr, err)
	}
	if port == "" {
		return fmt.Errorf("api: endereco de escuta %q: falta a porta", addr)
	}
	if host == "" {
		// ":8088" e todas as interfaces, que e o jeito mais facil de expor
		// isto sem perceber.
		return fmt.Errorf("%w: %q nao diz o endereco, e endereco em branco e todas as interfaces", ErrNotLoopback, addr)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("%w: %q nao e um IP literal, e o que um nome resolve hoje nao e o que ele resolve amanha", ErrNotLoopback, addr)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("%w: %q alcanca este servico de fora da maquina, e quem o alcanca manda mensagem pelo seu numero", ErrNotLoopback, addr)
	}
	return nil
}

// Listen abre a escuta, e recusa antes de abrir soquete nenhum se o
// endereco nao for de loopback.
func Listen(addr string) (net.Listener, error) {
	if err := CheckLoopback(addr); err != nil {
		return nil, err
	}
	return net.Listen("tcp", addr)
}
