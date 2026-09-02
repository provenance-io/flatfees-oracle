package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newCapturingServer starts an httptest server that records every request body
// it receives and always responds 200 OK.
func newCapturingServer(t *testing.T) (srv *httptest.Server, bodies *[]string) {
	t.Helper()
	var got []string
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = append(got, string(body))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

// slackText decodes the {"text": "..."} payload notify() posts and returns the
// text, undoing json.Marshal's HTML-escaping of '<'/'>' (as used in the
// "<url|View Logs>" link) so callers can assert on the real message content.
func slackText(t *testing.T, rawJSON string) string {
	t.Helper()
	var payload struct {
		Text string `json:"text"`
	}
	require.NoError(t, json.Unmarshal([]byte(rawJSON), &payload), "decoding slack payload: %s", rawJSON)
	return payload.Text
}

// withRegisteredNotifier registers sn as the package's defaultSlackNotifier for
// the duration of the test and restores whatever was registered before.
func withRegisteredNotifier(t *testing.T, sn *SlackNotifier) {
	t.Helper()
	prev := defaultSlackNotifier
	RegisterSlackNotifier(sn)
	t.Cleanup(func() { defaultSlackNotifier = prev })
}

func TestSlackNotifier_LevelGating(t *testing.T) {
	tests := []struct {
		name            string
		configuredLevel string
		call            func(sn *SlackNotifier)
		wantSent        bool
	}{
		{
			name:            "debug configured: debug is sent",
			configuredLevel: "debug",
			call:            func(sn *SlackNotifier) { sn.NotifyDebug(context.Background(), "msg", "id-1", nil) },
			wantSent:        true,
		},
		{
			name:            "debug configured: info is sent",
			configuredLevel: "debug",
			call:            func(sn *SlackNotifier) { sn.NotifyInfo(context.Background(), "msg", "id-1", nil) },
			wantSent:        true,
		},
		{
			name:            "debug configured: warn is sent",
			configuredLevel: "debug",
			call:            func(sn *SlackNotifier) { sn.NotifyWarn(context.Background(), "msg", "id-1", nil) },
			wantSent:        true,
		},
		{
			name:            "debug configured: error is sent",
			configuredLevel: "debug",
			call:            func(sn *SlackNotifier) { sn.NotifyError(context.Background(), "msg", "id-1", nil) },
			wantSent:        true,
		},

		{
			name:            "info configured: debug is not sent",
			configuredLevel: "info",
			call:            func(sn *SlackNotifier) { sn.NotifyDebug(context.Background(), "msg", "id-1", nil) },
			wantSent:        false,
		},
		{
			name:            "info configured: info is sent",
			configuredLevel: "info",
			call:            func(sn *SlackNotifier) { sn.NotifyInfo(context.Background(), "msg", "id-1", nil) },
			wantSent:        true,
		},
		{
			name:            "info configured: warn is sent",
			configuredLevel: "info",
			call:            func(sn *SlackNotifier) { sn.NotifyWarn(context.Background(), "msg", "id-1", nil) },
			wantSent:        true,
		},
		{
			name:            "info configured: error is sent",
			configuredLevel: "info",
			call:            func(sn *SlackNotifier) { sn.NotifyError(context.Background(), "msg", "id-1", nil) },
			wantSent:        true,
		},

		{
			name:            "warn configured: debug is not sent",
			configuredLevel: "warn",
			call:            func(sn *SlackNotifier) { sn.NotifyDebug(context.Background(), "msg", "id-1", nil) },
			wantSent:        false,
		},
		{
			name:            "warn configured: info is not sent",
			configuredLevel: "warn",
			call:            func(sn *SlackNotifier) { sn.NotifyInfo(context.Background(), "msg", "id-1", nil) },
			wantSent:        false,
		},
		{
			name:            "warn configured: warn is sent",
			configuredLevel: "warn",
			call:            func(sn *SlackNotifier) { sn.NotifyWarn(context.Background(), "msg", "id-1", nil) },
			wantSent:        true,
		},
		{
			name:            "warn configured: error is sent",
			configuredLevel: "warn",
			call:            func(sn *SlackNotifier) { sn.NotifyError(context.Background(), "msg", "id-1", nil) },
			wantSent:        true,
		},

		{
			name:            "error configured: debug is not sent",
			configuredLevel: "error",
			call:            func(sn *SlackNotifier) { sn.NotifyDebug(context.Background(), "msg", "id-1", nil) },
			wantSent:        false,
		},
		{
			name:            "error configured: info is not sent",
			configuredLevel: "error",
			call:            func(sn *SlackNotifier) { sn.NotifyInfo(context.Background(), "msg", "id-1", nil) },
			wantSent:        false,
		},
		{
			name:            "error configured: warn is not sent",
			configuredLevel: "error",
			call:            func(sn *SlackNotifier) { sn.NotifyWarn(context.Background(), "msg", "id-1", nil) },
			wantSent:        false,
		},
		{
			name:            "error configured: error is sent",
			configuredLevel: "error",
			call:            func(sn *SlackNotifier) { sn.NotifyError(context.Background(), "msg", "id-1", nil) },
			wantSent:        true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, bodies := newCapturingServer(t)
			sn := NewSlackNotifier(srv.URL, "testnet", tc.configuredLevel)
			tc.call(sn)
			gotSent := len(*bodies) > 0
			assert.Equal(t, tc.wantSent, gotSent, "request sent to slack")
		})
	}
}

