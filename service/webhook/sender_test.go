package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/macro-markets/whatsqr/session"
)

// t0 e a hora de mentira. O remetente nunca chama time.Now por conta
// propria, entao o timestamp que sai no cabecalho e exatamente este.
var t0 = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

const testSecret = "segredo-de-mentira"

// ---------------------------------------------------------------------
// O Mautic de mentira
// ---------------------------------------------------------------------

// captured e um POST que chegou, ja separado nos pedacos que o teste olha.
type captured struct {
	key       string
	timestamp string
	signature string
	body      []byte
}

// recorder e o Mautic de mentira. Tem mutex porque o remetente posta de
// outra goroutine, e um teste que so passa por sorte de escalonamento nao
// testa nada -- e com -race nem passa.
type recorder struct {
	mu sync.Mutex
	// codes sao as respostas planejadas, em ordem; depois que acabam, 200.
	codes []int
	got   []captured
	// release, quando existe, segura cada POST ate o teste soltar. E assim
	// que o teste do buffer cheio consegue encher o buffer.
	release chan struct{}
	// hits avisa que um POST chegou e ja foi registrado. Canal, e nao
	// espera com sleep, para o teste nao ficar refem do relogio de parede.
	hits chan struct{}
}

func newRecorder(codes ...int) *recorder {
	return &recorder{codes: codes, hits: make(chan struct{}, 256)}
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)

	r.mu.Lock()
	r.got = append(r.got, captured{
		key:       req.Header.Get(HeaderKey),
		timestamp: req.Header.Get(HeaderTimestamp),
		signature: req.Header.Get(HeaderSignature),
		body:      body,
	})
	code := http.StatusOK
	if len(r.codes) > 0 {
		code, r.codes = r.codes[0], r.codes[1:]
	}
	release := r.release
	r.mu.Unlock()

	select {
	case r.hits <- struct{}{}:
	default:
	}

	if release != nil {
		<-release
	}
	w.WriteHeader(code)
}

// awaitPosts espera mais n POSTs chegarem -- n a mais, e nao n no total --
// e devolve tudo o que chegou ate agora.
func (r *recorder) awaitPosts(t *testing.T, n int) []captured {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-r.hits:
		case <-time.After(3 * time.Second):
			t.Fatalf("esperava mais %d requisicoes, chegaram %d", n, i)
		}
	}
	return r.all()
}

// refuseMore falha se chegar mais alguma coisa na janela dada. Curta de
// proposito: e o unico lugar do arquivo que espera de verdade, e so existe
// para provar uma ausencia.
func (r *recorder) refuseMore(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case <-r.hits:
		t.Fatalf("chegou requisicao que nao devia ter saido")
	case <-time.After(d):
	}
}

func (r *recorder) all() []captured {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]captured(nil), r.got...)
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got)
}

// ---------------------------------------------------------------------
// Relogio, recuo e log de mentira
// ---------------------------------------------------------------------

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// waiter e o recuo de mentira: anota quanto seria esperado, adianta o
// relogio e volta na hora. E este o motivo de o recuo entrar por parametro
// -- com time.Sleep de verdade, o teste do recuo demoraria o proprio recuo
// e falharia sozinho na maquina de alguem sob carga.
type waiter struct {
	mu    sync.Mutex
	slept []time.Duration
	clock *clock
}

// newWaiter traz o proprio relogio: quem adianta o tempo e justamente o
// recuo, e separar os dois faria o timestamp do POST nao acompanhar a
// espera que acabou de acontecer.
func newWaiter() *waiter {
	return &waiter{clock: &clock{t: t0}}
}

func (w *waiter) wait(ctx context.Context, d time.Duration) bool {
	w.mu.Lock()
	w.slept = append(w.slept, d)
	w.mu.Unlock()
	w.clock.advance(d)
	return ctx.Err() == nil
}

func (w *waiter) all() []time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Duration(nil), w.slept...)
}

type logs struct {
	mu    sync.Mutex
	lines []string
}

func (l *logs) add(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logs) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

func (l *logs) matching(needle string) []string {
	var found []string
	for _, line := range l.all() {
		if strings.Contains(line, needle) {
			found = append(found, line)
		}
	}
	return found
}

