package service

import "github.com/tidwall/gjson"

func monitorStreamHasOutput(data string) bool {
	value := gjson.Parse(data)
	for _, path := range []string{"delta.text", "delta.thinking", "delta.partial_json", "choices.0.delta.content", "choices.0.delta.reasoning_content", "choices.0.delta.tool_calls.0.function.arguments", "candidates.0.content.parts.0.text"} {
		if value.Get(path).String() != "" {
			return true
		}
	}
	kind := value.Get("type").String()
	return (kind == "response.output_text.delta" || kind == "response.reasoning_summary_text.delta" || kind == "response.reasoning_text.delta" || kind == "response.function_call_arguments.delta") && value.Get("delta").String() != ""
}
