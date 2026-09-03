package logging

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeMsg(t *testing.T) {
	tests := []struct {
		name       string
		privKeyHex string
		msg        string
		expMsg     string
	}{
		{
			name:       "no private key hex",
			privKeyHex: "",
			msg:        "abcde",
			expMsg:     "abcde",
		},
		{
			name:       "empty msg",
			privKeyHex: "0xa1b2",
			msg:        "",
			expMsg:     "",
		},
		{
			name:       "msg without private key hex",
			privKeyHex: "0xa1b2",
			msg:        "just a message",
			expMsg:     "just a message",
		},
		{
			name:       "msg with private key hex",
			privKeyHex: "0xa1b2",
			msg:        "This has the a1b2 string.",
			expMsg:     "This has the <PRIVATE_KEY_HEX> string.",
		},
		{
			name:       "msg with private key hex three times",
			privKeyHex: "0xa1b3",
			msg:        "0xa1b3 plus a1b3 and again a1b3 for good measure",
			expMsg:     "<PRIVATE_KEY_HEX> plus <PRIVATE_KEY_HEX> and again <PRIVATE_KEY_HEX> for good measure",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PRIVATE_KEY_HEX", tc.privKeyHex)
			var actMsg string
			testFunc := func() {
				actMsg = SanitizeMsg(tc.msg)
			}
			require.NotPanics(t, testFunc, "SanitizeMsg(%q)", tc.msg)
			assert.Equal(t, tc.expMsg, actMsg, "SanitizeMsg(%q)", tc.msg)
		})
	}
}

func TestSanitizingHandler(t *testing.T) {
	tests := []struct {
		name       string
		privKeyHex string
		msg        string
		args       []any
		expLog     string
	}{
		{
			name:       "log msg without anything to sanitize",
			privKeyHex: "0xa1b2",
			msg:        "This is just a normal message.",
			args:       []any{"param1", "val1"},
			expLog:     `msg="This is just a normal message." param1=val1`,
		},
		{
			name:       "log msg with priv key hex string in it",
			privKeyHex: "0xa1b2",
			msg:        "This message has a1b2 in it.",
			expLog:     `msg="This message has <PRIVATE_KEY_HEX> in it."`,
		},
		{
			name:       "args contain priv key hex string",
			privKeyHex: "0xa1b2",
			msg:        "This is a message.",
			args:       []any{"param1", "val1", "param2", "[a1b2]"},
			expLog:     `msg="This is a message." param1=val1 param2=[<PRIVATE_KEY_HEX>]`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PRIVATE_KEY_HEX", tc.privKeyHex)

			// Create a new logger that writes to a buffer.
			var buf bytes.Buffer
			handler := NewSanitizingHandler(slog.NewTextHandler(&buf, nil))
			logger := slog.New(handler)

			// Log the message with the args.
			logger.Info(tc.msg, tc.args...)

			// Get what was logged.
			actLog := buf.String()

			// Check it against expected.
			assert.Contains(t, actLog, tc.expLog, "logged output: %s", actLog)
		})
	}
}