// ---------------------------------------------------------------------
// A bancada
// ---------------------------------------------------------------------

type bench struct {
	sender *Sender
	clock  *clock
	logs   *logs
	rec    *recorder
}

func newBench(t *testing.T, rec *recorder, opts Options) *bench {
	t.Helper()
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)

	b := &bench{clock: &clock{t: t0}, logs: &logs{}, rec: rec}
	if opts.URL == "" {
		opts.URL = srv.URL
	}
	if opts.Secret == nil {
		opts.Secret = func(string) (string, bool) { return testSecret, true }
	}
	if opts.Now == nil {
		opts.Now = b.clock.now
	}
	if opts.Logf == nil {
		opts.Logf = b.logs.add
	}
	if opts.HTTP == nil {
		// O cliente do httptest nao tem prazo, entao um POST segurado pelo
		// teste fica segurado ate o teste soltar.
		opts.HTTP = srv.Client()
	}
	if opts.Backoff == 0 {
		// Curto para o teste que nao e sobre o recuo nao pagar por ele.
		opts.Backoff = time.Millisecond
	}
	b.sender = New(opts)
	t.Cleanup(b.sender.Close)
	return b
}

// ---------------------------------------------------------------------
// Avisos de mentira
// ---------------------------------------------------------------------

func messageNotice(sessionID, waID, text string) session.Notice {
	return session.Notice{
		SessionID: sessionID,
		Kind:      session.NoticeMessage,
		State:     session.Connected,
		JID:       "5511999990000@s.whatsapp.net",
		Message: &session.Inbound{
			ID:        waID,
			From:      "5511988887777@s.whatsapp.net",
			Text:      text,
			Timestamp: t0,
		},
	}
}

func statusNotice(sessionID, waID string, st session.DeliveryStatus) session.Notice {
	return session.Notice{
		SessionID: sessionID,
		Kind:      session.NoticeStatus,
		State:     session.Connected,
		JID:       "5511999990000@s.whatsapp.net",
		Delivery: &session.Delivery{
			ID:        waID,
			To:        "5511988887777@s.whatsapp.net",
			Status:    st,
			Timestamp: t0,
		},
	}
}

func sessionNotice(sessionID string, st session.State) session.Notice {
	return session.Notice{
		SessionID: sessionID,
		Kind:      session.NoticeSession,
		State:     st,
		JID:       "5511999990000@s.whatsapp.net",
	}
}

func hexHMAC(secret, data string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}

// decode le o corpo como o Mautic leria.
func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("corpo nao e JSON: %v -- %s", err, body)
	}
	return out
}

func idOf(t *testing.T, c captured) string {
	t.Helper()
	id, _ := decode(t, c.body)["id"].(string)
	if id == "" {
		t.Fatalf("evento sem id: %s", c.body)
	}
	return id
}

// ---------------------------------------------------------------------
// Os testes
// ---------------------------------------------------------------------

func TestItSignsTimestampAndBody(t *testing.T) {
	rec := newRecorder()
	b := newBench(t, rec, Options{})

	b.sender.Notify(messageNotice("conexao-abc", "wa-1", "oi"))
	got := rec.awaitPosts(t, 1)[0]

	if got.key != "conexao-abc" {
		t.Fatalf("X-WhatsQr-Key = %q, queria a conexao", got.key)
	}
	ts, err := strconv.ParseInt(got.timestamp, 10, 64)
	if err != nil {
		t.Fatalf("X-WhatsQr-Timestamp = %q, nao e epoch: %v", got.timestamp, err)
	}
	if ts != t0.Unix() {
		t.Fatalf("timestamp = %d, queria %d -- o relogio tem que ser o injetado", ts, t0.Unix())
	}

	want := "sha256=" + hexHMAC(testSecret, got.timestamp+string(got.body))
	if got.signature != want {
		t.Fatalf("assinatura = %q, queria %q", got.signature, want)
	}

	// As duas conferencias abaixo sao o teste de verdade. Sem elas, uma
	// assinatura so do corpo passaria -- e um POST assinado capturado uma
	// vez valeria para sempre, bastando reenvia-lo em laco.
	if got.signature == "sha256="+hexHMAC(testSecret, string(got.body)) {
		t.Fatal("a assinatura cobre so o corpo: trocar o timestamp nao a invalida")
	}
	other := "sha256=" + hexHMAC(testSecret, strconv.FormatInt(ts+1, 10)+string(got.body))
	if got.signature == other {
		t.Fatal("dois timestamps diferentes dao a mesma assinatura")
	}

	// O prefixo e o formato que o WebhookSignatureVerifier do lado PHP ja
	// espera; sem ele o hash_equals recusa tudo.
	if !strings.HasPrefix(got.signature, "sha256=") {
		t.Fatalf("assinatura sem o prefixo sha256=: %q", got.signature)
	}
	if got.signature != strings.ToLower(got.signature) {
		t.Fatalf("hex em maiuscula: %q -- o hash_hmac do PHP devolve minuscula", got.signature)
	}
}

