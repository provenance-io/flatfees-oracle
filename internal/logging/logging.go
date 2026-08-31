// Package logging configures structured JSON logging to stdout and Slack.
// Routing to Kibana/Datadog is handled downstream by the cluster log shipper and
// Datadog; this package only ensures clean, leveled, consistently-tagged events.
package logging

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/google/uuid"
)

const serviceName = "flatfees-oracle"

// New returns a Logger emitting JSON to stdout at the given level, tagged
// with the service name and environment so downstream filtering is trivial.
func New(level, env string) Logger {
	// The id of the logger will ensure that we can determine which service sent the log message.
	// Only use the second section of the uuid.
	id := strings.Split(uuid.New().String(), "-")[1]
	base := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLevel(level)})
	slogger := slog.New(NewSanitizingHandler(base)).With(
		slog.String("service", serviceName),
		slog.String("env", env),
	)
	return &SlogLogger{
		logger: slogger,
		ID:     &id,
		MsgID:  new(int64),
		mutex:  &sync.Mutex{},
	}
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Logger defines the interface for logging operations
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
	Fatal(msg string, args ...any)
	With(args ...any) Logger
}

// SlogLogger implements Logger using slog
type SlogLogger struct {
	logger *slog.Logger
	ID     *string
	MsgID  *int64
	mutex  *sync.Mutex
}

// Debug logs at debug level
func (l *SlogLogger) Debug(msg string, args ...any) {
	l.log(slog.LevelDebug, msg, args...)
}

// Info logs at info level
func (l *SlogLogger) Info(msg string, args ...any) {
	l.log(slog.LevelInfo, msg, args...)
}

// Warn logs at warn level
func (l *SlogLogger) Warn(msg string, args ...any) {
	l.log(slog.LevelWarn, msg, args...)
}

// Error logs at error level and sends notification to Slack if configured
func (l *SlogLogger) Error(msg string, args ...any) {
	l.log(slog.LevelError, msg, args...)
}

// Fatal logs at error level, then panics.
func (l *SlogLogger) Fatal(msg string, args ...any) {
	l.log(slog.LevelError, msg, args...)
	panic(msg)
}

// With returns a logger with additional context
func (l *SlogLogger) With(args ...any) Logger {
	return &SlogLogger{
		logger: l.logger.With(args...),
		ID:     l.ID,
		MsgID:  l.MsgID,
		mutex:  l.mutex,
	}
}

func (l *SlogLogger) nextMsgID() string {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	*l.MsgID++
	return fmt.Sprintf("%s-%d", *l.ID, *l.MsgID)
}

func (l *SlogLogger) log(level slog.Level, msg string, args ...any) {
	// add msg id to the args
	msg_id := l.nextMsgID()
	args = append(args, "msg_id", msg_id)

	switch level {
	case slog.LevelDebug:
		l.logger.Debug(msg, args...)
	case slog.LevelInfo:
		l.logger.Info(msg, args...)
		if defaultSlackNotifier != nil {
			// Use background context for Slack notification
			defaultSlackNotifier.NotifyInfo(context.Background(), msg, msg_id, argsToFields(args))
		}
	case slog.LevelWarn:
		l.logger.Warn(msg, args...)
		if defaultSlackNotifier != nil {
			// Use background context for Slack notification
			defaultSlackNotifier.NotifyWarn(context.Background(), msg, msg_id, argsToFields(args))
		}
	case slog.LevelError:
		l.logger.Error(msg, args...)
		if defaultSlackNotifier != nil {
			// Use background context for Slack notification
			defaultSlackNotifier.NotifyError(context.Background(), msg, msg_id, argsToFields(args))
		}
	}
}

func argsToFields(args []any) map[string]any {
	// Extract error and convert args to fields map
	fields := make(map[string]any)

	// Process args as key-value pairs
	for i := 0; i < len(args); i += 2 {
		if i+1 < len(args) {
			key, ok := args[i].(string)
			if ok {
				// Check if this value is an error
				if errVal, isErr := args[i+1].(error); isErr {
					fields[key] = errVal.Error()
				} else {
					fields[key] = args[i+1]
				}
			}
		}
	}

	return fields
}
