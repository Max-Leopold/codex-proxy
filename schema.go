package main

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
)

// OpenAI-compatible response shapes live here so endpoint schemas are easy to audit.
// Field sets are based on https://github.com/openai/openai-openapi.

type OpenAIResponse struct {
	ID                 string         `json:"id"`
	Object             string         `json:"object"`
	CreatedAt          int64          `json:"created_at"`
	Status             string         `json:"status"`
	Error              any            `json:"error"`
	IncompleteDetails  any            `json:"incomplete_details"`
	Instructions       any            `json:"instructions"`
	MaxOutputTokens    any            `json:"max_output_tokens"`
	Model              string         `json:"model"`
	Output             []any          `json:"output"`
	ParallelToolCalls  bool           `json:"parallel_tool_calls"`
	PreviousResponseID any            `json:"previous_response_id"`
	Reasoning          any            `json:"reasoning"`
	Store              bool           `json:"store"`
	Temperature        float64        `json:"temperature"`
	Text               any            `json:"text"`
	ToolChoice         any            `json:"tool_choice"`
	Tools              []any          `json:"tools"`
	TopP               float64        `json:"top_p"`
	Truncation         string         `json:"truncation"`
	Usage              any            `json:"usage"`
	User               any            `json:"user"`
	Metadata           map[string]any `json:"metadata"`

	OutputText string `json:"-"`
}

func openAIModelsResponse(models []CodexModel) map[string]any {
	data := make([]any, 0, len(models))
	for _, model := range models {
		if !model.SupportedInAPI || model.Visibility != "list" {
			continue
		}
		data = append(data, map[string]any{
			"id":       model.Slug,
			"object":   "model",
			"created":  0,
			"owned_by": "openai-codex",
		})
	}
	return map[string]any{"object": "list", "data": data}
}

func openAIErrorResponse(message string) map[string]any {
	return map[string]any{"error": map[string]any{
		"message": message,
		"type":    "invalid_request_error",
		"param":   nil,
		"code":    nil,
	}}
}

func newOpenAIResponse(upstream map[string]any) OpenAIResponse {
	instructions := upstream["instructions"]
	reasoning := upstream["reasoning"]
	if reasoning == nil {
		reasoning = map[string]any{"effort": nil, "summary": nil}
	}
	text := upstream["text"]
	if text == nil {
		text = map[string]any{"format": map[string]any{"type": "text"}}
	}
	tools, _ := upstream["tools"].([]any)
	if tools == nil {
		tools = []any{}
	}
	toolChoice := upstream["tool_choice"]
	if toolChoice == nil {
		toolChoice = "auto"
	}
	metadata, _ := upstream["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
	}
	parallelToolCalls := true
	if value, ok := upstream["parallel_tool_calls"].(bool); ok {
		parallelToolCalls = value
	}

	return OpenAIResponse{
		ID:                 "resp_" + randomHex(16),
		Object:             "response",
		CreatedAt:          time.Now().Unix(),
		Status:             "completed",
		Instructions:       instructions,
		MaxOutputTokens:    upstream["max_output_tokens"],
		Model:              stringValue(upstream, "model"),
		Output:             []any{},
		ParallelToolCalls:  parallelToolCalls,
		PreviousResponseID: upstream["previous_response_id"],
		Reasoning:          reasoning,
		Store:              false,
		Temperature:        1,
		Text:               text,
		ToolChoice:         toolChoice,
		Tools:              tools,
		TopP:               1,
		Truncation:         "disabled",
		Usage:              openAIResponseUsage(nil),
		User:               upstream["user"],
		Metadata:           metadata,
	}
}

func openAIOutputItem(item map[string]any) map[string]any {
	switch stringValue(item, "type") {
	case "message":
		return openAIMessageItem(item)
	case "function_call":
		return openAIFunctionCallItem(item)
	default:
		return item
	}
}

func openAIMessageItem(item map[string]any) map[string]any {
	content := []any{}
	if parts, ok := item["content"].([]any); ok {
		for _, part := range parts {
			m, ok := part.(map[string]any)
			if !ok || stringValue(m, "type") != "output_text" {
				continue
			}
			annotations, _ := m["annotations"].([]any)
			if annotations == nil {
				annotations = []any{}
			}
			logprobs, _ := m["logprobs"].([]any)
			if logprobs == nil {
				logprobs = []any{}
			}
			content = append(content, map[string]any{
				"type":        "output_text",
				"text":        stringValue(m, "text"),
				"annotations": annotations,
				"logprobs":    logprobs,
			})
		}
	}
	return map[string]any{
		"id":      defaultedString(item, "id", "msg_"+randomHex(16)),
		"type":    "message",
		"status":  defaultedString(item, "status", "completed"),
		"role":    defaultedString(item, "role", "assistant"),
		"content": content,
	}
}

func openAIFunctionCallItem(item map[string]any) map[string]any {
	callID := stringValue(item, "call_id")
	if callID == "" {
		callID = defaultedString(item, "id", "call_"+randomHex(8))
	}
	arguments := stringValue(item, "arguments")
	if arguments == "" {
		arguments = "{}"
	}
	return map[string]any{
		"id":        defaultedString(item, "id", "fc_"+randomHex(16)),
		"type":      "function_call",
		"status":    defaultedString(item, "status", "completed"),
		"call_id":   callID,
		"name":      stringValue(item, "name"),
		"arguments": arguments,
	}
}