func TestItRetriesWithBackoffUntilTwoHundred(t *testing.T) {
	rec := newRecorder(http.StatusInternalServerError, http.StatusServiceUnavailable)
	w := newWaiter()
	b := newBench(t, rec, Options{Backoff: 2 * time.Second, Attempts: 5, Wait: w.wait, Now: w.clock.now})

	b.sender.Notify(messageNotice("s1", "wa-1", "oi"))
	got := rec.awaitPosts(t, 3)

	if len(got) != 3 {
		t.Fatalf("saiu %d vezes, queria 3 (duas recusas temporarias e o 200)", len(got))
	}
	if slept := w.all(); len(slept) != 2 || slept[0] != 2*time.Second || slept[1] != 4*time.Second {
		t.Fatalf("recuo = %v, queria [2s 4s] -- dobrando a cada tentativa", slept)
	}

	// O corpo e o mesmo nas tres: e o mesmo evento.
	for i, c := range got {
		if string(c.body) != string(got[0].body) {
			t.Fatalf("tentativa %d mudou o corpo: %s", i+1, c.body)
		}
	}

	// O timestamp, ao contrario, e refeito a cada tentativa. Se fosse
	// carimbado uma vez so, um recuo longo entregaria ao Mautic um
	// timestamp vencido e a ultima tentativa -- justo a que ia dar certo --
	// seria recusada por velha.
	if got[0].timestamp == got[2].timestamp {
		t.Fatalf("o timestamp nao foi refeito na retentativa: %q", got[0].timestamp)
	}
	last, _ := strconv.ParseInt(got[2].timestamp, 10, 64)
	if want := t0.Add(6 * time.Second).Unix(); last != want {
		t.Fatalf("timestamp da ultima = %d, queria %d", last, want)
	}
	// E a assinatura acompanha o timestamp novo.
	if got[2].signature != "sha256="+hexHMAC(testSecret, got[2].timestamp+string(got[2].body)) {
		t.Fatal("a retentativa nao reassinou com o timestamp novo")
	}

	// Deu 200: nao tenta mais.
	rec.refuseMore(t, 50*time.Millisecond)
}

func TestItGivesUpAndDropsTheOldestWhenTheBufferIsFull(t *testing.T) {
	rec := newRecorder()
	rec.release = make(chan struct{})
	b := newBench(t, rec, Options{Buffer: 4})

	// O primeiro sai na frente e fica preso no Mautic de mentira. Esperar
	// por ele antes de mandar o resto e o que torna a conta abaixo exata:
	// dai em diante o buffer esta vazio e todo mundo entra nele.
	b.sender.Notify(messageNotice("s1", "e0", "primeiro"))
	rec.awaitPosts(t, 1)

	for i := 1; i <= 9; i++ {
		b.sender.Notify(messageNotice("s1", fmt.Sprintf("e%d", i), "enfileirado"))
	}

	// Com buffer de 4, e1..e4 entram, e e5..e9 empurram os mais velhos para
	// fora. Sobram os quatro mais novos.
	dropped := b.logs.matching("buffer cheio")
	if len(dropped) != 5 {
		t.Fatalf("descartes registrados = %d, queria 5:\n%s", len(dropped), strings.Join(b.logs.all(), "\n"))
	}
	for i := 1; i <= 5; i++ {
		id := "msg:e" + strconv.Itoa(i)
		if len(b.logs.matching(id)) == 0 {
			t.Fatalf("o descarte de %s nao foi registrado -- descarte em silencio e mensagem perdida sem ninguem saber", id)
		}
	}

	close(rec.release)
	got := rec.awaitPosts(t, 4)

	var arrived []string
	for _, c := range got {
		body := decode(t, c.body)
		msg, _ := body["message"].(map[string]any)
		id, _ := msg["id"].(string)
		arrived = append(arrived, id)
	}
	want := []string{"e0", "e6", "e7", "e8", "e9"}
	if strings.Join(arrived, ",") != strings.Join(want, ",") {
		t.Fatalf("chegaram %v, queria %v -- o descarte e do mais velho, e a ordem nao pode mudar", arrived, want)
	}
}

