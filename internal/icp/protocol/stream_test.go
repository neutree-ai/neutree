package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
)

// Independent wire fixtures exercise cumulative metering and interleaved tool arguments.
var streamFixtures = map[ID][]string{
	OpenAIChat: {
		`{"id":"up","model":"served","choices":[{"delta":{"reasoning_content":"why"}}]}`,
		`{"choices":[{"delta":{"content":"你"}}]}`,
		`{"choices":[{"delta":{"reasoning":"?"}}]}`,
		`{"choices":[{"delta":{"content":"好"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"a","arguments":"{"}},{"index":1,"id":"call_b","function":{"name":"b","arguments":"{"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"\"v\":1}"}},{"index":0,"function":{"arguments":"}"}}]}}]}`,
		`{"choices":[{"delta":{"content":"!"},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":8,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":5}}}`,
		"[DONE]",
	},
	AnthropicMessages: {
		`{"type":"message_start","message":{"id":"up","model":"served","usage":{"input_tokens":3,"cache_read_input_tokens":5,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"why"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":"你好"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call_a","name":"a","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"call_b","name":"b","input":{}}}`,
		`{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{\"v\":1}"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}`,
		`{"type":"message_stop"}`,
	},
	OpenAIResponses: {
		`{"type":"response.created","response":{"id":"up","model":"served"}}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"why"}`,
		`{"type":"response.output_text.delta","output_index":1,"delta":"你"}`,
		`{"type":"response.output_text.delta","output_index":1,"content_index":1,"delta":"好"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"message"}}`,
		`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_a","call_id":"call_a","name":"a","arguments":"{}"}}`,
		`{"type":"response.output_item.added","output_index":3,"item":{"type":"function_call","id":"fc_b","call_id":"call_b","name":"b"}}`,
		`{"type":"response.function_call_arguments.done","output_index":2,"arguments":"{\"a\":0}"}`,
		`{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","arguments":"{\"a\":0}"}}`,
		`{"type":"response.output_item.done","output_index":3,"item":{"type":"function_call","arguments":"{\"v\":1}"}}`,
		`{"type":"response.completed","response":{"usage":{"input_tokens":8,"input_tokens_details":{"cached_tokens":5},"output_tokens":3}}}`,
	},
}

func TestStreamConversionMatrix(t *testing.T) {
	for src, fixture := range streamFixtures {
		for dst := range streamFixtures {
			if src == dst {
				continue
			}
			t.Run(string(src)+"_to_"+string(dst), func(t *testing.T) {
				d := wireCodec(src).NewStreamDecoder()
				e := wireCodec(dst).NewStreamEncoder(EncodeOpts{IncludeUsage: true})
				var wire bytes.Buffer
				var sequence int
				input := "data: " + strings.Join(fixture, "\r\n\r\ndata: ") + "\r\n\r\n"
				err := ScanSSE(iotest.OneByteReader(strings.NewReader(input)), func(f Frame) error {
					return d.Push(f, func(ev Event) error {
						return e.Push(ev, func(out Frame) error {
							if dst == OpenAIResponses {
								var item struct {
									Sequence *int `json:"sequence_number"`
									Item     struct {
										Type, ID string
										CallID   string `json:"call_id"`
									}
								}
								if err := json.Unmarshal(out.Data, &item); err != nil {
									return err
								}
								if item.Sequence == nil || *item.Sequence != sequence {
									t.Fatalf("%s sequence: %s", out.Name, out.Data)
								}
								sequence++
								if item.Item.Type == "function_call" && (item.Item.ID == item.Item.CallID || !strings.HasPrefix(item.Item.ID, "fc_")) {
									t.Fatal("wire item id", item.Item)
								}
							}
							_, err := wire.Write(out.Raw)
							return err
						})
					})
				})
				if err != nil || d.Finish() != nil {
					t.Fatal(err, d.Finish())
				}
				// Read the actual client bytes, including Data/Raw synchronization and native terminal.
				client := wireCodec(dst).NewStreamDecoder()
				var thinking, text string
				var kinds []string
				calls := map[int]string{}
				args := map[string]string{}
				var end Event
				err = ScanSSE(&wire, func(f Frame) error {
					return client.Push(f, func(ev Event) error {
						switch ev.Kind {
						case evEnd:
							end = ev
						case evBlock:
							if ev.Index == len(kinds) {
								kinds = append(kinds, ev.Block.Kind)
							}
							switch ev.Block.Kind {
							case "thinking":
								thinking += ev.Block.Text
							case "text":
								text += ev.Block.Text
							case "tool_use":
								if ev.Block.ID != "" {
									calls[ev.Index] = ev.Block.ID
								}
								args[calls[ev.Index]] += string(ev.Block.Data)
							}
						}
						return nil
					})
				})
				if err != nil || client.Finish() != nil {
					t.Fatal(err, client.Finish())
				}
				wantText, wantThinking, wantArgs := "你好", "why", "{}"
				if src == OpenAIChat {
					wantText, wantThinking = "你好!", "why?"
					if !slices.Equal(kinds, []string{blockThink, blockText, blockThink, blockText, blockCall, blockCall, blockText}) {
						t.Fatalf("reordered blocks: %v", kinds)
					}
				}
				if src == OpenAIResponses {
					wantArgs = `{"a":0}`
				}
				if end.Model != "served" || end.ID != "up" || end.FinishReason != "tool_calls" || end.Usage == nil || end.Usage.Prompt != 8 || end.Usage.Total != 11 || end.Usage.CacheRead == nil || *end.Usage.CacheRead != 5 || thinking != wantThinking || text != wantText || args["call_a"] != wantArgs || args["call_b"] != `{"v":1}` {
					t.Fatalf("got end=%+v usage=%+v, %s, %s, %v", end, end.Usage, thinking, text, args)
				}
			})
		}
	}
}

