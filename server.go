package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type Server struct {
	codex  *CodexClient
	log    *slog.Logger
	apiKey string
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.HandleFunc("POST /v1/responses", s.handleResponses)
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	return s.logRequests(s.requireAPIKey(mux))
}

func (s *Server) requireAPIKey(next http.Handler) http.Handler {
	if s.apiKey == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validBearerToken(r.Header.Get("Authorization"), s.apiKey) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="codex-proxy"`)
			writeOpenAIError(w, http.StatusUnauthorized, "missing or invalid API key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func validBearerToken(header, apiKey string) bool {
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	tokenHash := sha256.Sum256([]byte(token))
	apiKeyHash := sha256.Sum256([]byte(apiKey))
	return subtle.ConstantTimeCompare(tokenHash[:], apiKeyHash[:]) == 1
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		requestID := randomHex(4)
		log := s.log.With(
			"request_id", requestID,
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
		)
		lw := &loggingResponseWriter{ResponseWriter: w, status: http.StatusOK}
		log.Info("request started")
		next.ServeHTTP(lw, r)

		log.Info("request finished",
			"status", lw.status,
			"bytes", lw.bytes,
			"duration", time.Since(start).Round(time.Millisecond).String(),
		)
	})
}

type loggingResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *loggingResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *loggingResponseWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

func (w *loggingResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.codex.Models(r.Context())
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, openAIModelsResponse(models))
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	raw, err := decodeJSONMap(r)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	upstream, stream, err := NormalizeResponsesRequest(raw)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if stream {
		s.streamResponses(w, r.Context(), upstream)
		return
	}
	agg, err := s.aggregateResponses(r.Context(), upstream)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, agg)
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	raw, err := decodeJSONMap(r)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	upstream, stream, err := BuildResponsesRequestFromChat(raw)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	model := stringValue(raw, "model")
	if stream {
		s.streamChatCompletions(w, r.Context(), upstream, model, includeUsage(raw))
		return
	}
	agg, err := s.aggregateResponses(r.Context(), upstream)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ChatCompletionFromAggregate(agg, model))
}

func (s *Server) aggregateResponses(ctx context.Context, upstream map[string]any) (OpenAIResponse, error) {
	resp, err := s.codex.StreamResponses(ctx, upstream)
	if err != nil {
		return OpenAIResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return OpenAIResponse{}, upstreamError(resp)
	}
	return AggregateResponsesStream(resp.Body, upstream)
}

func (s *Server) streamChatCompletions(w http.ResponseWriter, ctx context.Context, upstream map[string]any, model string, sendUsage bool) {
	resp, err := s.codex.StreamResponses(ctx, upstream)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeOpenAIError(w, resp.StatusCode, upstreamError(resp).Error())
		return
	}

	setSSEHeaders(w.Header())
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	id := "chatcmpl-" + randomHex(16)
	created := time.Now().Unix()
	finishReason := "stop"
	var usage any
	toolIndex := 0

	sendChunk := func(choices []any, usage any) error {
		chunk := openAIChatCompletionChunk(id, model, created, choices, usage)
		if err := writeSSEData(w, chunk); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}

	if err := sendChunk([]any{openAIChatDeltaChoice(map[string]any{"role": "assistant", "content": ""}, nil)}, nil); err != nil {
		return
	}

	err = ReadStreamEvents(resp.Body, func(event StreamEvent) error {
		switch event.Type {
		case "response.output_text.delta":
			return sendChunk([]any{openAIChatDeltaChoice(map[string]any{"content": stringValue(event.Data, "delta")}, nil)}, nil)
		case "response.output_item.done":
			item, _ := event.Data["item"].(map[string]any)
			if stringValue(item, "type") != "function_call" {
				return nil
			}
			finishReason = "tool_calls"
			callID := defaultedString(item, "call_id", defaultedString(item, "id", "call_"+randomHex(8)))
			arguments := defaultedString(item, "arguments", "{}")
			delta := openAIChatToolCallDelta(toolIndex, callID, stringValue(item, "name"), arguments)
			toolIndex++
			return sendChunk([]any{openAIChatDeltaChoice(delta, nil)}, nil)
		case "response.completed":
			if response, ok := event.Data["response"].(map[string]any); ok {
				usage = response["usage"]
			}
		}
		return nil
	})
	if err != nil || ctx.Err() != nil {
		return
	}

	_ = sendChunk([]any{openAIChatDeltaChoice(map[string]any{}, finishReason)}, nil)
	if sendUsage {
		_ = sendChunk([]any{}, chatUsageFromResponsesUsage(usage))
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func (s *Server) streamResponses(w http.ResponseWriter, ctx context.Context, upstream map[string]any) {
	resp, err := s.codex.StreamResponses(ctx, upstream)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeOpenAIError(w, resp.StatusCode, upstreamError(resp).Error())
		return
	}

	setSSEHeaders(w.Header())
	w.WriteHeader(http.StatusOK)
	if err := copyAndFlush(w, resp.Body); err != nil || ctx.Err() != nil {
		return
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	flush(w)
}

func decodeJSONMap(r *http.Request) (map[string]any, error) {
	dec := json.NewDecoder(r.Body)
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("invalid JSON body: %w", err)
	}
	return raw, nil
}

func upstreamError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	message := strings.TrimSpace(string(body))
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err == nil {
		if detail := stringValue(payload, "detail"); detail != "" {
			message = detail
		} else if errObj, ok := payload["error"].(map[string]any); ok {
			if msg := stringValue(errObj, "message"); msg != "" {
				message = msg
			}
		}
	}
	if message == "" {
		message = resp.Status
	}
	return fmt.Errorf("Codex upstream returned HTTP %d: %s", resp.StatusCode, message)
}

func includeUsage(raw map[string]any) bool {
	options, ok := raw["stream_options"].(map[string]any)
	return ok && boolValue(options, "include_usage")
}

func copyAndFlush(w http.ResponseWriter, r io.Reader) error {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
			flush(w)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func flush(w http.ResponseWriter) {
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeSSEData(w io.Writer, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", b)
	return err
}

func setSSEHeaders(header http.Header) {
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeOpenAIError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, openAIErrorResponse(message))
}