func TestEveryEventCarriesItsOwnId(t *testing.T) {
	rec := newRecorder()
	b := newBench(t, rec, Options{})

	b.sender.Notify(messageNotice("s1", "wa-1", "oi"))
	b.sender.Notify(statusNotice("s1", "wa-1", session.DeliveryDelivered))
	b.sender.Notify(statusNotice("s1", "wa-1", session.DeliveryRead))
	b.sender.Notify(sessionNotice("s1", session.LoggedOut))
	b.sender.Notify(sessionNotice("s1", session.LoggedOut))
	got := rec.awaitPosts(t, 5)

	seen := map[string]int{}
	for _, c := range got {
		id := idOf(t, c)
		seen[id]++
	}
	if len(seen) != 5 {
		t.Fatalf("5 eventos deram %d ids distintos: %v -- o dedupe do Mautic e por chave, e chave repetida some", len(seen), seen)
	}

	// O status carrega o id da mensagem E o andar da entrega: sem o andar,
	// "entregue" e "lido" da mesma mensagem teriam a mesma chave e o
	// segundo seria engolido pelo dedupe.
	delivered := idOf(t, got[1])
	read := idOf(t, got[2])
	if !strings.Contains(delivered, "wa-1") || !strings.Contains(read, "wa-1") {
		t.Fatalf("o status nao usou o id do WhatsApp: %q e %q", delivered, read)
	}
	if delivered == read {
		t.Fatal("entregue e lido deram a mesma chave")
	}

	// Dois session iguais precisam de chaves diferentes: uma sessao pode
	// cair, voltar e cair de novo, e a segunda queda nao pode sumir.
	if idOf(t, got[3]) == idOf(t, got[4]) {
		t.Fatal("dois avisos de sessao iguais deram a mesma chave")
	}

	// O mesmo message reaparecendo -- o whatsmeow reentrega no sync -- tem
	// que dar a mesma chave, senao a conversa ganha a mensagem duas vezes.
	b.sender.Notify(messageNotice("s1", "wa-1", "oi"))
	again := rec.awaitPosts(t, 1)
	if idOf(t, again[5]) != idOf(t, again[0]) {
		t.Fatalf("a mesma mensagem deu duas chaves: %q e %q", idOf(t, again[0]), idOf(t, again[5]))
	}
}

// TestItGivesUpOnARefusalThatWillNotChange: tentar de novo um 401 para
// sempre e diferente de tentar de novo um 503. O 401 nao muda sozinho, e
// insistir nele gasta o buffer inteiro -- os eventos bons atras dele e que
// pagariam a conta.
func TestItGivesUpOnARefusalThatWillNotChange(t *testing.T) {
	rec := newRecorder(http.StatusUnauthorized)
	w := newWaiter()
	b := newBench(t, rec, Options{Attempts: 5, Wait: w.wait, Now: w.clock.now})

	b.sender.Notify(messageNotice("s1", "wa-1", "oi"))
	rec.awaitPosts(t, 1)
	rec.refuseMore(t, 50*time.Millisecond)

	if n := rec.count(); n != 1 {
		t.Fatalf("saiu %d vezes, queria 1 -- 401 nao melhora esperando", n)
	}
	if slept := w.all(); len(slept) != 0 {
		t.Fatalf("recuou %v antes de desistir de um 401", slept)
	}
	if len(b.logs.matching("401")) == 0 {
		t.Fatalf("desistiu do 401 em silencio:\n%s", strings.Join(b.logs.all(), "\n"))
	}
}

