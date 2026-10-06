package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/macro-markets/whatsqr/session"
)

const testToken = "token-de-teste-com-tamanho"

// jidPareado e o numero de mentira destes testes. Um so: o que interessa
// aqui e a rota, e a regra do chip ja tem teste no state.go.
const testJID = "5511999990000@s.whatsapp.net"

// ---------------------------------------------------------------------
// As mentiras
// ---------------------------------------------------------------------

// fakeClient e o WhatsApp de mentira. Tem mutex proprio porque a interface
// session.Client promete ser chamada de varias goroutines ao mesmo tempo, e
// e isso que o -race cobra.
type fakeClient struct {
	events chan session.Event

	mu           sync.Mutex
	qr           string
	jid          string
	messageID    string
	sendErr      error
	disconnected bool
}

func newFakeClient(qr, jid string) *fakeClient {
	return &fakeClient{
		events:    make(chan session.Event),
		qr:        qr,
		jid:       jid,
		messageID: "wamid-de-mentira",
	}
}

func (c *fakeClient) Connect(context.Context) (<-chan session.Event, error) { return c.events, nil }

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

func (c *fakeClient) SendText(_ context.Context, _, _ string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sendErr != nil {
		return "", c.sendErr
	}
	return c.messageID, nil
}

func (c *fakeClient) Disconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disconnected = true
}

// emit entrega um evento e so volta quando a goroutine da sessao o recebeu.
// O canal e sem buffer e quem le e um laco unico, entao a consulta feita
// depois disto ja enxerga o efeito -- nao ha espera arbitraria em teste
// nenhum abaixo.
func (c *fakeClient) emit(t *testing.T, ev session.Event) {
	t.Helper()
	select {
	case c.events <- ev:
	case <-time.After(3 * time.Second):
		t.Fatalf("ninguem leu o evento %s", ev.Kind)
	}
}

// fakeCredentials e o store de credenciais de mentira: guarda por qual JID
// o DELETE mandou apagar.
type fakeCredentials struct {
	mu        sync.Mutex
	forgot    []string
	forgetErr error
}

func (f *fakeCredentials) Forget(_ context.Context, jid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.forgetErr != nil {
		return f.forgetErr
	}
	f.forgot = append(f.forgot, jid)
	return nil
}

func (f *fakeCredentials) failWith(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgetErr = err
}

func (f *fakeCredentials) allForgotten() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.forgot...)
}

// fakeDirectory e o de-para id<->JID de mentira.
type fakeDirectory struct {
	mu    sync.Mutex
	bound map[string]string
}

func newFakeDirectory() *fakeDirectory {
	return &fakeDirectory{bound: map[string]string{}}
}

func (d *fakeDirectory) JID(id string) (string, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	jid, ok := d.bound[id]
	return jid, ok, nil
}

func (d *fakeDirectory) Unbind(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.bound, id)
	return nil
}

func (d *fakeDirectory) bind(id, jid string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.bound[id] = jid
}

func (d *fakeDirectory) has(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.bound[id]
	return ok
}

// ---------------------------------------------------------------------
// O banco de provas
// ---------------------------------------------------------------------

type harness struct {
	t       *testing.T
	server  *Server
	handler http.Handler
	manager *session.Manager
	creds   *fakeCredentials
	dir     *fakeDirectory

	mu      sync.Mutex
	clients map[string]*fakeClient
	// dialErr e a recusa que a fabrica devolve para aquele id. Ha recusa
	// que acontece antes de existir cliente -- a credencial duplicada --, e
	// sem isto nenhum teste de rota consegue chegar nela.
	dialErr map[string]error
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		t:       t,
		creds:   &fakeCredentials{},
		dir:     newFakeDirectory(),
		clients: map[string]*fakeClient{},
		dialErr: map[string]error{},
	}
	h.manager = session.NewManager(h.dial, session.Options{})
	t.Cleanup(h.manager.Shutdown)
	h.server = NewServer(Options{
		Manager:     h.manager,
		Token:       testToken,
		Credentials: h.creds,
		Directory:   h.dir,
		Logf:        func(string, ...any) {},
	})
	h.handler = h.server.Handler()
	return h
}

