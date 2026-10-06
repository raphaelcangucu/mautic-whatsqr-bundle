package session

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// O cliente desta borda continua sem teste -- ele precisa de um WhatsApp de
// verdade. O store, nao: achar credencial e abrir aparelho sao disco, e e
// aqui que a traducao entre as duas formas de escrever o mesmo numero
// (com aparelho, que e como o whatsmeow indexa, e sem, que e como o resto
// do servico fala) tem que ser cobrada.

func newTestStore(t *testing.T) *WhatsmeowStore {
	t.Helper()
	store, err := OpenStore(context.Background(), filepath.Join(t.TempDir(), "whatsqr.db"), nil)
	if err != nil {
		t.Fatalf("abrindo o store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// putDevice grava uma credencial de mentira e devolve o JID COM aparelho,
// que e como o whatsmeow a indexa. As chaves sao as que o NewDevice gera; a
// conta e preenchida com bytes quaisquer porque o insert do whatsmeow le os
// campos dela e nao aceita nil.
func putDevice(t *testing.T, s *WhatsmeowStore, user string, device uint16) string {
	t.Helper()
	d := s.container.NewDevice()
	jid := types.NewJID(user, types.DefaultUserServer)
	jid.Device = device
	d.ID = &jid
	// Os tamanhos sao os que o esquema do whatsmeow cobra num CHECK; o
	// conteudo nao importa, porque nada aqui verifica assinatura.
	d.Account = &waAdv.ADVSignedDeviceIdentity{
		Details:             make([]byte, 8),
		AccountSignature:    make([]byte, 64),
		AccountSignatureKey: make([]byte, 32),
		DeviceSignature:     make([]byte, 64),
	}
	if err := s.container.PutDevice(context.Background(), d); err != nil {
		t.Fatalf("gravando a credencial de %s: %v", jid, err)
	}
	return jid.String()
}

func TestDevicesAreListedWithoutTheDevicePart(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	putDevice(t, s, "5511999990000", 12)
	putDevice(t, s, "5511999991111", 3)

	got, err := s.Devices(ctx)
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	want := map[string]bool{
		"5511999990000@s.whatsapp.net": true,
		"5511999991111@s.whatsapp.net": true,
	}
	if len(got) != len(want) {
		t.Fatalf("Devices devolveu %v", got)
	}
	for _, jid := range got {
		if !want[jid] {
			t.Errorf("Devices devolveu %q, que nao e a forma sem aparelho", jid)
		}
	}
}

func TestTheSameNumberAppearsOnceInTheList(t *testing.T) {
	// A lista e de numeros. Duas credenciais do mesmo numero repetidas nela
	// fariam o servico tentar religar a mesma sessao duas vezes.
	ctx := context.Background()
	s := newTestStore(t)
	putDevice(t, s, "5511999990000", 12)
	putDevice(t, s, "5511999990000", 13)

	got, err := s.Devices(ctx)
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	if len(got) != 1 || got[0] != "5511999990000@s.whatsapp.net" {
		t.Fatalf("Devices devolveu %v", got)
	}
}

func TestOpenTakesTheNumberWithoutTheDevicePart(t *testing.T) {
	// Esta e a forma que os eventos carregam, que o Notice entrega e que o
	// de-para guarda. Se Open nao a aceitasse, quem chama teria que saber
	// traduzir -- e traduzir e conhecer o formato de JID do WhatsApp, que e
	// protocolo e nao amarracao.
	ctx := context.Background()
	s := newTestStore(t)
	withDevice := putDevice(t, s, "5511999990000", 12)

	client, err := s.Open(ctx, "5511999990000@s.whatsapp.net")
	if err != nil {
		t.Fatalf("Open sem aparelho: %v", err)
	}
	if client.JID() != "5511999990000@s.whatsapp.net" {
		t.Errorf("o cliente abriu em %q", client.JID())
	}

	// A forma com aparelho continua valendo: ela e o indice de verdade.
	if _, err := s.Open(ctx, withDevice); err != nil {
		t.Errorf("Open com aparelho: %v", err)
	}
}

func TestOpenRefusesANumberWithTwoCredentials(t *testing.T) {
	// Escolher uma das duas seria abrir a credencial errada em metade das
	// vezes, calado.
	ctx := context.Background()
	s := newTestStore(t)
	putDevice(t, s, "5511999990000", 12)
	putDevice(t, s, "5511999990000", 13)

	_, err := s.Open(ctx, "5511999990000@s.whatsapp.net")
	if !errors.Is(err, ErrAmbiguousJID) {
		t.Fatalf("Open com duas credenciais: %v, queria ErrAmbiguousJID", err)
	}
}

func TestOpenSaysWhenTheCredentialIsNotThere(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if _, err := s.Open(ctx, "5511999990000@s.whatsapp.net"); err == nil {
		t.Fatal("abriu um numero que nao esta no store")
	}
	if _, err := s.Open(ctx, "nao e um jid @@"); err == nil {
		t.Fatal("aceitou um jid torto")
	}

	// JID vazio e aparelho novo, que vai pedir QR.
	client, err := s.Open(ctx, "")
	if err != nil {
		t.Fatalf("Open de aparelho novo: %v", err)
	}
	if client.JID() != "" {
		t.Errorf("aparelho novo ja veio com chip: %q", client.JID())
	}
}

func TestForgetTakesTheNumberAndLeavesNothingBehind(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	putDevice(t, s, "5511999990000", 12)
	// Duas credenciais do mesmo numero: deixar a segunda seria deixar algo
	// que ainda fala pelo numero, que e exatamente o que Forget existe para
	// impedir.
	putDevice(t, s, "5511999990000", 13)
	putDevice(t, s, "5511999991111", 3)

	if err := s.Forget(ctx, "5511999990000@s.whatsapp.net"); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	got, err := s.Devices(ctx)
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	if len(got) != 1 || got[0] != "5511999991111@s.whatsapp.net" {
		t.Fatalf("depois do Forget sobrou %v", got)
	}

	// Apagar o que nao esta la nao e erro: o DELETE precisa poder ser
	// repetido depois de uma falha no meio.
	if err := s.Forget(ctx, "5511999990000@s.whatsapp.net"); err != nil {
		t.Errorf("Forget repetido: %v", err)
	}
}

// The pairing window is short -- whatsmeow hands out six codes, the first good
// for 60 seconds and the rest for 20 each, so the whole window is under three
// minutes. What happens at the end of it decides whether the pairing screen
// tells the truth.
//
// The QR channel does not always announce its own end: on some paths it simply
// closes. The range loop then exits with the last code still stored, so the
// service keeps serving a code that cannot work and keeps reporting `pairing`.
// That is what a number stuck for nineteen hours looked like -- the screen said
// "scan this", the service said pairing, and the code had been dead since three
// minutes in.
func TestAClosedQrChannelStopsOfferingTheDeadCode(t *testing.T) {
	qrChan := make(chan whatsmeow.QRChannelItem, 1)
	c := &whatsmeowClient{outbox: make(chan Event, 4), stopped: make(chan struct{})}

	qrChan <- whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventCode, Code: "codigo-vivo"}
	pronto := make(chan struct{})
	go func() { c.followQR(qrChan); close(pronto) }()

	esperar(t, func() bool { return "codigo-vivo" == c.CurrentQR() }, "o codigo devia ter sido guardado")

	if ev := <-c.outbox; ev.Kind != EventQRChanged {
		t.Fatalf("missing QR renewal: %v", ev.Kind)
	}
	close(qrChan)
	<-pronto

	if qr := c.CurrentQR(); "" != qr {
		t.Fatalf("o codigo morto continuou sendo oferecido: %q", qr)
	}

	select {
	case ev := <-c.outbox:
		if EventFailed != ev.Kind {
			t.Fatalf("esperava EventFailed, veio %v", ev.Kind)
		}
		// A tela distingue "expirou" de "o WhatsApp recusou", e so o primeiro
		// oferece gerar outro codigo. Um motivo generico levaria o atendente a
		// desistir de uma janela que so precisava ser reaberta.
		if !strings.Contains(ev.Reason, "expirou") {
			t.Fatalf("o motivo precisa dizer que expirou, veio %q", ev.Reason)
		}
	default:
		t.Fatal("o fim da janela nao avisou ninguem")
	}
}

// E o contrario: depois do pareamento o canal tambem fecha, e ali nada deu
// errado. Avisar falha aqui apagaria da tela um numero que acabou de conectar.
func TestAChannelThatClosesAfterPairingAnnouncesNoFailure(t *testing.T) {
	qrChan := make(chan whatsmeow.QRChannelItem, 1)
	c := &whatsmeowClient{outbox: make(chan Event, 4), stopped: make(chan struct{})}

	qrChan <- whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelSuccess.Event}
	pronto := make(chan struct{})
	go func() { c.followQR(qrChan); close(pronto) }()
	close(qrChan)
	<-pronto

	select {
	case ev := <-c.outbox:
		t.Fatalf("nao devia anunciar nada depois do pareamento, veio %v (%s)", ev.Kind, ev.Reason)
	default:
	}
}

func esperar(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	limite := time.Now().Add(2 * time.Second)
	for time.Now().Before(limite) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}

// The QR emitter watches the context it was handed. Hand it the HTTP request's
// context and the emitter stops the instant the response is written -- measured
// at zero milliseconds:
//
//	13:05:49.753 Emitting QR code https://wa.me/...
//	13:05:49.753 Context is done, stopping QR emitter
//
// The code in the reply was already dead when the caller received it. Two
// different phone numbers failed to pair against this, and it looked from the
// outside exactly like WhatsApp refusing the number.
func TestTheQrContextOutlivesTheRequestThatOpenedTheSession(t *testing.T) {
	pedido, encerrarPedido := context.WithCancel(context.Background())
	parada := make(chan struct{})

	janela, fechar := qrContext(pedido, parada)
	defer fechar()

	// A resposta do POST foi escrita e o contexto do pedido morreu. A janela de
	// pareamento nao tem nada com isso: quem escaneia e uma pessoa, e ela chega
	// depois.
	encerrarPedido()

	select {
	case <-janela.Done():
		t.Fatal("a janela morreu junto com o pedido HTTP -- o codigo entregue ja nasce morto")
	case <-time.After(50 * time.Millisecond):
	}

	// E ela tem que morrer quando a sessao morre, senao fica uma goroutine e um
	// websocket vivos para uma sessao que ninguem mais ve.
	close(parada)
	select {
	case <-janela.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("a janela sobreviveu ao fim da sessao")
	}
}

func TestTranslatePrivacySenderUsesAlternatePhoneAndDisplayName(t *testing.T) {
	evt := &events.Message{
		Info: types.MessageInfo{MessageSource: types.MessageSource{
			Sender:    types.NewJID("opaque-id", types.HiddenUserServer),
			SenderAlt: types.NewJID("5531999990000", types.DefaultUserServer),
		}, PushName: "Contato de teste"},
		Message: &waE2E.Message{Conversation: proto.String("olá")},
	}
	got := translateMessage(evt)
	if got == nil || got.From != "5531999990000@s.whatsapp.net" || got.Name != "Contato de teste" {
		t.Fatalf("alternate phone/name lost: %+v", got)
	}
}
