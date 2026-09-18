package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/macro-markets/whatsqr/api"

	// O mesmo driver SQLite em Go puro que o session/whatsmeow.go usa. O
	// import vale por registro no database/sql e roda uma vez so, esteja
	// ele escrito em um pacote ou em dois; esta aqui para que este arquivo
	// nao dependa de outro lembrar de registrar o driver.
	_ "modernc.org/sqlite"
)

// Este arquivo tem duas coisas, e elas moram juntas por um motivo que vale
// escrever: as duas sao o que o servico sabe sobre si mesmo entre um
// reinicio e outro. A configuracao diz por quais numeros ele fala e com que
// segredo; o de-para diz qual credencial do disco e de qual numero. O
// session/whatsmeow.go ja aponta para ca quando diz que o de-para "e da
// camada que guarda a configuracao".

// ErrConfigPermissions e a recusa de subir com a configuracao aberta.
// Sentinela porque nao e um erro para tentar de novo: e um chmod.
var ErrConfigPermissions = errors.New("config: permissao do arquivo de configuracao")

// configMode e a unica permissao aceita. Exatamente 0600, e nao "no maximo
// 0600": este arquivo guarda o token do servico e os segredos do webhook --
// o mesmo poder que o SQLite da sessao, que o desenho ja manda tratar como
// credencial. A primeira versao do desenho cuidava de um e esquecia o
// outro.
const configMode os.FileMode = 0o600

// minSecretLength e o piso do token e dos segredos. Existe porque o token e
// a unica coisa entre outro processo desta maquina e um numero de WhatsApp
// que fala com clientes; um token de quatro letras e uma porta destrancada
// com um aviso pendurado.
const minSecretLength = 16

// SessionConfig e o que a configuracao guarda de cada numero.
type SessionConfig struct {
	// WebhookSecret assina o que sai para o Mautic daquela sessao. E por
	// numero, e nao global: o que ele compra se vazar e fazer um numero
	// enviar para um destinatario escolhido por quem vazou, e um segredo
	// global faria um vazamento valer pelos cinco de uma vez.
	WebhookSecret string `json:"webhook_secret"`
}

// Config e o arquivo de configuracao inteiro.
type Config struct {
	// Listen e o endereco de escuta. Fora do loopback, o servico nao sobe.
	Listen string `json:"listen"`
	// Token e o que vai em "Authorization: Bearer" nas cinco rotas.
	Token string `json:"token"`
	// WebhookURL e a rota do webhook no Mautic.
	WebhookURL string `json:"webhook_url"`
	// StorePath e o SQLite das sessoes -- e, na tabela propria, do de-para.
	StorePath string `json:"store_path"`
	// LogLevel e o nivel do log do whatsmeow: DEBUG, INFO, WARN, ERROR.
	LogLevel string `json:"log_level"`
	// Sessions e mapa, e nao lista, porque a chave e o id da sessao e um id
	// repetido numa lista seria dois segredos para o mesmo numero, com o
	// desempate decidido pela ordem do arquivo.
	Sessions map[string]SessionConfig `json:"sessions"`
}

