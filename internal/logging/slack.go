package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const prod_project_id = "provenance-io"
const test_project_id = "provenance-io-test"
const log_search_url = "https://console.cloud.google.com/logs/query"
const query_template = ";query=resource.labels.container_name%%3D%%22%s%%22%%0AjsonPayload.msg_id%%3D%%22%s%%22;summaryFields=:false:32:beginning;cursorTimestamp=%s;duration=PT5M?project=%s"

var defaultSlackNotifier *SlackNotifier

func RegisterSlackNotifier(notifier *SlackNotifier) {
	defaultSlackNotifier = notifier
}

type SlackNotifier struct {
	webhookURL string
	service    string
	env        string
	client     *http.Client
	testMode   bool
}

func NewSlackNotifier(webhookURL string, env string) *SlackNotifier {
	testMode := env != "mainnet"

	return &SlackNotifier{
		webhookURL: webhookURL,
		service:    serviceName,
		env:        env,
		client:     &http.Client{Timeout: 3 * time.Second},
		testMode:   testMode,
	}
}

func LogStartup(log Logger) {
	// For the first log message, we clear out the defaultSlackNotifier so that calling log.Info
	// won't also get sent to slack. We'll send a slightly different message to slack.
	sn := defaultSlackNotifier
	defaultSlackNotifier = nil
	defer func() {
		defaultSlackNotifier = sn
	}()

	log.Info("Flatfees Oracle started")
	if sn != nil {
		startupFields := map[string]any{
			"environment": defaultSlackNotifier.env,
			"timestamp":   time.Now().Format(time.RFC3339),
		}
		sn.NotifyStartup(context.Background(), "Flatfees Oracle started", startupFields)
	}
}

// NotifyStartup sends a formatted startup banner to Slack.
// It is deliberately best-effort: it will never panic or return an error.
// This bypasses throttling since startup is a one-time event.
func (s *SlackNotifier) NotifyStartup(ctx context.Context, title string, fields map[string]any) {
	if s == nil || s.webhookURL == "" {
		return // misconfigured, just do nothing
	}

	// Create banner with 7 :prov: emojis
	banner := ":prov::prov::prov::prov::prov::prov::prov:"

	// Build the formatted message
	text := fmt.Sprintf("*%s*\n%s", title, banner)

	if len(fields) > 0 {
		// attach fields as a JSON blob at the bottom
		data, _ := json.Marshal(fields)
		text += fmt.Sprintf("\n```%s```", string(data))
	}

	s.notify(ctx, text)
}

// NotifyInfo sends a simple formatted info message to Slack.
// It is deliberately best-effort: it will never panic or return an error.
func (s *SlackNotifier) NotifyInfo(ctx context.Context, message string, msg_id string, fields map[string]any) {
	if s == nil || s.webhookURL == "" {
		return // misconfigured, just do nothing
	}

	emoji := ":point-right:"
	if s.testMode {
		emoji = ":point-left:"
	}

	text := s.createLogText(emoji, "INFO", message, msg_id, fields)
	s.notify(ctx, text)
}

// NotifyWarn sends a simple formatted warning message to Slack.
// It is deliberately best-effort: it will never panic or return an error.
func (s *SlackNotifier) NotifyWarn(ctx context.Context, message string, msg_id string, fields map[string]any) {
	if s == nil || s.webhookURL == "" {
		return // misconfigured, just do nothing
	}

	emoji := ":firecracker:"
	if s.testMode {
		emoji = ":shrug:"
	}

	text := s.createLogText(emoji, "WARNING", message, msg_id, fields)
	s.notify(ctx, text)
}

// NotifyError sends a simple formatted error message to Slack.
// It is deliberately best-effort: it will never panic or return an error.
func (s *SlackNotifier) NotifyError(ctx context.Context, message string, msg_id string, fields map[string]any) {
	if s == nil || s.webhookURL == "" {
		return // misconfigured, just do nothing
	}

	emoji := ":boom:"
	if s.testMode {
		emoji = ":information_desk_person:"
	}

	text := s.createLogText(emoji, "ERROR", message, msg_id, fields)
	s.notify(ctx, text)
}

func formatSlackFieldsBlob(fields map[string]any, max int) string {
	data, _ := json.Marshal(fields)
	sanitized := SanitizeMsg(string(data))
	if max > 0 && len(sanitized) > max {
		// Use ToValidUTF8 here so that we don't get partial utf-8 if :max is in the middle of an emoji.
		sanitized = strings.ToValidUTF8(sanitized[:max], "")
	}
	return sanitized
}

func (s *SlackNotifier) createLogText(emoji, bold string, message string, msg_id string, fields map[string]any) string {
	envProjectID := prod_project_id
	if s.testMode {
		envProjectID = test_project_id
	}

	cursorTimestamp := time.Now().UTC().Format(time.RFC3339Nano)
	queryURL := log_search_url + fmt.Sprintf(query_template, s.service, msg_id, cursorTimestamp, envProjectID)

	// Build a one-line summary plus optional details
	header := fmt.Sprintf("%s *%s* in `%s - %s`", emoji, bold, s.service, s.env)
	if s.service == "" && s.env == "" {
		header = fmt.Sprintf("%s *%s*", emoji, bold)
	}

	text := fmt.Sprintf("%s - <%s|View Logs>\n>%s", header, queryURL, message)

	if len(fields) > 0 {
		text += fmt.Sprintf("\n```%s```", formatSlackFieldsBlob(fields, 1000))
	}

	return text
}

func (s *SlackNotifier) notify(ctx context.Context, text string) {
	payload := map[string]any{
		"text": text,
	}

	body, _ := json.Marshal(payload)

	// short-lived context so this can't hang your request
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, errReq := http.NewRequestWithContext(reqCtx, "POST", s.webhookURL, bytes.NewReader(body))
	if errReq != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")

	// Best-effort, ignore response errors (maybe log locally if you want)
	resp, errDo := s.client.Do(req)
	if errDo != nil {
		return
	}
	_ = resp.Body.Close()
}
