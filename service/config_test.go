package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/macro-markets/whatsqr/session"
)

// testJID e o numero de mentira destes testes.
const testJID = "5511999990000@s.whatsapp.net"

const goodConfig = `{
  "listen": "127.0.0.1:8088",
  "token": "token-do-servico-longo-o-bastante",
  "webhook_url": "https://mautic.exemplo/api/whatsqr/webhook",
  "store_path": "whatsqr.db",
  "sessions": {
    "numero-1": {"webhook_secret": "segredo-do-numero-um-longo"},
    "numero-2": {"webhook_secret": "segredo-do-numero-dois-longo"}
  }
}`

// writeConfig grava o arquivo com a permissao pedida. O chmod vem depois do
// WriteFile porque o umask do processo morde o modo do WriteFile, e um
// teste de permissao que nao controla a permissao nao testa nada.
func writeConfig(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "whatsqr.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("gravando a configuracao: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %v: %v", mode, err)
	}
	return path
}

func TestItRefusesAConfigAnyoneElseCanRead(t *testing.T) {
	// Este arquivo guarda o token do servico e os segredos do webhook: o
	// mesmo poder que o SQLite da sessao, que o desenho ja manda proteger.
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o666, 0o660, 0o700} {
		path := writeConfig(t, goodConfig, mode)
		_, err := LoadConfig(path)
		if !errors.Is(err, ErrConfigPermissions) {
			t.Errorf("permissao %v: erro %v, queria ErrConfigPermissions", mode, err)
		}
	}
	path := writeConfig(t, goodConfig, 0o600)
	if _, err := LoadConfig(path); err != nil {
		t.Fatalf("permissao 0600 foi recusada: %v", err)
	}
}

func TestItRefusesToListenOffLoopback(t *testing.T) {
	body := strings.Replace(goodConfig, `"127.0.0.1:8088"`, `"0.0.0.0:8088"`, 1)
	path := writeConfig(t, body, 0o600)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("0.0.0.0 foi aceito, e este servico fala por um numero de verdade")
	}
	if !strings.Contains(err.Error(), "0.0.0.0") {
		t.Errorf("a recusa nao diz qual endereco: %v", err)
	}
}

func TestItRefusesAFieldNobodyDeclared(t *testing.T) {
	// "webhook_secrets" em vez de "sessions" e um segredo que nao esta la,
	// e sem esta recusa o servico sobe e perde tudo o que chega em
	// silencio.
	body := strings.Replace(goodConfig, `"sessions"`, `"webhook_secrets"`, 1)
	path := writeConfig(t, body, 0o600)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("um campo desconhecido passou")
	}
}

func TestItRefusesAnIncompleteConfig(t *testing.T) {
	cases := map[string]string{
		"sem token":          strings.Replace(goodConfig, `"token-do-servico-longo-o-bastante"`, `""`, 1),
		"token curto":        strings.Replace(goodConfig, `"token-do-servico-longo-o-bastante"`, `"1234"`, 1),
		"sem webhook":        strings.Replace(goodConfig, `"https://mautic.exemplo/api/whatsqr/webhook"`, `""`, 1),
		"webhook sem http":   strings.Replace(goodConfig, `"https://mautic.exemplo/api/whatsqr/webhook"`, `"mautic.exemplo"`, 1),
		"segredo curto":      strings.Replace(goodConfig, `"segredo-do-numero-um-longo"`, `"1234"`, 1),
		"sessao sem segredo": strings.Replace(goodConfig, `"segredo-do-numero-um-longo"`, `""`, 1),
		"sem escuta":         strings.Replace(goodConfig, `"127.0.0.1:8088"`, `""`, 1),
	}
	for name, body := range cases {
		path := writeConfig(t, body, 0o600)
		if _, err := LoadConfig(path); err == nil {
			t.Errorf("%s: passou", name)
		}
	}
}

func TestSecretsAreLookedUpBySession(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, goodConfig, 0o600))
	if err != nil {
		t.Fatalf("carregando: %v", err)
	}
	// O segredo e por numero, e nao global: um vazamento nao pode valer
	// para os cinco de uma vez.
	if secret, ok := cfg.Secret("numero-1"); !ok || secret != "segredo-do-numero-um-longo" {
		t.Errorf("numero-1: %q %v", secret, ok)
	}
	if secret, ok := cfg.Secret("numero-2"); !ok || secret == "segredo-do-numero-um-longo" {
		t.Errorf("numero-2 recebeu o segredo do numero-1: %q", secret)
	}
	if _, ok := cfg.Secret("numero-3"); ok {
		t.Error("uma sessao sem configuracao ganhou segredo")
	}
	if !cfg.HasSecret("numero-1") || cfg.HasSecret("numero-3") {
		t.Error("HasSecret nao concorda com Secret")
	}
}

// ---------------------------------------------------------------------
// O de-para id <-> JID
// ---------------------------------------------------------------------

func newDirectory(t *testing.T) (*directory, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "whatsqr.db")
	dir, err := openDirectory(context.Background(), path)
	if err != nil {
		t.Fatalf("abrindo o de-para: %v", err)
	}
	t.Cleanup(func() { dir.Close() })
	return dir, path
}

