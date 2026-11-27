package protocol

import (
	"bytes"
	"encoding/json"
)

type tokenDetails struct {
	Cached    *int64 `json:"cached_tokens"`
	Written   *int64 `json:"cache_write_tokens"`
	Creation  *int64 `json:"cache_creation_tokens"`
	Reasoning *int64 `json:"reasoning_tokens"`
}

func count(n *int64) int64 {
	if n == nil {
		return 0
	}

	return max(0, *n)
}

// A nil subset means the provider didn't report it. An explicit zero is significant.
func subset(n *int64, total int64) *int64 {
	if n == nil {
		return nil
	}

	v := min(count(n), total)

	return &v
}

func (u *Usage) UnmarshalJSON(b []byte) error {
	v, err := decodeUsage(OpenAIChat, b, nil)
	if err == nil && v != nil {
		*u = *v
	}

	return err
}

// Anthropic updates individual cumulative counters in message_delta. Retain each
// missing counter, including the uncached input from the initial message_start.
func decodeUsage(wire ID, raw json.RawMessage, prior *Usage) (*Usage, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return prior, nil
	}
	var v struct {
		Input             *int64       `json:"input_tokens"`
		Output            *int64       `json:"output_tokens"`
		Prompt            *int64       `json:"prompt_tokens"`
		Completion        *int64       `json:"completion_tokens"`
		Total             *int64       `json:"total_tokens"`
		CacheRead         *int64       `json:"cache_read_input_tokens"`
		CacheWrite        *int64       `json:"cache_creation_input_tokens"`
		InputDetails      tokenDetails `json:"input_tokens_details"`
		OutputDetails     tokenDetails `json:"output_tokens_details"`
		PromptDetails     tokenDetails `json:"prompt_tokens_details"`
		CompletionDetails tokenDetails `json:"completion_tokens_details"`
		FlatRead          *int64       `json:"cache_read_tokens"`
		FlatWrite         *int64       `json:"cache_creation_tokens"`
		FlatReasoning     *int64       `json:"reasoning_tokens"`
		Cost              *float64     `json:"cost"`
	}

	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}

	u := Usage{Cost: v.Cost}

	switch wire {
	case AnthropicMessages:
		if prior != nil {
			u = *prior
		}

		uncached := u.Prompt - count(u.CacheRead) - count(u.CacheWrite)
		if v.Input != nil {
			uncached = count(v.Input)
		}

		if v.Output != nil {
			u.Completion = count(v.Output)
		}

		if v.CacheRead != nil {
			u.CacheRead = v.CacheRead
		}

		if v.CacheWrite != nil {
			u.CacheWrite = v.CacheWrite
		}

		u.Prompt = uncached + count(u.CacheRead) + count(u.CacheWrite)
		u.Total = u.Prompt + u.Completion
	default:
		input, output, details, outDetails := v.Input, v.Output, v.InputDetails, v.OutputDetails
		if wire == OpenAIChat {
			input, output, details, outDetails = v.Prompt, v.Completion, v.PromptDetails, v.CompletionDetails
		}

		u.Prompt, u.Completion = count(input), count(output)
		u.CacheRead, u.CacheWrite, u.Reasoning = v.FlatRead, v.FlatWrite, v.FlatReasoning

		if details.Cached != nil {
			u.CacheRead = details.Cached
		}

		if details.Creation != nil {
			u.CacheWrite = details.Creation
		}

		if details.Written != nil {
			u.CacheWrite = details.Written
		}

		if outDetails.Reasoning != nil {
			u.Reasoning = outDetails.Reasoning
		}

		if input == nil && output == nil {
			// Some non-chat endpoints report only a total; don't invent its split.
			u.Total = count(v.Total)
		}
	}

	u.CacheRead = subset(u.CacheRead, u.Prompt)
	u.CacheWrite = subset(u.CacheWrite, u.Prompt-count(u.CacheRead))
	u.Reasoning = subset(u.Reasoning, u.Completion)

	if u.Total == 0 {
		u.Total = u.Prompt + u.Completion
	}

	return &u, nil
}

func renderUsage(wire ID, u *Usage) map[string]any {
	if u == nil {
		return nil
	}

	if wire == AnthropicMessages {
		m := map[string]any{"input_tokens": max(0, u.Prompt-count(u.CacheRead)-count(u.CacheWrite)), "output_tokens": u.Completion}
		if u.CacheRead != nil {
			m["cache_read_input_tokens"] = *u.CacheRead
		}

		if u.CacheWrite != nil {
			m["cache_creation_input_tokens"] = *u.CacheWrite
		}

		return m
	}

	in, out := "input_tokens", "output_tokens"
	if wire == OpenAIChat {
		in, out = "prompt_tokens", "completion_tokens"
	}

	m := map[string]any{in: u.Prompt, out: u.Completion, "total_tokens": u.Total}

	if u.CacheRead != nil || u.CacheWrite != nil {
		d := map[string]int64{}
		if u.CacheRead != nil {
			d["cached_tokens"] = *u.CacheRead
		}

		if u.CacheWrite != nil {
			d["cache_write_tokens"] = *u.CacheWrite
		}

		m[in+"_details"] = d
	}

	if u.Reasoning != nil {
		m[out+"_details"] = map[string]int64{"reasoning_tokens": *u.Reasoning}
	}

	if u.Cost != nil {
		m["cost"] = *u.Cost
	}

	return m
}
