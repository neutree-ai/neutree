local cjson = require("cjson.safe")

local AIFormatAnthropicHandler = {
    PRIORITY = 900,
    VERSION = "0.0.1",
}

local EMPTY = {}

-- ============================================================
-- Helper: build an SSE frame
-- ============================================================
local function sse_frame(event_type, data)
    if type(data) == "table" then
        data = cjson.encode(data)
    end
    if event_type then
        return "event: " .. event_type .. "\ndata: " .. data .. "\n\n"
    end
    return "data: " .. data .. "\n\n"
end

-- ============================================================
-- Helper: Anthropic error response
-- ============================================================
local function anthropic_error(status, err_type, message)
    return kong.response.exit(status, {
        type = "error",
        error = {
            type = err_type,
            message = message,
        },
    })
end

-- ============================================================
-- Request conversion: Anthropic -> OpenAI
-- ============================================================

local function convert_tool_choice(tc)
    if tc == nil then
        return nil
    end
    if type(tc) == "string" then
        if tc == "auto" then return "auto" end
        if tc == "any" then return "required" end
        if tc == "none" then return "none" end
        return tc
    end
    if type(tc) == "table" then
        if tc.type == "auto" then return "auto" end
        if tc.type == "any" then return "required" end
        if tc.type == "tool" then
            return { type = "function", ["function"] = { name = tc.name } }
        end
    end
    return nil
end

local function convert_tools(tools)
    if tools == nil then return nil end
    local result = {}
    for i, tool in ipairs(tools) do
        result[i] = {
            type = "function",
            ["function"] = {
                name = tool.name,
                description = tool.description,
                parameters = tool.input_schema,
            },
        }
    end
    return result
end