func (h *harness) dial(id string) (session.Client, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err, refused := h.dialErr[id]; refused {
		return nil, err
	}
	if c, ok := h.clients[id]; ok {
		return c, nil
	}
	c := newFakeClient("qr-de-"+id, "")
	h.clients[id] = c
	return c, nil
}

// client registra o cliente que o dial vai devolver para aquele id.
func (h *harness) client(id string, c *fakeClient) *fakeClient {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[id] = c
	return c
}

// ensureClient registra um cliente so se o teste ainda nao tiver posto o
// dele. Sem isto, um ajudante de abertura apagaria o cliente que o teste
// preparou e o teste passaria a medir outro objeto.
func (h *harness) ensureClient(id string, c *fakeClient) *fakeClient {
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.clients[id]; ok {
		return existing
	}
	h.clients[id] = c
	return c
}

func (h *harness) do(method, path, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// openPaired abre uma sessao que ja volta conectada, como a que veio do
// disco: sem QR e com chip.
func (h *harness) openPaired(id string) *fakeClient {
	h.t.Helper()
	c := h.ensureClient(id, newFakeClient("", testJID))
	if rec := h.do(http.MethodPost, "/sessions", fmt.Sprintf(`{"id":%q}`, id)); rec.Code != http.StatusCreated {
		h.t.Fatalf("abrindo %s: %d %s", id, rec.Code, rec.Body.String())
	}
	return c
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
		t.Fatalf("corpo %q nao e json: %v", rec.Body.String(), err)
	}
}

// probe transforma um padrao da tabela num pedido concreto. Existe para o
// teste do token percorrer a tabela em vez de repetir cinco casos a mao --
// uma rota nova nasce coberta sem ninguem lembrar de acrescentar nada.
func probe(pattern string) (method, path string) {
	method, rest, ok := strings.Cut(pattern, " ")
	if !ok {
		return http.MethodGet, pattern
	}
	parts := strings.Split(rest, "/")
	for i, part := range parts {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			parts[i] = "sonda"
		}
	}
	return method, strings.Join(parts, "/")
}

// ---------------------------------------------------------------------
// Os testes
// ---------------------------------------------------------------------

func TestEveryRouteRequiresTheToken(t *testing.T) {
	h := newHarness(t)

	// A tabela e percorrida, mas primeiro se confere que ela tem as cinco.
	// Sem isto, apagar uma rota da tabela faria o laco cobrir quatro e o
	// teste continuar verde.
	want := map[string]bool{
		"POST /sessions":               true,
		"GET /sessions/{id}/qr":        true,
		"GET /sessions/{id}/events":    true,
		"DELETE /sessions/{id}":        true,
		"POST /sessions/{id}/messages": true,
		"GET /health":                  true,
	}
	routes := h.server.routes()
	for _, r := range routes {
		if !want[r.pattern] {
			t.Errorf("rota %q nao estava na tabela esperada -- se ela e nova, ponha aqui", r.pattern)
		}
		delete(want, r.pattern)
	}
	for pattern := range want {
		t.Errorf("rota %q sumiu da tabela", pattern)
	}
	if t.Failed() {
		// Sem a tabela certa, percorre-la nao prova nada.
		t.FailNow()
	}

	refused := []struct {
		name  string
		value string
	}{
		{"sem cabecalho", ""},
		{"vazio", ""},
		{"sem o esquema", testToken},
		{"esquema errado", "Basic " + testToken},
		{"token errado", "Bearer " + testToken + "x"},
		{"token vazio", "Bearer "},
		// Um prefixo do token certo: pega a comparacao que para no
		// primeiro byte diferente e aceita o que so comeca igual.
		{"prefixo do token", "Bearer " + testToken[:10]},
	}

	for _, r := range routes {
		method, path := probe(r.pattern)
		for _, bad := range refused {
			req := httptest.NewRequest(method, path, strings.NewReader(""))
			if bad.value != "" {
				req.Header.Set("Authorization", bad.value)
			}
			rec := httptest.NewRecorder()
			h.handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s com %s: deu %d, queria 401", method, path, bad.name, rec.Code)
			}
		}

		// Com o token certo a rota faz o que for: 400, 404, 200. O que nao
		// pode e continuar 401, que esconderia uma rota que recusa sempre.
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("%s %s com o token certo continuou 401", method, path)
		}
	}

	// O caminho que nao existe tambem pede token: sem isso, quem bate na
	// porta descobre quais rotas existem pela diferenca entre 401 e 404.
	req := httptest.NewRequest(http.MethodGet, "/nao-existe", nil)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("caminho desconhecido sem token: deu %d, queria 401", rec.Code)
	}
}