func TestStreamRequiresProtocolTerminal(t *testing.T) {
	for id, fixture := range streamFixtures {
		d := wireCodec(id).NewStreamDecoder()
		for _, data := range fixture[:len(fixture)-1] {
			var closed []int
			if err := d.Push(Frame{Data: []byte(data), HasData: true}, func(ev Event) error {
				if ev.Kind == evBlockEnd {
					closed = append(closed, ev.Index)
				}
				if ev.Kind == evEnd {
					t.Fatal(id, "early terminal")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(data, `"item":{"type":"message"}`) && !slices.Equal(closed, []int{1, 2}) {
				t.Fatalf("completed message did not close its two parts: %v", closed)
			}
		}
		if !errors.Is(d.Finish(), io.ErrUnexpectedEOF) {
			t.Fatal(id, "accepted truncated stream")
		}
	}
	for src, payload := range map[ID]string{
		OpenAIChat: `{"error":{"message":"denied"}}`, AnthropicMessages: `{"type":"error","error":{"message":"denied"}}`,
		OpenAIResponses: `{"type":"response.failed","response":{"error":{"message":"denied"}}}`,
	} {
		for dst := range streamFixtures {
			if src == dst {
				continue
			}
			d, e := wireCodec(src).NewStreamDecoder(), wireCodec(dst).NewStreamEncoder(EncodeOpts{})
			var frames [][]byte
			err := d.Push(Frame{Data: []byte(payload), HasData: true}, func(ev Event) error {
				return e.Push(ev, func(f Frame) error { frames = append(frames, f.Raw); return nil })
			})
			if err != nil || len(frames) != 1 || !slices.Equal(frames[0], ErrorFrame(dst, 0, "denied")) {
				t.Fatalf("%s -> %s: %s, %v", src, dst, frames, err)
			}
		}
	}
}

func TestForeignPrivateThinkingIsOmitted(t *testing.T) {
	for _, tc := range []struct {
		src, dst               ID
		block, end, unexpected string
	}{
		{OpenAIResponses, AnthropicMessages, `{"type":"response.output_item.done","item":{"type":"reasoning","encrypted_content":"private"}}`, `{"type":"response.completed"}`, "thinking"},
		{AnthropicMessages, OpenAIResponses, `{"type":"content_block_start","content_block":{"type":"redacted_thinking","data":"private"}}`, `{"type":"message_stop"}`, "response.output_item.added"},
	} {
		dec := wireCodec(tc.src).NewStreamDecoder()
		enc := wireCodec(tc.dst).NewStreamEncoder(EncodeOpts{})
		var out bytes.Buffer
		var names []string
		for _, data := range []string{tc.block, tc.end} {
			err := dec.Push(Frame{HasData: true, Data: []byte(data)}, func(e Event) error {
				return enc.Push(e, func(f Frame) error {
					names = append(names, f.Name)
					_, err := out.Write(f.Raw)
					return err
				})
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		if tc.dst == AnthropicMessages && (!slices.Equal(names, []string{"message_start", "content_block_start", "content_block_stop", "message_delta", "message_stop"}) || !strings.Contains(out.String(), `"content_block":{"text":"","type":"text"}`)) {
			t.Fatal(out.String())
		}
		if out.Len() == 0 || strings.Contains(out.String(), tc.unexpected) || strings.Contains(out.String(), "private") {
			t.Fatal(out.String())
		}
	}
}