local function convert_messages(anthropic_messages)
    local openai_messages = {}

    for _, msg in ipairs(anthropic_messages) do
        if msg.role == "user" then
            if type(msg.content) == "string" then
                openai_messages[#openai_messages + 1] = {
                    role = "user",
                    content = msg.content,
                }
            elseif type(msg.content) == "table" then
                local user_parts = {}
                local tool_results = {}
                local trailing_user_parts = {}

                -- separate tool_result blocks from other content
                local found_tool_result = false
                for _, block in ipairs(msg.content) do
                    if block.type == "tool_result" then
                        found_tool_result = true
                        local tool_content = ""
                        if type(block.content) == "string" then
                            tool_content = block.content
                        elseif type(block.content) == "table" then
                            -- array of content blocks
                            local parts = {}
                            for _, cb in ipairs(block.content) do
                                if cb.type == "text" then
                                    parts[#parts + 1] = cb.text or ""
                                end
                            end
                            tool_content = table.concat(parts, "\n")
                        end

                        if block.is_error then
                            tool_content = "Error: " .. tool_content
                        end

                        tool_results[#tool_results + 1] = {
                            role = "tool",
                            tool_call_id = block.tool_use_id,
                            content = tool_content,
                        }
                    elseif found_tool_result then
                        -- content after tool_results -> trailing user message
                        if block.type == "text" then
                            trailing_user_parts[#trailing_user_parts + 1] = {
                                type = "text",
                                text = block.text,
                            }
                        elseif block.type == "image" then
                            local url = "data:" .. (block.source.media_type or "image/png") .. ";base64," .. block.source.data
                            trailing_user_parts[#trailing_user_parts + 1] = {
                                type = "image_url",
                                image_url = { url = url },
                            }
                        end
                    else
                        if block.type == "text" then
                            user_parts[#user_parts + 1] = {
                                type = "text",
                                text = block.text,
                            }
                        elseif block.type == "image" then
                            local url = "data:" .. (block.source.media_type or "image/png") .. ";base64," .. block.source.data
                            user_parts[#user_parts + 1] = {
                                type = "image_url",
                                image_url = { url = url },
                            }
                        end
                    end
                end

                -- emit user content before tool_results
                if #user_parts > 0 then
                    openai_messages[#openai_messages + 1] = {
                        role = "user",
                        content = user_parts,
                    }
                end

                -- emit tool messages
                for _, tr in ipairs(tool_results) do
                    openai_messages[#openai_messages + 1] = tr
                end

                -- emit trailing user content after tool_results
                if #trailing_user_parts > 0 then
                    if #trailing_user_parts == 1 and trailing_user_parts[1].type == "text" then
                        openai_messages[#openai_messages + 1] = {
                            role = "user",
                            content = trailing_user_parts[1].text,
                        }
                    else
                        openai_messages[#openai_messages + 1] = {
                            role = "user",
                            content = trailing_user_parts,
                        }
                    end
                end

                -- if only had tool_results with no other content, already emitted
                if #user_parts == 0 and #tool_results == 0 and #trailing_user_parts == 0 then
                    -- fallback: just pass as-is
                    openai_messages[#openai_messages + 1] = {
                        role = "user",
                        content = msg.content,
                    }
                end
            end

        elseif msg.role == "assistant" then
            if type(msg.content) == "string" then
                openai_messages[#openai_messages + 1] = {
                    role = "assistant",
                    content = msg.content,
                }
            elseif type(msg.content) == "table" then
                local text_parts = {}
                local tool_calls = {}

                for _, block in ipairs(msg.content) do
                    if block.type == "text" then
                        text_parts[#text_parts + 1] = block.text
                    elseif block.type == "tool_use" then
                        local args_str = ""
                        if block.input then
                            args_str = cjson.encode(block.input)
                        end
                        tool_calls[#tool_calls + 1] = {
                            id = block.id,
                            type = "function",
                            ["function"] = {
                                name = block.name,
                                arguments = args_str,
                            },
                        }
                    end
                    -- type == "thinking" -> skip
                end

                local assistant_msg = {
                    role = "assistant",
                }

                local combined_text = table.concat(text_parts, "\n\n")
                if combined_text ~= "" then
                    assistant_msg.content = combined_text
                end

                if #tool_calls > 0 then
                    assistant_msg.tool_calls = tool_calls
                end

                openai_messages[#openai_messages + 1] = assistant_msg
            end
        else
            -- pass through other roles (e.g. system)
            openai_messages[#openai_messages + 1] = msg
        end
    end

    return openai_messages
end

local function convert_request(anthropic_req)
    local openai_req = {
        model = anthropic_req.model,
        max_tokens = anthropic_req.max_tokens,
        stream = anthropic_req.stream,
        temperature = anthropic_req.temperature,
        top_p = anthropic_req.top_p,
    }

    -- system message
    local messages = {}
    if anthropic_req.system then
        if type(anthropic_req.system) == "string" then
            messages[#messages + 1] = { role = "system", content = anthropic_req.system }
        elseif type(anthropic_req.system) == "table" then
            local parts = {}
            for _, block in ipairs(anthropic_req.system) do
                if type(block) == "table" and block.text then
                    parts[#parts + 1] = block.text
                elseif type(block) == "string" then
                    parts[#parts + 1] = block
                end
            end
            messages[#messages + 1] = { role = "system", content = table.concat(parts, "\n\n") }
        end
    end

    -- convert messages
    local converted = convert_messages(anthropic_req.messages or {})
    for _, m in ipairs(converted) do
        messages[#messages + 1] = m
    end
    openai_req.messages = messages

    -- stop_sequences -> stop
    if anthropic_req.stop_sequences then
        openai_req.stop = anthropic_req.stop_sequences
    end

    -- stream_options
    if anthropic_req.stream then
        openai_req.stream_options = { include_usage = true }
    end

    -- tools
    if anthropic_req.tools then
        openai_req.tools = convert_tools(anthropic_req.tools)
    end

    -- tool_choice
    if anthropic_req.tool_choice then
        openai_req.tool_choice = convert_tool_choice(anthropic_req.tool_choice)
    end

    return openai_req
end

-- ============================================================
-- Response conversion: OpenAI -> Anthropic (non-streaming)
-- ============================================================

local function map_finish_reason(reason)
    if reason == "stop" then return "end_turn" end
    if reason == "length" then return "max_tokens" end
    if reason == "tool_calls" then return "tool_use" end
    return "end_turn"
end

local function convert_response(openai_resp, request_model)
    local choice = ((openai_resp.choices or EMPTY)[1]) or EMPTY
    local message = choice.message or EMPTY

    local content = {}

    -- text content
    if message.content and message.content ~= "" and message.content ~= cjson.null then
        content[#content + 1] = {
            type = "text",
            text = message.content,
        }
    end

    -- tool calls
    if message.tool_calls then
        for _, tc in ipairs(message.tool_calls) do
            local input = {}
            if tc["function"] and tc["function"].arguments then
                local parsed, err = cjson.decode(tc["function"].arguments)
                if err or parsed == nil then
                    input = { raw = tc["function"].arguments }
                else
                    input = parsed
                end
            end
            content[#content + 1] = {
                type = "tool_use",
                id = tc.id,
                name = tc["function"] and tc["function"].name or "",
                input = input,
            }
        end
    end

    -- if no content at all, add empty text block
    if #content == 0 then
        content[#content + 1] = {
            type = "text",
            text = "",
        }
    end

    local usage = openai_resp.usage or EMPTY
    local anthropic_resp = {
        id = openai_resp.id or ("msg_" .. ngx.now()),
        type = "message",
        role = "assistant",
        model = request_model or openai_resp.model or "unknown",
        content = content,
        stop_reason = map_finish_reason(choice.finish_reason),
        stop_sequence = cjson.null,
        usage = {
            input_tokens = usage.prompt_tokens or 0,
            output_tokens = usage.completion_tokens or 0,
            cache_creation_input_tokens = 0,
            cache_read_input_tokens = 0,
        },
    }

    return anthropic_resp
end

-- ============================================================
-- Streaming conversion helpers
-- ============================================================

local function make_message_start(request_model, input_tokens)
    return {
        type = "message_start",
        message = {
            id = "msg_" .. ngx.now(),
            type = "message",
            role = "assistant",
            model = request_model or "unknown",
            content = {},
            stop_reason = cjson.null,
            stop_sequence = cjson.null,
            usage = {
                input_tokens = input_tokens or 0,
                output_tokens = 0,
                cache_creation_input_tokens = 0,
                cache_read_input_tokens = 0,
            },
        },
    }
end

local function make_content_block_start_text(index)
    return {
        type = "content_block_start",
        index = index,
        content_block = {
            type = "text",
            text = "",
        },
    }
end

local function make_content_block_start_tool_use(index, id, name)
    return {
        type = "content_block_start",
        index = index,
        content_block = {
            type = "tool_use",
            id = id,
            name = name,
            input = {},
        },
    }
end

local function make_content_block_delta_text(index, text)
    return {
        type = "content_block_delta",
        index = index,
        delta = {
            type = "text_delta",
            text = text,
        },
    }
end

local function make_content_block_delta_json(index, partial_json)
    return {
        type = "content_block_delta",
        index = index,
        delta = {
            type = "input_json_delta",
            partial_json = partial_json,
        },
    }
end

local function make_content_block_stop(index)
    return {
        type = "content_block_stop",
        index = index,
    }
end

local function make_ping()
    return { type = "ping" }
end

local function make_message_delta(stop_reason, output_tokens)
    return {
        type = "message_delta",
        delta = {
            stop_reason = stop_reason or "end_turn",
            stop_sequence = cjson.null,
        },
        usage = {
            output_tokens = output_tokens or 0,
        },
    }
end

local function make_message_stop()
    return { type = "message_stop" }
end

-- ============================================================
-- Phase: access
-- ============================================================
function AIFormatAnthropicHandler:access(conf)
    local request_path = kong.request.get_path()

    -- only activate for /anthropic/v1/messages path
    if not string.match(request_path, "/anthropic/v1/messages$") then
        kong.ctx.plugin.skip = true
        return
    end

    local content_type = kong.request.get_header("Content-Type") or "application/json"
    if not string.find(content_type, "application/json", nil, true) then
        return anthropic_error(400, "invalid_request_error", "Content-Type must be application/json")
    end

    local request_body = kong.request.get_raw_body()
    if request_body == nil or request_body == "" then
        return anthropic_error(400, "invalid_request_error", "Request body is required")
    end

    local anthropic_req, err = cjson.decode(request_body)
    if err or anthropic_req == nil then
        return anthropic_error(400, "invalid_request_error", "Invalid JSON in request body")
    end

    -- store original request info
    kong.ctx.plugin.request_model = anthropic_req.model
    kong.ctx.plugin.is_stream = anthropic_req.stream == true

    -- convert request
    local openai_req = convert_request(anthropic_req)
    local openai_json = cjson.encode(openai_req)

    -- rewrite upstream request
    kong.service.request.set_raw_body(openai_json)
    kong.service.request.set_header("Content-Type", "application/json")

    -- rewrite path: replace /anthropic/v1/messages with /v1/chat/completions
    if kong.ctx.shared.model_router_upstream_base_path then
        local base = kong.ctx.shared.model_router_upstream_base_path
        if base == "/" then base = "" end
        kong.service.request.set_path(base .. "/v1/chat/completions")
    else
        local new_path = string.gsub(request_path, "/anthropic/v1/messages$", "/v1/chat/completions")
        kong.service.request.set_path(new_path)
    end

    -- remove Anthropic-specific headers that upstream doesn't need
    kong.service.request.clear_header("anthropic-version")
    kong.service.request.clear_header("anthropic-beta")

    -- for non-streaming, enable response buffering
    if not kong.ctx.plugin.is_stream then
        kong.service.request.enable_buffering()
    end

    -- initialize streaming state
    if kong.ctx.plugin.is_stream then
        kong.ctx.plugin.message_started = false
        kong.ctx.plugin.text_block_open = false
        kong.ctx.plugin.ping_sent = false
        kong.ctx.plugin.current_tool_index = nil
        kong.ctx.plugin.anthropic_block_index = 0
        kong.ctx.plugin.sse_buffer = ""
        kong.ctx.plugin.input_tokens = 0
        kong.ctx.plugin.output_tokens = 0
        kong.ctx.plugin.response_model = nil
        kong.ctx.plugin.finish_events_sent = false
    end
end

-- ============================================================
-- Phase: header_filter
-- ============================================================
function AIFormatAnthropicHandler:header_filter(conf)
    if kong.ctx.plugin.skip then
        return
    end

    local response_status = kong.service.response.get_status()
    if response_status ~= 200 then
        -- pass through error responses without conversion
        return
    end

    if kong.ctx.plugin.is_stream then
        kong.response.set_header("Content-Type", "text/event-stream")
        kong.response.clear_header("Content-Length")
    else
        -- clear Content-Length since response size will change after conversion
        kong.response.clear_header("Content-Length")
        kong.response.set_header("Content-Type", "application/json")
    end
end

-- ============================================================
-- Phase: body_filter (non-streaming)
-- ============================================================
local function handle_non_stream_body()
    local ctx = kong.ctx.plugin

    -- accumulate body chunks
    if not ctx.body_buffer then
        ctx.body_buffer = ""
    end
    ctx.body_buffer = ctx.body_buffer .. (ngx.arg[1] or "")

    if ngx.arg[2] then
        -- last chunk: convert and replace
        local openai_resp, err = cjson.decode(ctx.body_buffer)
        if err or openai_resp == nil then
            -- can't parse, pass through
            ngx.arg[1] = ctx.body_buffer
            return
        end

        ctx.response_model = openai_resp.model

        -- record usage for log phase
        if openai_resp.usage then
            ctx.input_tokens = openai_resp.usage.prompt_tokens or 0
            ctx.output_tokens = openai_resp.usage.completion_tokens or 0
        end

        local anthropic_resp = convert_response(openai_resp, ctx.request_model)
        ngx.arg[1] = cjson.encode(anthropic_resp)
    else
        -- not last chunk, suppress output until we have everything
        ngx.arg[1] = ""
    end
end

-- ============================================================
-- Phase: body_filter (streaming)
-- ============================================================
local function handle_stream_body()
    local ctx = kong.ctx.plugin
    local chunk = ngx.arg[1] or ""
    local is_last = ngx.arg[2]

    ctx.sse_buffer = (ctx.sse_buffer or "") .. chunk

    local output_parts = {}

    -- process complete SSE events (delimited by \n\n)
    while true do
        local event_end = string.find(ctx.sse_buffer, "\n\n", 1, true)
        if not event_end then
            break
        end

        local raw_event = string.sub(ctx.sse_buffer, 1, event_end - 1)
        ctx.sse_buffer = string.sub(ctx.sse_buffer, event_end + 2)

        -- parse SSE event lines
        local data_line = nil
        for line in string.gmatch(raw_event, "[^\n]+") do
            local d = string.match(line, "^data:%s*(.+)")
            if d then
                data_line = d
            end
        end

        if data_line == nil then
            -- no data line, skip (could be a comment or empty event)
            goto continue
        end

        -- handle [DONE] marker
        if data_line == "[DONE]" then
            if not ctx.finish_events_sent then
                -- close any open blocks
                if ctx.current_tool_index ~= nil then
                    output_parts[#output_parts + 1] = sse_frame("content_block_stop",
                        make_content_block_stop(ctx.anthropic_block_index))
                    ctx.current_tool_index = nil
                end
                if ctx.text_block_open then
                    output_parts[#output_parts + 1] = sse_frame("content_block_stop",
                        make_content_block_stop(0))
                    ctx.text_block_open = false
                end

                output_parts[#output_parts + 1] = sse_frame("message_delta",
                    make_message_delta("end_turn", ctx.output_tokens))
                output_parts[#output_parts + 1] = sse_frame("message_stop", make_message_stop())
                ctx.finish_events_sent = true
            end
            output_parts[#output_parts + 1] = "data: [DONE]\n\n"
            goto continue
        end

        -- parse JSON
        local event_t, parse_err = cjson.decode(data_line)
        if parse_err or event_t == nil then
            goto continue
        end

        -- record model
        if event_t.model and ctx.response_model == nil then
            ctx.response_model = event_t.model
        end

        -- record usage from final chunk
        if event_t.usage then
            ctx.input_tokens = event_t.usage.prompt_tokens or ctx.input_tokens
            ctx.output_tokens = event_t.usage.completion_tokens or ctx.output_tokens
        end

        local choice = ((event_t.choices or EMPTY)[1]) or nil

        if choice == nil then
            -- no choice data (e.g. usage-only chunk), skip
            goto continue
        end

        local delta = choice.delta or EMPTY
        local finish_reason = choice.finish_reason

        -- emit message_start + content_block_start(text) + ping on first data
        if not ctx.message_started then
            output_parts[#output_parts + 1] = sse_frame("message_start",
                make_message_start(ctx.request_model, ctx.input_tokens))
            output_parts[#output_parts + 1] = sse_frame("content_block_start",
                make_content_block_start_text(0))
            output_parts[#output_parts + 1] = sse_frame("ping", make_ping())
            ctx.message_started = true
            ctx.text_block_open = true
            ctx.anthropic_block_index = 0
        end

        -- handle text content delta
        if delta.content and delta.content ~= "" and delta.content ~= cjson.null then
            output_parts[#output_parts + 1] = sse_frame("content_block_delta",
                make_content_block_delta_text(0, delta.content))
        end

        -- handle tool calls
        if delta.tool_calls then
            for _, tc in ipairs(delta.tool_calls) do
                local tc_index = tc.index or 0

                -- new tool call detected
                if tc_index ~= ctx.current_tool_index then
                    -- close previous block if needed
                    if ctx.current_tool_index ~= nil then
                        output_parts[#output_parts + 1] = sse_frame("content_block_stop",
                            make_content_block_stop(ctx.anthropic_block_index))
                    elseif ctx.text_block_open then
                        -- close the text block before opening tool block
                        output_parts[#output_parts + 1] = sse_frame("content_block_stop",
                            make_content_block_stop(0))
                        ctx.text_block_open = false
                    end

                    ctx.current_tool_index = tc_index
                    ctx.anthropic_block_index = ctx.anthropic_block_index + 1

                    local tc_id = tc.id or ""
                    local tc_name = (tc["function"] or EMPTY).name or ""

                    output_parts[#output_parts + 1] = sse_frame("content_block_start",
                        make_content_block_start_tool_use(ctx.anthropic_block_index, tc_id, tc_name))
                end

                -- tool call arguments delta
                local args = (tc["function"] or EMPTY).arguments
                if args and args ~= "" then
                    output_parts[#output_parts + 1] = sse_frame("content_block_delta",
                        make_content_block_delta_json(ctx.anthropic_block_index, args))
                end
            end
        end

        -- handle finish
        if finish_reason and finish_reason ~= cjson.null and not ctx.finish_events_sent then
            -- close any open blocks
            if ctx.current_tool_index ~= nil then
                output_parts[#output_parts + 1] = sse_frame("content_block_stop",
                    make_content_block_stop(ctx.anthropic_block_index))
                ctx.current_tool_index = nil
            end
            if ctx.text_block_open then
                output_parts[#output_parts + 1] = sse_frame("content_block_stop",
                    make_content_block_stop(0))
                ctx.text_block_open = false
            end

            output_parts[#output_parts + 1] = sse_frame("message_delta",
                make_message_delta(map_finish_reason(finish_reason), ctx.output_tokens))
            output_parts[#output_parts + 1] = sse_frame("message_stop", make_message_stop())
            ctx.finish_events_sent = true
        end

        ::continue::
    end

    -- if this is the last chunk and we haven't sent finish events, close out
    if is_last and not ctx.finish_events_sent then
        if ctx.message_started then
            if ctx.current_tool_index ~= nil then
                output_parts[#output_parts + 1] = sse_frame("content_block_stop",
                    make_content_block_stop(ctx.anthropic_block_index))
            end
            if ctx.text_block_open then
                output_parts[#output_parts + 1] = sse_frame("content_block_stop",
                    make_content_block_stop(0))
            end
            output_parts[#output_parts + 1] = sse_frame("message_delta",
                make_message_delta("end_turn", ctx.output_tokens))
            output_parts[#output_parts + 1] = sse_frame("message_stop", make_message_stop())
        end
        output_parts[#output_parts + 1] = "data: [DONE]\n\n"
        ctx.finish_events_sent = true
    end

    ngx.arg[1] = table.concat(output_parts)
end

-- ============================================================
-- Phase: body_filter
-- ============================================================
function AIFormatAnthropicHandler:body_filter(conf)
    if kong.ctx.plugin.skip then
        return
    end

    local response_status = kong.service.response.get_status()
    if response_status ~= 200 then
        return
    end

    if kong.ctx.plugin.is_stream then
        handle_stream_body()
    else
        handle_non_stream_body()
    end
end

-- ============================================================
-- Phase: log
-- ============================================================
function AIFormatAnthropicHandler:log(conf)
    if kong.ctx.plugin.skip then
        return
    end

    local response_status = kong.service.response.get_status()
    if response_status ~= 200 then
        return
    end

    local ctx = kong.ctx.plugin

    local meta = {
        plugin_id = conf.__plugin_id,
        request_model = ctx.request_model,
        response_model = ctx.response_model,
    }
    kong.log.set_serialize_value("ai.statistics.meta", meta)

    local usage = {
        completion_tokens = ctx.output_tokens or 0,
        prompt_tokens = ctx.input_tokens or 0,
        total_tokens = (ctx.input_tokens or 0) + (ctx.output_tokens or 0),
    }
    kong.log.set_serialize_value("ai.statistics.usage", usage)
end

return AIFormatAnthropicHandler