func TestItOnlyListensOnLoopback(t *testing.T) {
	refused := []string{
		"0.0.0.0:8088",
		"[::]:8088",
		// Endereco em branco e todas as interfaces, que e o jeito mais
		// facil de expor isto sem perceber.
		":8088",
		"78.46.212.196:8088",
		"192.168.1.10:8088",
		// Nome que precisa de DNS: o que ele resolve hoje nao e o que ele
		// vai resolver amanha.
		"mautic.exemplo:8088",
		"8088",
	}
	for _, addr := range refused {
		err := CheckLoopback(addr)
		if err == nil {
			t.Errorf("%q foi aceito e nao devia", addr)
			continue
		}
		if addr != "8088" && !errors.Is(err, ErrNotLoopback) {
			t.Errorf("%q: erro %v, queria ErrNotLoopback", addr, err)
		}
	}

	for _, addr := range []string{"127.0.0.1:8088", "127.0.0.2:8088", "[::1]:8088", "localhost:8088"} {
		if err := CheckLoopback(addr); err != nil {
			t.Errorf("%q foi recusado: %v", addr, err)
		}
	}

	// Listen recusa antes de abrir soquete nenhum.
	if _, err := Listen("0.0.0.0:0"); !errors.Is(err, ErrNotLoopback) {
		t.Fatalf("Listen em 0.0.0.0: erro %v, queria ErrNotLoopback", err)
	}

	// E a porta que ele de fato abre e de loopback. Porta efemera para o
	// teste nao brigar com nada que ja esteja escutando na maquina.
	ln, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen em 127.0.0.1: %v", err)
	}
	defer ln.Close()
	host, _, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("endereco %q: %v", ln.Addr(), err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Fatalf("escutou em %q, que nao e loopback", host)
	}
}