func TestSlogLogger_DispatchesToConfiguredNotifier(t *testing.T) {
	tests := []struct {
		name     string
		logFunc  func(l Logger)
		wantText string // text createLogText embeds for this level
	}{
		{name: "debug", logFunc: func(l Logger) { l.Debug("debug msg") }, wantText: "DEBUG"},
		{name: "info", logFunc: func(l Logger) { l.Info("info msg") }, wantText: "INFO"},
		{name: "warn", logFunc: func(l Logger) { l.Warn("warn msg") }, wantText: "WARNING"},
		{name: "error", logFunc: func(l Logger) { l.Error("error msg") }, wantText: "ERROR"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, bodies := newCapturingServer(t)
			sn := NewSlackNotifier(srv.URL, "testnet", "debug") // debug: nothing gets filtered out
			withRegisteredNotifier(t, sn)

			l := New("debug", "testnet")
			tc.logFunc(l)

			require.Len(t, *bodies, 1, "expected exactly one slack notification")
			text := slackText(t, (*bodies)[0])
			assert.Contains(t, text, tc.name+" msg")
			assert.Contains(t, text, tc.wantText)
		})
	}
}

// LOG_LEVEL (the local stdout threshold) and SLACK_LOG_LEVEL (the Slack
// threshold) are independent knobs: SlogLogger.log() decides whether to
// notify Slack based purely on the level passed to Debug/Info/Warn/Error and
// SlackNotifier's own configured level, regardless of whether the local
// handler would have suppressed that line on stdout.
func TestSlogLogger_LocalLevelAndSlackLevelAreIndependent(t *testing.T) {
	t.Run("LOG_LEVEL=error, SLACK_LOG_LEVEL=debug: debug reaches slack but not stdout", func(t *testing.T) {
		var buf bytes.Buffer
		l := newTestLogger(&buf, slog.LevelError) // local threshold: only ERROR+

		srv, bodies := newCapturingServer(t)
		sn := NewSlackNotifier(srv.URL, "testnet", "debug") // slack threshold: everything
		withRegisteredNotifier(t, sn)

		l.Debug("debug msg")

		assert.Empty(t, buf.String(), "LOG_LEVEL=error should suppress a Debug line from stdout")
		require.Len(t, *bodies, 1, "SLACK_LOG_LEVEL=debug should still send the Debug message to slack")
		assert.Contains(t, slackText(t, (*bodies)[0]), "debug msg")
	})

	t.Run("LOG_LEVEL=debug, SLACK_LOG_LEVEL=error: debug reaches stdout but not slack", func(t *testing.T) {
		var buf bytes.Buffer
		l := newTestLogger(&buf, slog.LevelDebug) // local threshold: everything

		srv, bodies := newCapturingServer(t)
		sn := NewSlackNotifier(srv.URL, "testnet", "error") // slack threshold: only ERROR+
		withRegisteredNotifier(t, sn)

		l.Debug("debug msg")

		assert.Contains(t, buf.String(), "debug msg", "LOG_LEVEL=debug should still write the Debug line to stdout")
		assert.Empty(t, *bodies, "SLACK_LOG_LEVEL=error should suppress the Debug message from slack")
	})
}