// TestItKeepsTryingWhenTheMauticAsksToSlowDown: 429 e 408 sao 4xx que
// mudam sozinhos, e sao a excecao da regra acima.
func TestItKeepsTryingWhenTheMauticAsksToSlowDown(t *testing.T) {
	rec := newRecorder(http.StatusTooManyRequests, http.StatusRequestTimeout)
	w := newWaiter()
	b := newBench(t, rec, Options{Attempts: 5, Backoff: time.Second, Wait: w.wait, Now: w.clock.now})

	b.sender.Notify(messageNotice("s1", "wa-1", "oi"))
	got := rec.awaitPosts(t, 3)
	if len(got) != 3 {
		t.Fatalf("saiu %d vezes, queria 3", len(got))
	}
}

// TestItStopsAfterTheLastAttempt: o Mautic fora do ar nao vira tentativa
// infinita -- o buffer atras teria que esperar por ela.
func TestItStopsAfterTheLastAttempt(t *testing.T) {
	rec := newRecorder(500, 500, 500)
	w := newWaiter()
	b := newBench(t, rec, Options{Attempts: 3, Backoff: time.Second, Wait: w.wait, Now: w.clock.now})

	b.sender.Notify(messageNotice("s1", "wa-1", "oi"))
	rec.awaitPosts(t, 3)
	rec.refuseMore(t, 50*time.Millisecond)

	if n := rec.count(); n != 3 {
		t.Fatalf("saiu %d vezes, queria 3", n)
	}
	if len(b.logs.matching("desistiu")) == 0 {
		t.Fatalf("desistiu em silencio:\n%s", strings.Join(b.logs.all(), "\n"))
	}
}

// TestNotifyNeverBlocksTheSessionGoroutine e a regra que o gerente escreveu
// no Options.Notify: quem escuta e chamado na goroutine da sessao e nao
// pode bloquear. Se bloquear, uma sessao para de ver queda e mensagem
// enquanto o Mautic estiver lento.
func TestNotifyNeverBlocksTheSessionGoroutine(t *testing.T) {
	rec := newRecorder()
	rec.release = make(chan struct{})
	defer close(rec.release)
	b := newBench(t, rec, Options{Buffer: 2})

	b.sender.Notify(messageNotice("s1", "e0", "presa no Mautic"))
	rec.awaitPosts(t, 1)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 1; i <= 200; i++ {
			b.sender.Notify(messageNotice("s1", fmt.Sprintf("e%d", i), "texto"))
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Notify travou com o Mautic segurando o POST")
	}
}

// TestManySessionsNotifyAtOnce existe para o -race: no servico sao varias
// goroutines de sessao chamando o mesmo remetente ao mesmo tempo.
func TestManySessionsNotifyAtOnce(t *testing.T) {
	rec := newRecorder()
	b := newBench(t, rec, Options{Buffer: 512})

	var wg sync.WaitGroup
	for s := 0; s < 8; s++ {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				b.sender.Notify(messageNotice(fmt.Sprintf("s%d", s), fmt.Sprintf("s%d-e%d", s, i), "texto"))
			}
		}(s)
	}
	wg.Wait()
	rec.awaitPosts(t, 160)

	if n := rec.count(); n != 160 {
		t.Fatalf("chegaram %d de 160", n)
	}
}

// TestCloseStopsAWaitInProgress: fechar o servico nao pode esperar o recuo
// terminar. Aqui o recuo e de uma hora e o Close volta na hora.
func TestCloseStopsAWaitInProgress(t *testing.T) {
	rec := newRecorder(500, 500, 500, 500, 500)
	b := newBench(t, rec, Options{Attempts: 5, Backoff: time.Hour})

	b.sender.Notify(messageNotice("s1", "wa-1", "oi"))
	rec.awaitPosts(t, 1)

	done := make(chan struct{})
	go func() {
		defer close(done)
		b.sender.Close()
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close ficou preso no recuo")
	}
}

