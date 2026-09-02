package logging

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestLogger builds a *SlogLogger writing JSON to buf at the given level,
// bypassing New()'s hardcoded os.Stdout so tests can inspect the output.
func newTestLogger(buf *bytes.Buffer, level slog.Level) *SlogLogger {
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level})
	id := "test"
	return &SlogLogger{
		logger: slog.New(NewSanitizingHandler(h)),
		ID:     &id,
		MsgID:  new(int64),
		mutex:  &sync.Mutex{},
	}
}

type logLine struct {
	Level string `json:"level"`
	Msg   string `json:"msg"`
	MsgID string `json:"msg_id"`
	Comp  string `json:"component"`
}

// decodeLogLines parses each newline-delimited JSON record slog wrote to buf.
func decodeLogLines(t *testing.T, buf *bytes.Buffer) []logLine {
	t.Helper()
	var lines []logLine
	sc := bufio.NewScanner(strings.NewReader(buf.String()))
	for sc.Scan() {
		text := sc.Text()
		if text == "" {
			continue
		}
		var l logLine
		require.NoError(t, json.Unmarshal([]byte(text), &l), "line: %s", text)
		lines = append(lines, l)
	}
	require.NoError(t, sc.Err())
	return lines
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want slog.Level
	}{
		{name: "debug", in: "debug", want: slog.LevelDebug},
		{name: "debug uppercase", in: "DEBUG", want: slog.LevelDebug},
		{name: "info", in: "info", want: slog.LevelInfo},
		{name: "warn", in: "warn", want: slog.LevelWarn},
		{name: "warning alias", in: "warning", want: slog.LevelWarn},
		{name: "error", in: "error", want: slog.LevelError},
		{name: "padded with whitespace", in: "  warn  ", want: slog.LevelWarn},
		{name: "empty defaults to info", in: "", want: slog.LevelInfo},
		{name: "unrecognized defaults to info", in: "verbose", want: slog.LevelInfo},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, parseLevel(tc.in))
		})
	}
}

func TestNew_AssignsIndependentIDAndStartsCounterAtZero(t *testing.T) {
	l1 := New("debug", "testnet")
	sl1, ok := l1.(*SlogLogger)
	require.True(t, ok, "New must return a *SlogLogger")
	require.NotNil(t, sl1.ID)
	assert.Len(t, *sl1.ID, 4, "id is the second (4-hex-char) segment of a uuid")
	if assert.NotNil(t, sl1.MsgID) {
		assert.Equal(t, int64(0), *sl1.MsgID)
	}

	l2 := New("debug", "testnet")
	sl2 := l2.(*SlogLogger)
	assert.NotEqual(t, *sl1.ID, *sl2.ID, "each New() call should mint its own id")
	assert.NotSame(t, sl1.MsgID, sl2.MsgID, "each New() call should get its own counter")
}

func TestSlogLogger_LevelsLogExpectedContentAndIncrementMsgID(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf, slog.LevelDebug)

	l.Debug("debug msg")
	l.Info("info msg")
	l.Warn("warn msg")
	l.Error("error msg")
	lines := decodeLogLines(t, &buf)

	wants := []struct {
		level string
		msg   string
		msgID string
	}{
		{level: "DEBUG", msg: "debug msg", msgID: "test-1"},
		{level: "INFO", msg: "info msg", msgID: "test-2"},
		{level: "WARN", msg: "warn msg", msgID: "test-3"},
		{level: "ERROR", msg: "error msg", msgID: "test-4"},
	}

	require.Len(t, lines, len(wants))
	for i, line := range lines {
		assert.Equal(t, wants[i].level, line.Level, "line %d level", i)
		assert.Equal(t, wants[i].msg, line.Msg, "line %d msg", i)
		assert.Equal(t, wants[i].msgID, line.MsgID, "line %d msg_id", i)
	}
}

