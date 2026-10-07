package session

import (
	"fmt"
	"net/url"
	"regexp"

	waLog "go.mau.fi/whatsmeow/util/log"
)

var loggedURL = regexp.MustCompile(`https?://[^\s"<>]+`)

// Whatsmeow errors may contain signed media URLs. Never retain their tokens.
func privateLogMessage(format string, args ...any) string {
	return loggedURL.ReplaceAllStringFunc(fmt.Sprintf(format, args...), func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return "[private URL]"
		}
		u.RawQuery = ""
		u.Fragment = ""
		u.User = nil
		return u.String()
	})
}

type privateLogger struct{ waLog.Logger }

func (l privateLogger) Warnf(f string, a ...any)       { l.Logger.Warnf("%s", privateLogMessage(f, a...)) }
func (l privateLogger) Errorf(f string, a ...any)      { l.Logger.Errorf("%s", privateLogMessage(f, a...)) }
func (l privateLogger) Infof(f string, a ...any)       { l.Logger.Infof("%s", privateLogMessage(f, a...)) }
func (l privateLogger) Debugf(f string, a ...any)      { l.Logger.Debugf("%s", privateLogMessage(f, a...)) }
func (l privateLogger) Sub(module string) waLog.Logger { return privateLogger{l.Logger.Sub(module)} }