func TestSendReturnsTheMessageId(t *testing.T) {
	h := newHarness(t)
	c := h.client("numero-1", newFakeClient("", testJID))
	c.messageID = "wamid-ABCDEF"
	h.openPaired("numero-1")

	rec := h.do(http.MethodPost, "/sessions/numero-1/messages",
		`{"to":"5511988887777","text":"bom dia","request_id":"req-42"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("envio: %d %s", rec.Code, rec.Body.String())
	}

	var got struct {
		MessageID string `json:"message_id"`
		RequestID string `json:"request_id"`
	}
	decode(t, rec, &got)
	if got.MessageID != "wamid-ABCDEF" {
		t.Errorf("message_id %q, queria wamid-ABCDEF", got.MessageID)
	}
	// O request_id volta porque e por ele que o Mautic amarra a resposta ao
	// job que a pediu.
	if got.RequestID != "req-42" {
		t.Errorf("request_id %q, queria req-42", got.RequestID)
	}
}

func TestOpeningASessionReturnsTheQr(t *testing.T) {
	h := newHarness(t)
	h.client("numero-1", newFakeClient("qr-inicial", ""))

	rec := h.do(http.MethodPost, "/sessions", `{"id":"numero-1"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("abertura: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		QR     string `json:"qr"`
	}
	decode(t, rec, &got)
	if got.ID != "numero-1" || got.Status != string(session.Pairing) || got.QR != "qr-inicial" {
		t.Fatalf("abertura devolveu %+v", got)
	}

	// Abrir duas vezes o mesmo id nao pode virar duas sessoes no mesmo
	// numero: o segundo cliente ficaria falando pela mesma linha.
	if rec := h.do(http.MethodPost, "/sessions", `{"id":"numero-1"}`); rec.Code != http.StatusConflict {
		t.Fatalf("segunda abertura: %d, queria 409", rec.Code)
	}
}

func TestOpeningASessionWithoutASecretIsRefused(t *testing.T) {
	h := newHarness(t)
	h.server = NewServer(Options{
		Manager:     h.manager,
		Token:       testToken,
		Credentials: h.creds,
		Directory:   h.dir,
		HasSecret:   func(id string) bool { return id == "conhecido" },
		Logf:        func(string, ...any) {},
	})
	h.handler = h.server.Handler()

	// Sem segredo nada sai pelo webhook, e uma sessao cujo que-chega some
	// em silencio e pior que uma sessao que nem abriu.
	rec := h.do(http.MethodPost, "/sessions", `{"id":"desconhecido"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("id sem segredo: %d %s, queria 400", rec.Code, rec.Body.String())
	}
	if rec := h.do(http.MethodPost, "/sessions", `{"id":"conhecido"}`); rec.Code != http.StatusCreated {
		t.Fatalf("id com segredo: %d %s", rec.Code, rec.Body.String())
	}
}

func TestOpeningASessionWithoutAnIdIsRefused(t *testing.T) {
	h := newHarness(t)
	for _, body := range []string{``, `{}`, `{"id":""}`, `nao e json`} {
		if rec := h.do(http.MethodPost, "/sessions", body); rec.Code != http.StatusBadRequest {
			t.Errorf("corpo %q: %d, queria 400", body, rec.Code)
		}
	}
}

func TestTheQrOnlyExistsDuringPairing(t *testing.T) {
	h := newHarness(t)
	c := h.client("numero-1", newFakeClient("qr-inicial", ""))
	if rec := h.do(http.MethodPost, "/sessions", `{"id":"numero-1"}`); rec.Code != http.StatusCreated {
		t.Fatalf("abertura: %d", rec.Code)
	}

	// O QR e lido do cliente a cada pedido, e nao de uma copia guardada na
	// abertura: ele e renovado durante o pareamento, e um QR velho na tela
	// e um scan que nao funciona sem explicacao.
	c.mu.Lock()
	c.qr = "qr-renovado"
	c.mu.Unlock()

	rec := h.do(http.MethodGet, "/sessions/numero-1/qr", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("qr: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		QR string `json:"qr"`
	}
	decode(t, rec, &got)
	if got.QR != "qr-renovado" {
		t.Errorf("qr %q, queria qr-renovado", got.QR)
	}

	// Pareou: nao ha mais QR, e dizer isso e diferente de devolver vazio.
	c.mu.Lock()
	c.qr = ""
	c.mu.Unlock()
	c.emit(t, session.Event{Kind: session.EventPaired, JID: testJID})

	if rec := h.do(http.MethodGet, "/sessions/numero-1/qr", ""); rec.Code != http.StatusConflict {
		t.Fatalf("qr depois do pareamento: %d, queria 409", rec.Code)
	}
}

func TestDeleteForgetsTheCredentialAndTheMapping(t *testing.T) {
	h := newHarness(t)
	h.dir.bind("numero-1", testJID)
	c := h.openPaired("numero-1")

	if rec := h.do(http.MethodDelete, "/sessions/numero-1", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}

	c.mu.Lock()
	disconnected := c.disconnected
	c.mu.Unlock()
	if !disconnected {
		t.Error("o cliente nao foi desconectado")
	}
	// Deixar a credencial no disco e deixar algo que ainda fala pelo
	// numero; deixar o de-para e o proximo reinicio tentando religar uma
	// sessao que o atendente mandou apagar.
	if got := h.creds.allForgotten(); len(got) != 1 || got[0] != testJID {
		t.Errorf("credenciais apagadas: %v, queria [%s]", got, testJID)
	}
	if h.dir.has("numero-1") {
		t.Error("o de-para id<->jid ficou para tras")
	}
	if rec := h.do(http.MethodDelete, "/sessions/numero-1", ""); rec.Code != http.StatusNotFound {
		t.Errorf("delete de novo: %d, queria 404", rec.Code)
	}
}

func TestDeleteKeepsTheMappingWhenTheCredentialSurvives(t *testing.T) {
	h := newHarness(t)
	h.creds.failWith(errors.New("disco cheio"))
	h.dir.bind("numero-1", testJID)
	h.openPaired("numero-1")

	// A credencial nao foi apagada: ela ainda fala pelo numero. Responder
	// 204 aqui seria dizer ao atendente que o numero foi desligado quando
	// ele nao foi.
	rec := h.do(http.MethodDelete, "/sessions/numero-1", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("delete com falha ao apagar: %d, queria 500", rec.Code)
	}
	if !h.dir.has("numero-1") {
		t.Fatal("o de-para sumiu, e sem ele a retentativa nao sabe qual credencial apagar")
	}

	// E a retentativa ainda encontra o que apagar, embora a sessao ja tenha
	// saido do gerente.
	h.creds.failWith(nil)
	if rec := h.do(http.MethodDelete, "/sessions/numero-1", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("retentativa do delete: %d %s", rec.Code, rec.Body.String())
	}
	if got := h.creds.allForgotten(); len(got) != 1 || got[0] != testJID {
		t.Errorf("credenciais apagadas: %v", got)
	}
}

func TestSendOnADroppedSessionIsTemporary(t *testing.T) {
	h := newHarness(t)
	c := h.openPaired("numero-1")
	c.emit(t, session.Event{Kind: session.EventDropped})

	rec := h.do(http.MethodPost, "/sessions/numero-1/messages", `{"to":"5511988887777","text":"oi"}`)
	// Temporaria, e nao permanente: a sessao ainda esta tentando voltar, e
	// marcar a mensagem como falha definitiva mostra "nao saiu" ao
	// atendente de algo que sairia dali a dez segundos.
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("envio em sessao caida: %d %s, queria 503", rec.Code, rec.Body.String())
	}
	var got struct {
		Error     string `json:"error"`
		Temporary bool   `json:"temporary"`
	}
	decode(t, rec, &got)
	if !got.Temporary {
		t.Errorf("a falha nao veio marcada como temporaria: %+v", got)
	}
	if got.Error == "" {
		t.Error("a recusa nao diz o motivo")
	}
}

func TestSendRefusesAnEmptyDestinationOrText(t *testing.T) {
	h := newHarness(t)
	h.openPaired("numero-1")
	for _, body := range []string{`{}`, `{"to":"","text":"oi"}`, `{"to":"5511988887777","text":""}`, `torto`} {
		if rec := h.do(http.MethodPost, "/sessions/numero-1/messages", body); rec.Code != http.StatusBadRequest {
			t.Errorf("corpo %q: %d, queria 400", body, rec.Code)
		}
	}
}

func TestARefusedSendIsPermanent(t *testing.T) {
	h := newHarness(t)
	c := h.client("numero-1", newFakeClient("", testJID))
	c.sendErr = errors.New("destino invalido")
	h.openPaired("numero-1")

	rec := h.do(http.MethodPost, "/sessions/numero-1/messages", `{"to":"torto","text":"oi"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("envio recusado pelo WhatsApp: %d %s, queria 502", rec.Code, rec.Body.String())
	}
	var got struct {
		Temporary bool `json:"temporary"`
	}
	decode(t, rec, &got)
	// Marcar isto como temporario poria a fila do Mautic a retentar para
	// sempre um envio que nunca vai dar certo.
	if got.Temporary {
		t.Error("um destino torto veio marcado como falha temporaria")
	}
}

func TestUnknownSessionIsNotFound(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/sessions/nao-existe/qr", ""},
		{http.MethodDelete, "/sessions/nao-existe", ""},
		{http.MethodPost, "/sessions/nao-existe/messages", `{"to":"5511988887777","text":"oi"}`},
	}
	for _, c := range cases {
		if rec := h.do(c.method, c.path, c.body); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d, queria 404", c.method, c.path, rec.Code)
		}
	}
}

func TestHealthListsEverySession(t *testing.T) {
	h := newHarness(t)
	h.openPaired("numero-1")
	h.client("numero-2", newFakeClient("qr-de-numero-2", ""))
	if rec := h.do(http.MethodPost, "/sessions", `{"id":"numero-2"}`); rec.Code != http.StatusCreated {
		t.Fatalf("abertura da segunda: %d", rec.Code)
	}

	rec := h.do(http.MethodGet, "/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("health: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Sessions []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			JID    string `json:"jid"`
		} `json:"sessions"`
	}
	decode(t, rec, &got)
	if len(got.Sessions) != 2 {
		t.Fatalf("health trouxe %d sessoes: %+v", len(got.Sessions), got.Sessions)
	}
	// Ordenado pelo id: uma tela que troca a ordem das linhas a cada
	// atualizacao nao e legivel.
	if got.Sessions[0].ID != "numero-1" || got.Sessions[1].ID != "numero-2" {
		t.Fatalf("health fora de ordem: %+v", got.Sessions)
	}
	if got.Sessions[0].Status != string(session.Connected) || got.Sessions[0].JID != testJID {
		t.Errorf("numero-1: %+v", got.Sessions[0])
	}
	if got.Sessions[1].Status != string(session.Pairing) {
		t.Errorf("numero-2: %+v", got.Sessions[1])
	}
	// O QR nao sai no /health: ele vive so na rota de pareamento, e um QR
	// numa tela de estado e um QR num log.
	if strings.Contains(rec.Body.String(), "qr-de-numero-2") {
		t.Error("o /health vazou o QR")
	}
}