// LoadConfig le, confere a permissao e valida.
func LoadConfig(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: abrindo %s: %w", path, err)
	}
	defer f.Close()

	// O stat e do descritor ja aberto, e nao do caminho: entre um os.Stat e
	// um os.Open o arquivo pode ter sido trocado, e a conferencia teria
	// olhado um arquivo e lido outro.
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("config: lendo %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("config: %s nao e um arquivo comum", path)
	}
	if perm := info.Mode().Perm(); perm != configMode {
		return nil, fmt.Errorf("%w: %s esta %04o e precisa estar %04o -- ele guarda o token e os segredos do webhook; corrija com chmod 600 %s",
			ErrConfigPermissions, path, perm, configMode, path)
	}

	var cfg Config
	dec := json.NewDecoder(f)
	// Campo desconhecido e quase sempre um nome digitado errado, e um
	// "webhook_secrets" no lugar de "sessions" e um servico que sobe sem
	// segredo nenhum e perde em silencio tudo o que chega.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("config: lendo %s: %w", path, err)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	if c.StorePath == "" {
		c.StorePath = "whatsqr.db"
	}
	if c.LogLevel == "" {
		c.LogLevel = "WARN"
	}
	if c.Listen == "" {
		return errors.New("config: listen esta vazio")
	}
	// A recusa vem daqui, e nao de um aviso no log, porque este servico
	// fala por um numero de WhatsApp de verdade: exposto, quem chegar nele
	// manda mensagem como voce.
	if err := api.CheckLoopback(c.Listen); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if len(c.Token) < minSecretLength {
		return fmt.Errorf("config: token com %d caracteres; o minimo e %d", len(c.Token), minSecretLength)
	}
	if c.WebhookURL == "" {
		return errors.New("config: webhook_url esta vazio")
	}
	parsed, err := url.Parse(c.WebhookURL)
	if err != nil {
		return fmt.Errorf("config: webhook_url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("config: webhook_url %q precisa ser http ou https com maquina", c.WebhookURL)
	}
	for id, s := range c.Sessions {
		if id == "" {
			return errors.New("config: ha uma sessao com id vazio")
		}
		if len(s.WebhookSecret) < minSecretLength {
			return fmt.Errorf("config: o segredo da sessao %q tem %d caracteres; o minimo e %d",
				id, len(s.WebhookSecret), minSecretLength)
		}
	}
	return nil
}

// Secret e o que o webhook.Options.Secret consulta.
func (c *Config) Secret(sessionID string) (string, bool) {
	s, ok := c.Sessions[sessionID]
	if !ok || s.WebhookSecret == "" {
		return "", false
	}
	return s.WebhookSecret, true
}

// HasSecret e o que a rota de abrir consulta antes de parear um numero cujo
// que-chega nao teria para onde ir.
func (c *Config) HasSecret(sessionID string) bool {
	_, ok := c.Secret(sessionID)
	return ok
}

// ---------------------------------------------------------------------
// O de-para id de sessao <-> JID do aparelho
// ---------------------------------------------------------------------

// O buraco que isto tapa: o whatsmeow indexa aparelhos por JID, e o id de
// sessao existe ANTES do pareamento, quando ainda nao ha JID nenhum.
// Alguem precisa guardar de quem e cada credencial, e esse alguem e este.
//
// Onde ele mora, e por que: no MESMO arquivo SQLite do whatsmeow, numa
// tabela propria com nome prefixado. Os dois guardam a mesma coisa -- de
// qual numero e aquela credencial -- e tem a mesma vida. Num arquivo so,
// copiar, restaurar, mover ou apagar leva os dois juntos; em dois arquivos,
// um backup que pegue um e nao o outro devolve credenciais orfas, e uma
// credencial orfa e um numero que o servico nao sabe mais de quem e. De
// quebra, a permissao restrita que o desenho ja exige daquele arquivo passa
// a valer para o de-para sem ninguem precisar lembrar de um segundo chmod.
//
// O preco e uma segunda conexao ao mesmo arquivo dentro do processo. Com
// WAL e busy_timeout isso e rotina no SQLite, e a tabela e separada, entao
// as migracoes do whatsmeow nunca a alcancam.
const directorySchema = `
CREATE TABLE IF NOT EXISTS whatsqr_session_device (
    session_id TEXT PRIMARY KEY,
    jid        TEXT NOT NULL UNIQUE,
    bound_at   INTEGER NOT NULL
)`

// errChipChanged e a recusa de trocar o chip de uma sessao ja pareada. A
// regra e a mesma do state.go; aqui ela e a versao que sobrevive ao
// reinicio, quando o objeto Session em memoria nao existe mais.
var errChipChanged = errors.New("config: essa sessao ja pareou com outro chip")

