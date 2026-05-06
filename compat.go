package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const defaultInstructions = "You are a helpful assistant."

func NormalizeResponsesRequest(raw map[string]any) (map[string]any, bool, error) {
	model := stringValue(raw, "model")
	if model == "" {
		return nil, false, errors.New("missing required field: model")
	}
	input, ok := raw["input"]
	if !ok {
		return nil, false, errors.New("missing required field: input")
	}
	normalizedInput, err := normalizeResponsesInput(input)
	if err != nil {
		return nil, false, err
	}

	stream := boolValue(raw, "stream")
	out := map[string]any{
		"model":        model,
		"input":        normalizedInput,
		"instructions": defaultedString(raw, "instructions", defaultInstructions),
		"store":        false,
		"stream":       true,
	}
	for _, key := range []string{"reasoning", "tools", "text", "tool_choice", "parallel_tool_calls"} {
		if v, ok := raw[key]; ok {
			out[key] = v
		}
	}
	return out, stream, nil
}

func normalizeResponsesInput(input any) ([]any, error) {
	switch v := input.(type) {
	case string:
		return []any{map[string]any{"role": "user", "content": v}}, nil
	case []any:
		return v, nil
	case map[string]any:
		return []any{v}, nil
	default:
		return nil, fmt.Errorf("input must be a string, object, or array")
	}
}

func BuildResponsesRequestFromChat(raw map[string]any) (map[string]any, bool, error) {
	model := stringValue(raw, "model")
	if model == "" {
		return nil, false, errors.New("missing required field: model")
	}
	messages, ok := raw["messages"].([]any)
	if !ok || len(messages) == 0 {
		return nil, false, errors.New("missing required field: messages")
	}

	var instructions []string
	input := make([]any, 0, len(messages))
	for _, item := range messages {
		message, ok := item.(map[string]any)
		if !ok {
			return nil, false, errors.New("messages must contain objects")
		}
		role := stringValue(message, "role")
		switch role {
		case "system", "developer":
			if text := chatContentText(message["content"]); text != "" {
				instructions = append(instructions, text)
			}
		case "user":
			input = append(input, map[string]any{"role": "user", "content": chatContentText(message["content"])})
		case "assistant":
			if text := chatContentText(message["content"]); text != "" {
				input = append(input, map[string]any{"role": "assistant", "content": text})
			}
			if toolCalls, ok := message["tool_calls"].([]any); ok {
				for _, toolCall := range toolCalls {
					if fc := responsesFunctionCallFromChat(toolCall); fc != nil {
						input = append(input, fc)
					}
				}
			}
		case "tool":
			callID := stringValue(message, "tool_call_id")
			if callID == "" {
				return nil, false, errors.New("tool message is missing tool_call_id")
			}
			input = append(input, map[string]any{
				"type":    "function_call_output",
				"call_id": callID,
				"output":  chatContentText(message["content"]),
			})
		default:
			return nil, false, fmt.Errorf("unsupported message role %q", role)
		}
	}
	if len(input) == 0 {
		input = append(input, map[string]any{"role": "user", "content": ""})
	}

	out := map[string]any{
		"model":        model,
		"input":        input,
		"instructions": defaultInstructions,
		"store":        false,
		"stream":       true,
	}
	if len(instructions) > 0 {
		out["instructions"] = strings.Join(instructions, "\n\n")
	}
	if tools := responsesToolsFromChat(raw["tools"]); len(tools) > 0 {
		out["tools"] = tools
	}
	if toolChoice, ok, err := responsesToolChoiceFromChat(raw["tool_choice"]); err != nil {
		return nil, false, err
	} else if ok {
		out["tool_choice"] = toolChoice
	}
	if text, ok, err := responsesTextFromChatResponseFormat(raw["response_format"]); err != nil {
		return nil, false, err
	} else if ok {
		out["text"] = text
	}
	if parallel, ok := raw["parallel_tool_calls"].(bool); ok {
		out["parallel_tool_calls"] = parallel
	}
	if reasoning, ok := raw["reasoning"].(map[string]any); ok {
		out["reasoning"] = reasoning
	} else if effort := stringValue(raw, "reasoning_effort"); effort != "" {
		out["reasoning"] = map[string]any{"effort": effort}
	}

	return out, boolValue(raw, "stream"), nil
}