func TestTwoSessionsDoNotAnswerForEachOther(t *testing.T) {
	h := newHarness(t)
	one := h.client("numero-1", newFakeClient("", testJID))
	one.messageID = "wamid-do-um"
	two := h.client("numero-2", newFakeClient("", "5511999991111@s.whatsapp.net"))
	two.messageID = "wamid-do-dois"
	h.openPaired("numero-1")
	h.openPaired("numero-2")

	rec := h.do(http.MethodPost, "/sessions/numero-2/messages", `{"to":"5511988887777","text":"oi"}`)
	var got struct {
		MessageID string `json:"message_id"`
	}
	decode(t, rec, &got)
	if got.MessageID != "wamid-do-dois" {
		t.Fatalf("a resposta saiu pelo numero errado: %q", got.MessageID)
	}
}

func TestABodyTooBigIsRefused(t *testing.T) {
	h := newHarness(t)
	h.openPaired("numero-1")
	huge := strings.Repeat("a", int(DefaultMaxBody)+1)
	rec := h.do(http.MethodPost, "/sessions/numero-1/messages",
		fmt.Sprintf(`{"to":"5511988887777","text":%q}`, huge))
	if rec.Code != http.StatusRequestEntityTooLarge && rec.Code != http.StatusBadRequest {
		t.Fatalf("corpo grande demais: %d", rec.Code)
	}
}