func TestNotifyInfo_MessageAndFieldsInBody(t *testing.T) {
	srv, bodies := newCapturingServer(t)
	sn := NewSlackNotifier(srv.URL, "testnet", "info")

	sn.NotifyInfo(context.Background(), "price fetched", "ab12-3", map[string]any{"trades": 42})

	require.Len(t, *bodies, 1)
	text := slackText(t, (*bodies)[0])
	assert.Contains(t, text, "price fetched")
	assert.Contains(t, text, "INFO")
	assert.Contains(t, text, `trades`)
	assert.Contains(t, text, "42")
}

func TestNotify_SanitizesPrivateKeyHex(t *testing.T) {
	t.Setenv("PRIVATE_KEY_HEX", "deadbeef")

	srv, bodies := newCapturingServer(t)
	sn := NewSlackNotifier(srv.URL, "testnet", "error")

	sn.NotifyError(context.Background(), "signer init failed: deadbeef", "id-1", map[string]any{"key": "deadbeef"})

	require.Len(t, *bodies, 1)
	text := slackText(t, (*bodies)[0])
	assert.NotContains(t, text, "deadbeef", "raw secret must never reach slack")
	assert.Contains(t, text, "<PRIVATE_KEY_HEX>")
}

func TestSlackNotifier_NilAndMisconfiguredAreNoops(t *testing.T) {
	tests := []struct {
		name     string
		notifier *SlackNotifier
	}{
		{name: "nil notifier", notifier: nil},
		{name: "misconfigured notifier", notifier: NewSlackNotifier("", "testnet", "info")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				tc.notifier.NotifyStartup(context.Background(), "title", nil)
			}, "NotifyStartup")
			assert.NotPanics(t, func() {
				tc.notifier.NotifyDebug(context.Background(), "msg", "id-1", nil)
			}, "NotifyDebug")
			assert.NotPanics(t, func() {
				tc.notifier.NotifyInfo(context.Background(), "msg", "id-1", nil)
			}, "NotifyInfo")
			assert.NotPanics(t, func() {
				tc.notifier.NotifyWarn(context.Background(), "msg", "id-1", nil)
			}, "NotifyWarn")
			assert.NotPanics(t, func() {
				tc.notifier.NotifyError(context.Background(), "msg", "id-1", nil)
			}, "NotifyError")
		})
	}
}

func TestLogStartupMsg(t *testing.T) {
	srv, bodies := newCapturingServer(t)
	sn := NewSlackNotifier(srv.URL, "testnet", "info")
	withRegisteredNotifier(t, sn)

	logger := New("info", "testnet")
	LogStartupMsg(logger)

	require.Len(t, *bodies, 1, "expected exactly one slack notification for startup (no duplicate from the suppressed log.Info), got: %v", *bodies)
	text := slackText(t, (*bodies)[0])
	assert.Contains(t, text, "Flatfees Oracle started")
	assert.Same(t, sn, defaultSlackNotifier, "defaultSlackNotifier must be restored after LogStartupMsg returns")
}

func TestFormatSlackFieldsBlob(t *testing.T) {
	t.Run("max of 0 does not truncate", func(t *testing.T) {
		fields := map[string]any{"key": strings.Repeat("a", 2000)}
		got := formatSlackFieldsBlob(fields, 0)
		assert.Greater(t, len(got), 1000)
	})

	t.Run("truncates to max and stays valid utf8", func(t *testing.T) {
		// "€" is 3 bytes in UTF-8, so a 1000-byte cut lands mid-rune unless the
		// truncation is utf8-aware.
		fields := map[string]any{"key": strings.Repeat("€", 500)}
		got := formatSlackFieldsBlob(fields, 1000)
		assert.LessOrEqual(t, len(got), 1000)
		assert.True(t, utf8.ValidString(got), "truncated blob must be valid utf8, got: %q", got)
	})

	t.Run("sanitizes private key hex", func(t *testing.T) {
		t.Setenv("PRIVATE_KEY_HEX", "deadbeef")
		fields := map[string]any{"key": "deadbeef"}
		got := formatSlackFieldsBlob(fields, 0)
		assert.NotContains(t, got, "deadbeef")
		assert.Contains(t, got, "<PRIVATE_KEY_HEX>")
	})
}