func synthesizedMessageItem(text string) map[string]any {
	return map[string]any{
		"id":     "msg_" + randomHex(16),
		"type":   "message",
		"status": "completed",
		"role":   "assistant",
		"content": []any{map[string]any{
			"type":        "output_text",
			"text":        text,
			"annotations": []any{},
			"logprobs":    []any{},
		}},
	}
}

func ChatCompletionFromAggregate(agg OpenAIResponse, model string) map[string]any {
	toolCalls := chatToolCallsFromOutput(agg.Output)
	finishReason := "stop"
	message := map[string]any{
		"role":        "assistant",
		"content":     agg.OutputText,
		"refusal":     nil,
		"annotations": []any{},
	}
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
		message["tool_calls"] = toolCalls
		if agg.OutputText == "" {
			message["content"] = nil
		}
	}
	return map[string]any{
		"id":           "chatcmpl-" + strings.TrimPrefix(agg.ID, "resp_"),
		"object":       "chat.completion",
		"created":      agg.CreatedAt,
		"model":        model,
		"service_tier": "default",
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"logprobs":      nil,
			"finish_reason": finishReason,
		}},
		"usage": chatUsageFromResponsesUsage(agg.Usage),
	}
}

func openAIChatCompletionChunk(id, model string, created int64, choices []any, usage any) map[string]any {
	chunk := map[string]any{
		"id":           id,
		"object":       "chat.completion.chunk",
		"created":      created,
		"model":        model,
		"service_tier": "default",
		"choices":      choices,
	}
	if usage != nil {
		chunk["usage"] = usage
	}
	return chunk
}

func openAIChatDeltaChoice(delta map[string]any, finishReason any) map[string]any {
	return map[string]any{
		"index":         0,
		"delta":         delta,
		"logprobs":      nil,
		"finish_reason": finishReason,
	}
}

func openAIChatToolCallDelta(index int, callID, name, arguments string) map[string]any {
	return map[string]any{
		"tool_calls": []any{map[string]any{
			"index": index,
			"id":    callID,
			"type":  "function",
			"function": map[string]any{
				"name":      name,
				"arguments": arguments,
			},
		}},
	}
}

func chatToolCallsFromOutput(output []any) []any {
	var toolCalls []any
	for _, item := range output {
		m, ok := item.(map[string]any)
		if !ok || stringValue(m, "type") != "function_call" {
			continue
		}
		callID := stringValue(m, "call_id")
		if callID == "" {
			callID = stringValue(m, "id")
		}
		if callID == "" {
			callID = "call_" + randomHex(8)
		}
		arguments := stringValue(m, "arguments")
		if arguments == "" {
			arguments = "{}"
		}
		toolCalls = append(toolCalls, map[string]any{
			"id":   callID,
			"type": "function",
			"function": map[string]any{
				"name":      stringValue(m, "name"),
				"arguments": arguments,
			},
		})
	}
	return toolCalls
}

func outputTextFromItems(output []any) string {
	var text strings.Builder
	for _, item := range output {
		m, ok := item.(map[string]any)
		if !ok || stringValue(m, "type") != "message" {
			continue
		}
		content, _ := m["content"].([]any)
		for _, part := range content {
			pm, ok := part.(map[string]any)
			if ok && stringValue(pm, "type") == "output_text" {
				text.WriteString(stringValue(pm, "text"))
			}
		}
	}
	return text.String()
}

func openAIResponseUsage(usage any) any {
	m, _ := usage.(map[string]any)
	inputDetails, _ := m["input_tokens_details"].(map[string]any)
	outputDetails, _ := m["output_tokens_details"].(map[string]any)
	return map[string]any{
		"input_tokens":  numberOrZero(m["input_tokens"]),
		"output_tokens": numberOrZero(m["output_tokens"]),
		"total_tokens":  numberOrZero(m["total_tokens"]),
		"input_tokens_details": map[string]any{
			"cached_tokens": numberOrZero(inputDetails["cached_tokens"]),
		},
		"output_tokens_details": map[string]any{
			"reasoning_tokens": numberOrZero(outputDetails["reasoning_tokens"]),
		},
	}
}

func chatUsageFromResponsesUsage(usage any) any {
	m, _ := usage.(map[string]any)
	promptDetails, _ := m["input_tokens_details"].(map[string]any)
	completionDetails, _ := m["output_tokens_details"].(map[string]any)
	return map[string]any{
		"prompt_tokens":     numberOrZero(m["input_tokens"]),
		"completion_tokens": numberOrZero(m["output_tokens"]),
		"total_tokens":      numberOrZero(m["total_tokens"]),
		"prompt_tokens_details": map[string]any{
			"cached_tokens": numberOrZero(promptDetails["cached_tokens"]),
			"audio_tokens":  0,
		},
		"completion_tokens_details": map[string]any{
			"reasoning_tokens":           numberOrZero(completionDetails["reasoning_tokens"]),
			"audio_tokens":               0,
			"accepted_prediction_tokens": 0,
			"rejected_prediction_tokens": 0,
		},
	}
}

func numberOrZero(v any) any {
	if v == nil {
		return 0
	}
	return v
}

func randomHex(bytesLen int) string {
	b := make([]byte, bytesLen)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