func TestTheMappingSurvivesARestart(t *testing.T) {
	// E este teste que o E2E 6 cobra: reiniciar o servico e a sessao voltar
	// connected sem pedir QR de novo. O id de sessao existe antes do
	// pareamento, quando ainda nao ha JID; se o de-para nao sobreviver, o
	// reinicio nao sabe qual aparelho religar e cada reinicio derruba o
	// atendimento.
	dir, path := newDirectory(t)
	if err := dir.Bind("numero-1", testJID); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := dir.Close(); err != nil {
		t.Fatalf("fechando: %v", err)
	}

	again, err := openDirectory(context.Background(), path)
	if err != nil {
		t.Fatalf("reabrindo: %v", err)
	}
	defer again.Close()

	jid, ok, err := again.JID("numero-1")
	if err != nil || !ok || jid != testJID {
		t.Fatalf("depois do reinicio: %q %v %v", jid, ok, err)
	}
	all, err := again.All()
	if err != nil || len(all) != 1 || all[0].SessionID != "numero-1" || all[0].JID != testJID {
		t.Fatalf("All depois do reinicio: %+v %v", all, err)
	}
}

func TestTheMappingRefusesAnotherChipOnTheSameSession(t *testing.T) {
	// A regra ja existe no state.go, mas o objeto Session morre com o
	// processo. Depois de um reinicio e o de-para que a torna aplicavel.
	dir, _ := newDirectory(t)
	if err := dir.Bind("numero-1", testJID); err != nil {
		t.Fatalf("bind: %v", err)
	}
	// Rebind com o mesmo chip e o caso comum -- toda reconexao avisa de
	// novo -- e tem que ser silencioso.
	if err := dir.Bind("numero-1", testJID); err != nil {
		t.Fatalf("rebind do mesmo chip: %v", err)
	}
	err := dir.Bind("numero-1", "5511999991111@s.whatsapp.net")
	if !errors.Is(err, errChipChanged) {
		t.Fatalf("outro chip na mesma sessao: %v, queria errChipChanged", err)
	}
	jid, _, _ := dir.JID("numero-1")
	if jid != testJID {
		t.Fatalf("o de-para foi trocado assim mesmo: %q", jid)
	}
}

func TestTheMappingRefusesOneChipOnTwoSessions(t *testing.T) {
	// Duas sessoes no mesmo chip fariam duas caixas falando pela mesma
	// linha, e a resposta sairia pela que chegasse primeiro.
	dir, _ := newDirectory(t)
	if err := dir.Bind("numero-1", testJID); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := dir.Bind("numero-2", testJID); err == nil {
		t.Fatal("o mesmo chip foi aceito em duas sessoes")
	}
}

func TestUnbindLetsTheSessionPairAgain(t *testing.T) {
	dir, _ := newDirectory(t)
	if err := dir.Bind("numero-1", testJID); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := dir.Unbind("numero-1"); err != nil {
		t.Fatalf("unbind: %v", err)
	}
	if _, ok, _ := dir.JID("numero-1"); ok {
		t.Fatal("o de-para ficou para tras depois do unbind")
	}
	// Depois do DELETE, o mesmo id pode parear outro chip: apagar foi um
	// gesto explicito de quem opera, e nao uma volta silenciosa.
	if err := dir.Bind("numero-1", "5511999991111@s.whatsapp.net"); err != nil {
		t.Fatalf("parear de novo depois do unbind: %v", err)
	}
	// Unbind de quem nao esta la nao e erro: o DELETE precisa poder ser
	// repetido.
	if err := dir.Unbind("nao-existe"); err != nil {
		t.Fatalf("unbind de sessao inexistente: %v", err)
	}
}

func TestTheMappingRefusesEmptyIdentifiers(t *testing.T) {
	dir, _ := newDirectory(t)
	if err := dir.Bind("", testJID); err == nil {
		t.Error("id vazio foi aceito")
	}
	if err := dir.Bind("numero-1", ""); err == nil {
		t.Error("jid vazio foi aceito, e uma sessao sem chip gravado aceita qualquer chip depois")
	}
}

// ---------------------------------------------------------------------
// A costura do reinicio
// ---------------------------------------------------------------------

// Este teste abre o SQLite de verdade -- inclusive as migracoes do
// whatsmeow -- porque e justamente a convivencia das duas conexoes no mesmo
// arquivo que a decisao de onde por o de-para assume. Nao ha rede nenhuma
// aqui: abrir um aparelho nao fala com o WhatsApp, quem fala e Connect.
func TestDialingLeansOnTheMappingAndRefusesAVanishedCredential(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "whatsqr.db")

	store, err := session.OpenStore(ctx, path, nil)
	if err != nil {
		t.Fatalf("abrindo o store: %v", err)
	}
	defer store.Close()

	dir, err := openDirectory(ctx, path)
	if err != nil {
		t.Fatalf("abrindo o de-para no mesmo arquivo: %v", err)
	}
	defer dir.Close()

	devices := &deviceIndex{store: store}
	dial := dialer(ctx, dir, devices)

	// Sessao que nunca pareou: aparelho novo, que vai pedir QR.
	client, err := dial("numero-novo")
	if err != nil {
		t.Fatalf("sessao nova: %v", err)
	}
	if client.JID() != "" {
		t.Errorf("aparelho novo ja veio com chip: %q", client.JID())
	}

	// Sessao pareada cuja credencial sumiu do disco. Abrir um aparelho novo
	// aqui seria a sessao voltando a pedir QR e aceitando qualquer chip --
	// o buraco que a regra do JID existe para tapar, reaberto pelo caminho
	// de tras.
	if err := dir.Bind("numero-1", testJID); err != nil {
		t.Fatalf("bind: %v", err)
	}
	_, err = dial("numero-1")
	if err == nil {
		t.Fatal("uma sessao sem credencial no disco foi aberta como se fosse nova")
	}
	if !strings.Contains(err.Error(), "DELETE /sessions/numero-1") {
		t.Errorf("a recusa nao diz como sair dela: %v", err)
	}

	// Apagar o que nao esta la nao e erro: o DELETE precisa poder ser
	// repetido depois de uma falha no meio.
	if err := devices.Forget(ctx, testJID); err != nil {
		t.Errorf("Forget de credencial inexistente: %v", err)
	}
}