// TestCloseIsIdempotent porque o servico pode fechar por mais de um
// caminho -- sinal e erro de subida -- e um close de canal duas vezes e
// panico.
func TestCloseIsIdempotent(t *testing.T) {
	b := newBench(t, newRecorder(), Options{})
	b.sender.Close()
	b.sender.Close()
	// Depois de fechado, Notify nao pode entrar em panico nem travar.
	b.sender.Notify(messageNotice("s1", "wa-1", "oi"))
}

// TestASessionWithoutASecretIsDropped: assinar com segredo vazio produz um
// POST que o Mautic recusa de qualquer jeito. Melhor nao mandar, e dizer.
func TestASessionWithoutASecretIsDropped(t *testing.T) {
	rec := newRecorder()
	b := newBench(t, rec, Options{
		Secret: func(id string) (string, bool) { return "", false },
	})

	b.sender.Notify(messageNotice("desconhecida", "wa-1", "oi"))
	rec.refuseMore(t, 100*time.Millisecond)

	if len(b.logs.matching("segredo")) == 0 {
		t.Fatalf("descartou em silencio:\n%s", strings.Join(b.logs.all(), "\n"))
	}
}

// TestTheBodyCarriesWhatTheMauticReads confere o contrato do corpo: o
// Mautic le tipo, sessao e o conteudo do evento.
func TestTheBodyCarriesWhatTheMauticReads(t *testing.T) {
	rec := newRecorder()
	b := newBench(t, rec, Options{})

	b.sender.Notify(messageNotice("s1", "wa-1", "e esse aqui"))
	b.sender.Notify(sessionNotice("s1", session.LoggedOut))
	got := rec.awaitPosts(t, 2)

	msg := decode(t, got[0].body)
	if msg["type"] != string(session.NoticeMessage) {
		t.Fatalf("type = %v, queria %q", msg["type"], session.NoticeMessage)
	}
	if msg["session_id"] != "s1" {
		t.Fatalf("session_id = %v", msg["session_id"])
	}
	inbound, ok := msg["message"].(map[string]any)
	if !ok {
		t.Fatalf("corpo sem message: %s", got[0].body)
	}
	if inbound["text"] != "e esse aqui" || inbound["from"] == "" {
		t.Fatalf("message = %v", inbound)
	}

	ses := decode(t, got[1].body)
	if ses["type"] != string(session.NoticeSession) || ses["state"] != string(session.LoggedOut) {
		t.Fatalf("aviso de sessao = %v", ses)
	}
}

// TestAnEventWithoutItsPayloadIsRefused: um message sem Inbound viraria um
// corpo com message nulo, e a caixa ganharia uma conversa vazia. Melhor
// recusar aqui, onde da para dizer de onde veio.
func TestAnEventWithoutItsPayloadIsRefused(t *testing.T) {
	rec := newRecorder()
	b := newBench(t, rec, Options{})

	b.sender.Notify(session.Notice{SessionID: "s1", Kind: session.NoticeMessage})
	b.sender.Notify(session.Notice{SessionID: "s1", Kind: session.NoticeStatus})
	rec.refuseMore(t, 100*time.Millisecond)

	if len(b.logs.all()) < 2 {
		t.Fatalf("recusou em silencio:\n%s", strings.Join(b.logs.all(), "\n"))
	}
}

// TestItPlugsIntoTheManagerSeam: o encaixe com o gerente e conferido pelo
// compilador. Se a assinatura de Notify sair de sincronia com o
// Options.Notify do gerente, isto para de compilar -- que e onde esse
// desencontro custa menos.
func TestItPlugsIntoTheManagerSeam(t *testing.T) {
	b := newBench(t, newRecorder(), Options{})

	opts := session.Options{Notify: b.sender.Notify}
	if opts.Notify == nil {
		t.Fatal("o gerente ficou sem para quem avisar")
	}
	opts.Notify(messageNotice("s1", "wa-1", "pelo encaixe"))
	b.rec.awaitPosts(t, 1)
}