func TestSlogLogger_With_ReturnsNewInstanceSharingCounterAndID(t *testing.T) {
	var buf bytes.Buffer
	base := newTestLogger(&buf, slog.LevelDebug)

	child := base.With("component", "x")
	childImpl, ok := child.(*SlogLogger)
	require.True(t, ok)

	assert.NotSame(t, base, childImpl, "With must return a new logger, not mutate the receiver")
	assert.Same(t, base.ID, childImpl.ID, "id must be shared across the logger family")
	assert.Same(t, base.MsgID, childImpl.MsgID, "the msg_id counter must be shared across the logger family")
	assert.Same(t, base.mutex, childImpl.mutex, "the mutex guarding the counter must be shared too")
}

func TestSlogLogger_With_DoesNotMutateOriginalAttributes(t *testing.T) {
	var buf bytes.Buffer
	base := newTestLogger(&buf, slog.LevelDebug)

	_ = base.With("component", "x")
	base.Info("still just the base logger")

	lines := decodeLogLines(t, &buf)
	require.Len(t, lines, 1)
	assert.Empty(t, lines[0].Comp, "calling With() must not add attributes to the original logger")
}

func TestSlogLogger_With_SharesMonotonicCounterAcrossFamily(t *testing.T) {
	var buf bytes.Buffer
	base := newTestLogger(&buf, slog.LevelDebug)
	child := base.With("component", "x")

	base.Info("from base")
	child.Info("from child")
	base.Info("from base again")

	lines := decodeLogLines(t, &buf)
	require.Len(t, lines, 3)
	assert.Equal(t, "test-1", lines[0].MsgID)
	assert.Equal(t, "test-2", lines[1].MsgID)
	assert.Equal(t, "test-3", lines[2].MsgID)
	assert.Equal(t, "x", lines[1].Comp, "the child's attribute should show up on its own line")
	assert.Empty(t, lines[2].Comp, "but not leak back onto the base logger's line")
}

func TestSlogLogger_Fatal_PanicsAndLogsAtErrorLevel(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf, slog.LevelDebug)

	assert.PanicsWithValue(t, "boom", func() { l.Fatal("boom") })

	lines := decodeLogLines(t, &buf)
	require.Len(t, lines, 1)
	assert.Equal(t, "ERROR", lines[0].Level)
	assert.Equal(t, "boom", lines[0].Msg)
}

func TestSlogLogger_ConcurrentLoggingProducesUniqueMsgIDs(t *testing.T) {
	var buf bytes.Buffer
	base := newTestLogger(&buf, slog.LevelDebug)
	child := base.With("component", "x")

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				base.Info("concurrent")
			} else {
				child.Info("concurrent")
			}
		}(i)
	}
	wg.Wait()

	lines := decodeLogLines(t, &buf)
	require.Len(t, lines, n)

	seen := make(map[string]bool, n)
	for _, line := range lines {
		assert.False(t, seen[line.MsgID], "msg_id %q was produced more than once", line.MsgID)
		seen[line.MsgID] = true
	}
	assert.Len(t, seen, n, "every concurrent log call must get a unique msg_id")
}

func TestArgsToFields(t *testing.T) {
	tests := []struct {
		name string
		args []any
		want map[string]any
	}{
		{
			name: "simple key value pairs",
			args: []any{"a", 1, "b", "two"},
			want: map[string]any{"a": 1, "b": "two"},
		},
		{
			name: "error values are stringified",
			args: []any{"error", errors.New("boom")},
			want: map[string]any{"error": "boom"},
		},
		{
			name: "trailing key without a value is dropped",
			args: []any{"a", 1, "orphan"},
			want: map[string]any{"a": 1},
		},
		{
			name: "non-string key is dropped",
			args: []any{42, "value", "a", 1},
			want: map[string]any{"a": 1},
		},
		{
			name: "no args",
			args: nil,
			want: map[string]any{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, argsToFields(tc.args))
		})
	}
}