func TestAServerWithoutATokenRefusesEverything(t *testing.T) {
	// Falha fechada: um servidor montado sem token nao pode virar um
	// servidor sem autenticacao nenhuma.
	h := newHarness(t)
	s := NewServer(Options{Manager: h.manager, Token: "", Logf: func(string, ...any) {}})
	handler := s.Handler()
	for _, r := range s.routes() {
		method, path := probe(r.pattern)
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer ")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s sem token configurado: %d, queria 401", method, path, rec.Code)
		}
	}
}

// O caso ruim que o /health nao contava: duas credenciais para o mesmo
// numero no disco. A sessao nao abre, entao ela nao esta no gerente -- e sem
// esta linha o numero some da tela sem motivo escrito e sem saida.
func TestHealthNamesTheNumberWithTwoCredentials(t *testing.T) {
	h := newHarness(t)
	h.openPaired("numero-1")
	// O embrulho e o mesmo que o dialer do main poe em volta da recusa do
	// store, inclusive a saida por escrito.
	h.dialErr["numero-2"] = fmt.Errorf(
		"a sessao numero-2 pareou com %s e essa credencial nao esta disponivel (%w: 2 credenciais no disco); apague a sessao (DELETE /sessions/numero-2) antes de parear outro chip",
		testJID, session.ErrAmbiguousJID)
	h.dir.bind("numero-2", testJID)

	if rec := h.do(http.MethodPost, "/sessions", `{"id":"numero-2"}`); rec.Code != http.StatusBadGateway {
		t.Fatalf("abertura recusada devolveu %d, esperava 502", rec.Code)
	}

	rec := h.do(http.MethodGet, "/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("health: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Sessions []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"sessions"`
	}
	decode(t, rec, &got)
	if len(got.Sessions) != 2 {
		t.Fatalf("health trouxe %d sessoes, esperava a de pe e a recusada: %+v", len(got.Sessions), got.Sessions)
	}
	if got.Sessions[0].ID != "numero-1" || got.Sessions[1].ID != "numero-2" {
		t.Fatalf("health fora de ordem: %+v", got.Sessions)
	}
	refused := got.Sessions[1]
	if refused.Status != string(session.AmbiguousCredential) {
		t.Fatalf("numero-2: status = %q, esperado %q", refused.Status, session.AmbiguousCredential)
	}
	if !strings.Contains(refused.Reason, testJID) {
		t.Errorf("numero-2: motivo = %q, esperava o chip la dentro", refused.Reason)
	}
}

// A saida que a tela escreve tem que existir de verdade: apagar a sessao
// recusada apaga as credenciais e limpa a linha do /health.
func TestDeletingTheRefusedSessionClearsTheDiskAndTheScreen(t *testing.T) {
	h := newHarness(t)
	h.dialErr["numero-2"] = fmt.Errorf("duas credenciais: %w", session.ErrAmbiguousJID)
	h.dir.bind("numero-2", testJID)
	if rec := h.do(http.MethodPost, "/sessions", `{"id":"numero-2"}`); rec.Code != http.StatusBadGateway {
		t.Fatalf("abertura recusada devolveu %d, esperava 502", rec.Code)
	}

	// 404 aqui seria a unica saida escrita respondendo que nao ha o que
	// apagar, enquanto as duas credenciais continuam no disco.
	if rec := h.do(http.MethodDelete, "/sessions/numero-2", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE devolveu %d %s, esperava 204", rec.Code, rec.Body.String())
	}
	if forgot := h.creds.allForgotten(); len(forgot) != 1 || forgot[0] != testJID {
		t.Errorf("credenciais apagadas = %v, esperava %q", forgot, testJID)
	}
	if h.dir.has("numero-2") {
		t.Error("o de-para sobreviveu ao DELETE")
	}

	rec := h.do(http.MethodGet, "/health", "")
	var got struct {
		Sessions []healthSession `json:"sessions"`
	}
	decode(t, rec, &got)
	if len(got.Sessions) != 0 {
		t.Fatalf("health = %+v, esperava vazio depois do DELETE", got.Sessions)
	}
}