// directoryTimeout e o teto de cada operacao. Existe porque Bind e chamado
// de dentro da goroutine dona da sessao, e uma sessao parada e uma sessao
// que parou de ver queda, volta e mensagem chegando.
const directoryTimeout = 5 * time.Second

type binding struct {
	SessionID string
	JID       string
}

type directory struct {
	db *sql.DB
}

func openDirectory(ctx context.Context, path string) (*directory, error) {
	// As mesmas pragmas do store do whatsmeow, e pelo mesmo motivo:
	// busy_timeout porque o arquivo e tocado por mais de uma conexao, e
	// _txlock=immediate para a transacao do Bind pegar a escrita de uma vez
	// em vez de descobrir no meio que perdeu.
	address := fmt.Sprintf(
		"file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate",
		path,
	)
	db, err := sql.Open("sqlite", address)
	if err != nil {
		return nil, fmt.Errorf("config: abrindo o de-para: %w", err)
	}
	if _, err := db.ExecContext(ctx, directorySchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("config: criando a tabela do de-para: %w", err)
	}
	return &directory{db: db}, nil
}

func (d *directory) Close() error { return d.db.Close() }

// Bind grava de quem e o chip. Silencioso quando o par ja e esse -- toda
// reconexao avisa de novo -- e recusa quando o chip mudou.
func (d *directory) Bind(sessionID, jid string) error {
	if sessionID == "" {
		return errors.New("config: de-para sem id de sessao")
	}
	if jid == "" {
		// Um jid vazio anularia a regra: a sessao ficaria sem chip gravado
		// e aceitaria qualquer um depois do proximo reinicio.
		return errors.New("config: de-para sem jid")
	}

	ctx, cancel := context.WithTimeout(context.Background(), directoryTimeout)
	defer cancel()

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var current string
	err = tx.QueryRowContext(ctx, `SELECT jid FROM whatsqr_session_device WHERE session_id = ?`, sessionID).Scan(&current)
	switch {
	case err == nil:
		if current == jid {
			return nil
		}
		return fmt.Errorf("%w: %s pareou com %s e voltou como %s", errChipChanged, sessionID, current, jid)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}

	// O UNIQUE do jid e o outro lado da mesma regra: duas sessoes no mesmo
	// chip seriam duas caixas falando pela mesma linha, e a resposta sairia
	// pela que chegasse primeiro.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO whatsqr_session_device (session_id, jid, bound_at) VALUES (?, ?, ?)`,
		sessionID, jid, time.Now().Unix(),
	); err != nil {
		return fmt.Errorf("config: gravando o de-para de %s: %w", sessionID, err)
	}
	return tx.Commit()
}

// JID diz com qual chip aquela sessao pareou.
func (d *directory) JID(sessionID string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), directoryTimeout)
	defer cancel()

	var jid string
	err := d.db.QueryRowContext(ctx, `SELECT jid FROM whatsqr_session_device WHERE session_id = ?`, sessionID).Scan(&jid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return jid, true, nil
}

// Unbind esquece o par. Apagar o que nao esta la nao e erro: o DELETE
// precisa poder ser repetido depois de uma falha no meio.
func (d *directory) Unbind(sessionID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), directoryTimeout)
	defer cancel()

	_, err := d.db.ExecContext(ctx, `DELETE FROM whatsqr_session_device WHERE session_id = ?`, sessionID)
	return err
}

// All lista os pares. E por aqui que o servico sabe o que religar depois de
// um reinicio, sem pedir QR de novo.
func (d *directory) All() ([]binding, error) {
	ctx, cancel := context.WithTimeout(context.Background(), directoryTimeout)
	defer cancel()

	rows, err := d.db.QueryContext(ctx, `SELECT session_id, jid FROM whatsqr_session_device ORDER BY session_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []binding
	for rows.Next() {
		var b binding
		if err := rows.Scan(&b.SessionID, &b.JID); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
