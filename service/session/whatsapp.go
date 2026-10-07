package session

import (
	"context"
	"time"
)

// Este arquivo e a fronteira do servico. Daqui para dentro so existem tipos
// deste pacote: nem import do whatsmeow, nem campo com o formato de um
// evento dele. O desenho aprovado promete poder trocar o motor por numero,
// e essa promessa so vale se o que entrar do outro lado desta interface
// puder mudar sem que o gerente perceba.
//
// Os nomes desta interface saiam do desenho aprovado em portugues e foram
// passados para o ingles: o state.go ja estava em ingles, e um pacote com
// Client.SendText ao lado de Manager.Send e pior do que qualquer das duas
// linguas sozinha. Os comentarios continuam em portugues porque o
// raciocinio e lido por quem mantem; o identificador, pelo compilador.

// EventKind diz o que aconteceu do lado do WhatsApp. String nomeada pelo
// mesmo motivo do State: estes valores vao parar em log e no corpo do
// webhook, e um dump precisa ser legivel sem tabela de traducao.
type EventKind string

const (
	// EventPaired: alguem escaneou o QR. Traz o JID do chip que apareceu.
	EventPaired EventKind = "paired"
	// EventDropped: a conexao caiu.
	EventDropped EventKind = "dropped"
	// EventResumed: o socket voltou. Traz o JID de quem voltou, que e o que
	// permite recusar a volta com outro chip.
	EventResumed EventKind = "resumed"
	// EventLoggedOut: o WhatsApp desfez o pareamento.
	EventLoggedOut EventKind = "logged_out"
	// EventMessage: chegou mensagem. Nao mexe no estado da sessao.
	EventMessage EventKind = "message"
	// EventDelivery: mudou o status de entrega de algo que saiu.
	EventDelivery EventKind = "delivery"
	// EventFailed: nao da para continuar -- o QR expirou, o WhatsApp
	// recusou. Reason e o que a tela de pareamento vai mostrar, e e por isso
	// que ele viaja como texto e nao como codigo: quem distingue "expirou"
	// de "recusado" e a borda, que viu o motivo de verdade.
	EventFailed EventKind = "failed"
	// A QR renewal changes only the pairing screen, never the webhook.
	EventQRChanged EventKind = "qr_changed"
)

// Pareado e voltou sao dois eventos, e nao um so, de proposito. Quem sabe
// distinguir o primeiro pareamento de uma volta e a borda, que ve o chip
// chegar pela primeira vez. Com um evento unico o gerente teria que olhar o
// estado atual para escolher entre Scanned e Reconnected -- e ai a regra de
// transicao estaria no gerente, que e exatamente onde ela nao pode estar.

// Event e uma coisa que aconteceu numa sessao. Nao carrega o id da sessao
// porque o canal ja e de uma sessao so: quem le sabe de quem e.
type Event struct {
	Kind EventKind
	// JID vale em EventPaired e EventResumed.
	JID string
	// Reason vale em EventFailed.
	Reason string
	// Message e Delivery sao ponteiros, e nao structs embutidos, porque o
	// valor zero de um struct embutido e indistinguivel de um preenchido
	// pela metade -- e uma mensagem sem texto seria lida como mensagem
	// vazia em vez de evento errado.
	Message  *Inbound
	Delivery *Delivery
}

// Inbound e uma mensagem que chegou, ja sem o formato da biblioteca.
type Inbound struct {
	ID         string
	From       string // Conversation peer JID (recipient when FromMe is true)
	FromMe     bool   // Mirrored message sent on the phone or another linked device
	Historical bool   // Imported history: no customer notifications or automation
	Name       string // Display name supplied by WhatsApp
	Text       string
	Timestamp  time.Time
	Attachment *Attachment
	// Unsupported marca o que chegou e nao vira texto -- midia, sobretudo.
	// O desenho manda mostrar na caixa que veio alguma coisa e nao deu de
	// ler: o cliente manda a foto do boleto e escreve "e esse aqui", e sem
	// isto o atendente le so o "e esse aqui".
	Unsupported bool
}

// DeliveryStatus e o andar da entrega de uma mensagem que saiu.
type DeliveryStatus string

const (
	DeliverySent      DeliveryStatus = "sent"
	DeliveryDelivered DeliveryStatus = "delivered"
	DeliveryRead      DeliveryStatus = "read"
	DeliveryFailed    DeliveryStatus = "failed"
)

// Delivery e a mudanca de status de uma mensagem que saiu.
type Delivery struct {
	ID        string
	To        string
	Status    DeliveryStatus
	Timestamp time.Time
}

// Client e uma sessao de WhatsApp vista de dentro do servico: o minimo que
// o gerente precisa para abrir, mostrar o QR, enviar e fechar.
//
// As implementacoes sao chamadas de mais de uma goroutine ao mesmo tempo --
// a requisicao que serve o QR, a que envia e a goroutine dona da sessao --
// e cada uma se vira com isso por dentro. O gerente serializa a maquina de
// estados, que e dele; nao o cliente, que e da borda.
type Client interface {
	// Connect liga a sessao e devolve o canal por onde os eventos chegam.
	//
	// Quando volta sem erro, ou CurrentQR tem um QR para mostrar, ou JID tem
	// o chip de uma sessao ja pareada restaurada do disco. O servico responde
	// POST /sessions dentro da mesma requisicao e nao pode ficar esperando
	// um evento que talvez nunca venha.
	//
	// O ctx vale pela tentativa de conexao, nao pela vida da sessao: a
	// sessao dura ate Disconnect. Sem essa separacao, o ctx da requisicao
	// HTTP que abriu a sessao a derrubaria ao terminar de responder.
	//
	// Fechar o canal significa que o cliente desistiu de vez.
	Connect(ctx context.Context) (<-chan Event, error)

	// CurrentQR e o QR de agora, ou vazio fora do pareamento. O QR e renovado
	// pelo WhatsApp de tempos em tempos, entao duas chamadas seguidas podem
	// devolver coisas diferentes -- e por isso ele e consulta, e nao um
	// valor entregue uma vez na abertura.
	CurrentQR() string

	// JID e o chip pareado, ou vazio se ainda nao pareou. E o que responde
	// pela sessao que voltou do disco ja autenticada, sem scan nenhum.
	JID() string

	// SendText manda o texto e devolve o id da mensagem. O id nao pode
	// voltar vazio: e ele que amarra o status de entrega que chega depois a
	// bolha certa na caixa.
	SendText(ctx context.Context, to string, text string) (string, error)

	// Disconnect fecha a sessao. E idempotente e pode acontecer no meio de
	// um SendText de outra goroutine; nesse caso o envio falha, o que e
	// preferivel a segurar o desligamento esperando a rede.
	Disconnect()
}