func AggregateResponsesStream(r io.Reader, upstream map[string]any) (OpenAIResponse, error) {
	agg := newOpenAIResponse(upstream)

	err := ReadStreamEvents(r, func(event StreamEvent) error {
		switch event.Type {
		case "response.output_text.delta":
			agg.OutputText += stringValue(event.Data, "delta")
		case "response.output_item.done":
			if item, ok := event.Data["item"].(map[string]any); ok {
				agg.Output = append(agg.Output, openAIOutputItem(item))
			}
		case "response.completed":
			if response, ok := event.Data["response"].(map[string]any); ok {
				if id := stringValue(response, "id"); id != "" {
					agg.ID = id
				}
				if status := stringValue(response, "status"); status != "" {
					agg.Status = status
				}
				if model := stringValue(response, "model"); model != "" {
					agg.Model = model
				}
				if usage, ok := response["usage"]; ok {
					agg.Usage = openAIResponseUsage(usage)
				}
			}
		case "response.failed", "response.incomplete":
			agg.Status = strings.TrimPrefix(event.Type, "response.")
		}
		return nil
	})
	if err != nil {
		return OpenAIResponse{}, err
	}
	if len(agg.Output) == 0 && agg.OutputText != "" {
		agg.Output = []any{synthesizedMessageItem(agg.OutputText)}
	}
	if agg.OutputText == "" {
		agg.OutputText = outputTextFromItems(agg.Output)
	}
	return agg, nil
}

func chatContentText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case nil:
		return ""
	case []any:
		var parts []string
		for _, item := range v {
			part, ok := item.(map[string]any)
			if ok && stringValue(part, "type") == "text" {
				parts = append(parts, stringValue(part, "text"))
			}
		}
		return strings.Join(parts, "")
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func responsesFunctionCallFromChat(toolCall any) map[string]any {
	m, ok := toolCall.(map[string]any)
	if !ok || stringValue(m, "type") != "function" {
		return nil
	}
	fn, ok := m["function"].(map[string]any)
	if !ok {
		return nil
	}
	name := stringValue(fn, "name")
	if name == "" {
		return nil
	}
	callID := stringValue(m, "id")
	if callID == "" {
		callID = "call_" + randomHex(8)
	}
	arguments := stringValue(fn, "arguments")
	if arguments == "" {
		arguments = "{}"
	}
	return map[string]any{
		"type":      "function_call",
		"call_id":   callID,
		"name":      name,
		"arguments": arguments,
	}
}

func responsesToolChoiceFromChat(value any) (any, bool, error) {
	if value == nil {
		return nil, false, nil
	}
	switch v := value.(type) {
	case string:
		switch v {
		case "auto", "none", "required":
			return v, true, nil
		default:
			return nil, false, fmt.Errorf("unsupported tool_choice %q", v)
		}
	case map[string]any:
		if stringValue(v, "type") != "function" {
			return nil, false, fmt.Errorf("unsupported tool_choice type %q", stringValue(v, "type"))
		}
		name := stringValue(v, "name")
		if name == "" {
			fn, _ := v["function"].(map[string]any)
			name = stringValue(fn, "name")
		}
		if name == "" {
			return nil, false, errors.New("function tool_choice is missing a function name")
		}
		return map[string]any{"type": "function", "name": name}, true, nil
	default:
		return nil, false, errors.New("tool_choice must be a string or object")
	}
}

func responsesTextFromChatResponseFormat(value any) (map[string]any, bool, error) {
	if value == nil {
		return nil, false, nil
	}
	m, ok := value.(map[string]any)
	if !ok {
		return nil, false, errors.New("response_format must be an object")
	}

	switch typ := stringValue(m, "type"); typ {
	case "text", "json_object":
		return map[string]any{"format": map[string]any{"type": typ}}, true, nil
	case "json_schema":
		jsonSchema, _ := m["json_schema"].(map[string]any)
		format := map[string]any{"type": "json_schema"}
		for _, key := range []string{"name", "description", "schema", "strict"} {
			if v, ok := jsonSchema[key]; ok {
				format[key] = v
			}
		}
		if stringValue(format, "name") == "" {
			return nil, false, errors.New("response_format.json_schema is missing name")
		}
		if _, ok := format["schema"]; !ok {
			return nil, false, errors.New("response_format.json_schema is missing schema")
		}
		return map[string]any{"format": format}, true, nil
	default:
		return nil, false, fmt.Errorf("unsupported response_format type %q", typ)
	}
}

func responsesToolsFromChat(value any) []any {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	tools := make([]any, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok || stringValue(m, "type") != "function" {
			continue
		}
		fn, ok := m["function"].(map[string]any)
		if !ok || stringValue(fn, "name") == "" {
			continue
		}
		tool := map[string]any{
			"type":        "function",
			"name":        stringValue(fn, "name"),
			"description": stringValue(fn, "description"),
			"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
		}
		if params, ok := fn["parameters"]; ok {
			tool["parameters"] = params
		}
		if strict, ok := fn["strict"].(bool); ok {
			tool["strict"] = strict
		}
		tools = append(tools, tool)
	}
	return tools
}

func stringValue(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch s := v.(type) {
	case string:
		return s
	default:
		return fmt.Sprint(s)
	}
}

func defaultedString(m map[string]any, key, fallback string) string {
	if value := strings.TrimSpace(stringValue(m, key)); value != "" {
		return value
	}
	return fallback
}

func boolValue(m map[string]any, key string) bool {
	v, ok := m[key]
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}
