local cjson = require("cjson.safe")
local buffer = require("string.buffer")
local ai_shared = require("kong.llm.drivers.shared")
local ai_driver = require("kong.llm.drivers.openai")
local strip = require("kong.tools.string").strip

local AIGatewayHandler = {
    PRIORITY = 1100,
    VERSION = "0.0.1",
}

local EMPTY = {}

-- cjson decodes JSON null as cjson.null (a userdata sentinel).
-- Indexing userdata causes "attempt to index a userdata value".
-- Use this helper wherever a decoded field might be null.
local function is_table(v)
    return type(v) == "table"
end

local SUPPORTED_ROUTE_TYPES = {
    "/v1/chat/completions",
    "/v1/embeddings",
    "/v1/rerank",
}

local function extract_suffix(request_path, prefix)
    local suffix = request_path
    if prefix and prefix ~= "" then
        local prefix_pattern = "^" .. prefix:gsub("([%-%.%+%[%]%(%)%$%^%%%?%*])", "%%%1")
        suffix = request_path:gsub(prefix_pattern, "", 1)
    end
    if suffix == "" then
        return "/"
    end
    return suffix
end

-- Strip the /v1 prefix from client-facing suffix so it is not duplicated
-- when the upstream URL already includes the API version path (e.g. /v1).
local function strip_api_version_prefix(path)
    local stripped = path:gsub("^/v1/", "/", 1)
    if stripped == path then
        -- also handle exact "/v1" without trailing slash
        stripped = path:gsub("^/v1$", "/", 1)
    end
    return stripped
end

local function is_models_path(path)
    return path == "/v1/models" or path == "/v1/models/"
end

local function is_anthropic_models_path(path)
    return path == "/anthropic/v1/models" or path == "/anthropic/v1/models/"
end

local function is_anthropic_messages_path(path)
    return path == "/anthropic/v1/messages" or path == "/anthropic/v1/messages/"
end

local function detect_route_type(request_path)
    for _, rt in ipairs(SUPPORTED_ROUTE_TYPES) do
        if string.match(request_path, rt .. "$") then
            return rt
        end
    end
    return nil
end

local function get_token_text(event_t)
    local first_choice = ((event_t or EMPTY).choices or EMPTY)[1] or EMPTY
    local token_text = (first_choice.delta or EMPTY).content or first_choice.text or ""
    return (type(token_text) == "string" and token_text) or ""
end

local function sse_frame(event_type, data)
    if type(data) == "table" then
        data = cjson.encode(data)
    end
    if event_type then
        return "event: " .. event_type .. "\ndata: " .. data .. "\n\n"
    end
    return "data: " .. data .. "\n\n"
end

local function anthropic_error(status, err_type, message)
    return kong.response.exit(status, {
        type = "error",
        error = {
            type = err_type,
            message = message,
        },
    })
end

local function fail(code, msg)
    return kong.response.exit(code, msg and { error = { message = msg } } or nil)
end

local function resolve_upstream(conf, model)
    if not conf.upstreams then
        return nil
    end

    for _, entry in ipairs(conf.upstreams) do
        if entry.model_mapping[model] then
            return entry
        end
    end

    return nil
end

local function set_upstream_target(entry)
    local target_host = entry.host
    local connect_host = target_host
    if not target_host:match("^%d+%.%d+%.%d+%.%d+$") then
        local toip = require("socket").dns.toip
        local ip, err = toip(target_host)
        if not ip then
            return nil, kong.response.exit(502, {
                error = {
                    message = "DNS resolution failed for upstream: " .. target_host .. " (" .. (err or "unknown") .. ")",
                    type = "upstream_error",
                },
            })
        end
        connect_host = ip
    end

    kong.service.set_target(connect_host, entry.port)
    kong.service.request.set_scheme(entry.scheme)

    if connect_host ~= target_host then
        kong.service.request.set_header("Host", target_host)
        ngx.var.upstream_host = target_host
    end

    if entry.auth_header and entry.auth_header ~= "" then
        kong.service.request.set_header("Authorization", entry.auth_header)
    end

    return connect_host
end

local function build_upstream_path(entry, api_path)
    local upstream_base = entry.path or "/"
    if upstream_base == "/" then
        upstream_base = ""
    end
    upstream_base = upstream_base:gsub("/$", "")

    local final_path = upstream_base .. api_path
    if final_path == "" then
        final_path = "/"
    end
    return final_path
end

local function maybe_return_model_list(conf, suffix)
    if not conf.upstreams then
        return false
    end

    if is_models_path(suffix) and kong.request.get_method() == "GET" then
        local models = {}
        for _, entry in ipairs(conf.upstreams) do
            for model_name, _ in pairs(entry.model_mapping) do
                models[#models + 1] = {
                    id = model_name,
                    object = "model",
                    created = 0,
                    owned_by = "external-endpoint",
                }
            end
        end
        kong.response.exit(200, {
            object = "list",
            data = models,
        })
        return true
    end

    if is_anthropic_models_path(suffix) and kong.request.get_method() == "GET" then
        local models = {}
        for _, entry in ipairs(conf.upstreams) do
            for model_name, _ in pairs(entry.model_mapping) do
                models[#models + 1] = {
                    id = model_name,
                    type = "model",
                    display_name = model_name,
                    created_at = "2025-01-01T00:00:00Z",
                }
            end
        end
        kong.response.exit(200, {
            data = models,
            has_more = false,
            first_id = models[1] and models[1].id or nil,
            last_id = models[#models] and models[#models].id or nil,
        })
        return true
    end

    return false
end

local function map_finish_reason(reason)
    if reason == "stop" then return "end_turn" end
    if reason == "length" then return "max_tokens" end
    if reason == "tool_calls" then return "tool_use" end
    return "end_turn"
end

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
                local found_tool_result = false

                for _, block in ipairs(msg.content) do
                    if block.type == "tool_result" then
                        found_tool_result = true
                        local tool_content = ""
                        if type(block.content) == "string" then
                            tool_content = block.content
                        elseif type(block.content) == "table" then
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

                if #user_parts > 0 then
                    openai_messages[#openai_messages + 1] = {
                        role = "user",
                        content = user_parts,
                    }
                end

                for _, tr in ipairs(tool_results) do
                    openai_messages[#openai_messages + 1] = tr
                end

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

                if #user_parts == 0 and #tool_results == 0 and #trailing_user_parts == 0 then
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
                end

                local assistant_msg = { role = "assistant" }
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

    local converted = convert_messages(anthropic_req.messages or {})
    for _, m in ipairs(converted) do
        messages[#messages + 1] = m
    end
    openai_req.messages = messages

    if anthropic_req.stop_sequences then
        openai_req.stop = anthropic_req.stop_sequences
    end
    if anthropic_req.stream then
        openai_req.stream_options = { include_usage = true }
    end
    if anthropic_req.tools then
        openai_req.tools = convert_tools(anthropic_req.tools)
    end
    if anthropic_req.tool_choice then
        openai_req.tool_choice = convert_tool_choice(anthropic_req.tool_choice)
    end

    return openai_req
end

local function convert_response(openai_resp, request_model)
    local choice = ((openai_resp.choices or EMPTY)[1]) or EMPTY
    local message = choice.message or EMPTY
    local content = {}

    if message.content and message.content ~= "" and message.content ~= cjson.null then
        content[#content + 1] = {
            type = "text",
            text = message.content,
        }
    end

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

    if #content == 0 then
        content[#content + 1] = {
            type = "text",
            text = "",
        }
    end

    local usage = is_table(openai_resp.usage) and openai_resp.usage or EMPTY
    return {
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
end

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

local function handle_openai_json_response()
    local response_body = kong.service.response.get_raw_body()
    if response_body == nil then
        return
    end

    local ai_response, err = cjson.decode(response_body)
    if err then
        return
    end

    local route_type = kong.ctx.plugin.route_type
    kong.ctx.plugin.response_model = ai_response.model
    if is_table(ai_response.usage) then
        if route_type == "/v1/chat/completions" then
            kong.ctx.plugin.completions_tokens = ai_response.usage.completion_tokens
            kong.ctx.plugin.prompt_tokens = ai_response.usage.prompt_tokens
            kong.ctx.plugin.total_tokens = ai_response.usage.total_tokens
        else
            kong.ctx.plugin.prompt_tokens = ai_response.usage.prompt_tokens
            kong.ctx.plugin.total_tokens = ai_response.usage.total_tokens
        end
    end
end

local function handle_openai_stream_response(chunk, finished)
    local content_type = kong.service.response.get_header("Content-Type")
    local normalized_content_type = content_type and content_type:sub(1, (content_type:find(";") or 0) - 1)
    if normalized_content_type and normalized_content_type ~= "text/event-stream" then
        return
    end

    local body_buffer = kong.ctx.plugin.sse_body_buffer
    if type(chunk) == "string" and chunk ~= "" then
        local events = ai_shared.frame_to_events(chunk, normalized_content_type)
        if not events then
            return
        end

        for _, event in ipairs(events) do
            local formatted = ai_driver.from_format(event, "", "stream/llm/v1/chat")
            if formatted and formatted ~= ai_shared._CONST.SSE_TERMINATOR then
                local event_t, err = cjson.decode(formatted)
                if not err then
                    if event_t.choices and #event_t.choices > 0 and body_buffer ~= nil then
                        body_buffer:put(get_token_text(event_t))
                    end
                    if kong.ctx.plugin.response_model == nil and event_t.model then
                        kong.ctx.plugin.response_model = event_t.model
                    end
                    if is_table(event_t.usage) then
                        kong.ctx.plugin.prompt_tokens = event_t.usage.prompt_tokens
                        kong.ctx.plugin.completions_tokens = event_t.usage.completion_tokens
                        kong.ctx.plugin.total_tokens = event_t.usage.total_tokens
                    end
                end
            end
        end
    end

    if finished and kong.ctx.plugin.total_tokens == nil and body_buffer ~= nil then
        local response = body_buffer:get()
        local completion_tokens_count = math.ceil(#strip(response) / 4)
        kong.ctx.plugin.completions_tokens = completion_tokens_count
        kong.ctx.plugin.total_tokens = kong.ctx.plugin.completions_tokens + (kong.ctx.plugin.prompt_tokens or 0)
    end
end

local function handle_anthropic_non_stream_body()
    local ctx = kong.ctx.plugin
    if not ctx.body_buffer then
        ctx.body_buffer = ""
    end
    ctx.body_buffer = ctx.body_buffer .. (ngx.arg[1] or "")

    if ngx.arg[2] then
        local openai_resp, err = cjson.decode(ctx.body_buffer)
        if err or openai_resp == nil then
            ngx.arg[1] = ctx.body_buffer
            return
        end

        ctx.response_model = openai_resp.model
        if is_table(openai_resp.usage) then
            ctx.input_tokens = openai_resp.usage.prompt_tokens or 0
            ctx.output_tokens = openai_resp.usage.completion_tokens or 0
        end

        local anthropic_resp = convert_response(openai_resp, ctx.request_model)
        ngx.arg[1] = cjson.encode(anthropic_resp)
    else
        ngx.arg[1] = ""
    end
end

local function handle_anthropic_stream_body()
    local ctx = kong.ctx.plugin
    local chunk = ngx.arg[1] or ""
    local is_last = ngx.arg[2]
    ctx.sse_buffer = (ctx.sse_buffer or "") .. chunk

    local output_parts = {}
    while true do
        local event_end = string.find(ctx.sse_buffer, "\n\n", 1, true)
        if not event_end then
            break
        end

        local raw_event = string.sub(ctx.sse_buffer, 1, event_end - 1)
        ctx.sse_buffer = string.sub(ctx.sse_buffer, event_end + 2)

        local data_line = nil
        for line in string.gmatch(raw_event, "[^\n]+") do
            local d = string.match(line, "^data:%s*(.+)")
            if d then
                data_line = d
            end
        end

        if data_line == nil then
            goto continue
        end

        if data_line == "[DONE]" then
            if not ctx.finish_events_sent then
                if ctx.current_tool_index ~= nil then
                    output_parts[#output_parts + 1] = sse_frame("content_block_stop", make_content_block_stop(ctx.anthropic_block_index))
                    ctx.current_tool_index = nil
                end
                if ctx.text_block_open then
                    output_parts[#output_parts + 1] = sse_frame("content_block_stop", make_content_block_stop(0))
                    ctx.text_block_open = false
                end
                output_parts[#output_parts + 1] = sse_frame("message_delta", make_message_delta("end_turn", ctx.output_tokens))
                output_parts[#output_parts + 1] = sse_frame("message_stop", make_message_stop())
                ctx.finish_events_sent = true
            end
            output_parts[#output_parts + 1] = "data: [DONE]\n\n"
            goto continue
        end

        local event_t, parse_err = cjson.decode(data_line)
        if parse_err or event_t == nil then
            goto continue
        end

        if event_t.model and ctx.response_model == nil then
            ctx.response_model = event_t.model
        end
        if is_table(event_t.usage) then
            ctx.input_tokens = event_t.usage.prompt_tokens or ctx.input_tokens
            ctx.output_tokens = event_t.usage.completion_tokens or ctx.output_tokens
        end

        local choice = ((event_t.choices or EMPTY)[1]) or nil
        if choice == nil then
            goto continue
        end

        local delta = choice.delta or EMPTY
        local finish_reason = choice.finish_reason

        if not ctx.message_started then
            output_parts[#output_parts + 1] = sse_frame("message_start", make_message_start(ctx.request_model, ctx.input_tokens))
            output_parts[#output_parts + 1] = sse_frame("content_block_start", make_content_block_start_text(0))
            output_parts[#output_parts + 1] = sse_frame("ping", make_ping())
            ctx.message_started = true
            ctx.text_block_open = true
            ctx.anthropic_block_index = 0
        end

        if delta.content and delta.content ~= "" and delta.content ~= cjson.null then
            output_parts[#output_parts + 1] = sse_frame("content_block_delta", make_content_block_delta_text(0, delta.content))
        end

        if delta.tool_calls then
            for _, tc in ipairs(delta.tool_calls) do
                local tc_index = tc.index or 0
                if tc_index ~= ctx.current_tool_index then
                    if ctx.current_tool_index ~= nil then
                        output_parts[#output_parts + 1] = sse_frame("content_block_stop", make_content_block_stop(ctx.anthropic_block_index))
                    elseif ctx.text_block_open then
                        output_parts[#output_parts + 1] = sse_frame("content_block_stop", make_content_block_stop(0))
                        ctx.text_block_open = false
                    end

                    ctx.current_tool_index = tc_index
                    ctx.anthropic_block_index = ctx.anthropic_block_index + 1
                    local tc_id = tc.id or ""
                    local tc_name = (tc["function"] or EMPTY).name or ""
                    output_parts[#output_parts + 1] = sse_frame("content_block_start", make_content_block_start_tool_use(ctx.anthropic_block_index, tc_id, tc_name))
                end

                local args = (tc["function"] or EMPTY).arguments
                if args and args ~= "" then
                    output_parts[#output_parts + 1] = sse_frame("content_block_delta", make_content_block_delta_json(ctx.anthropic_block_index, args))
                end
            end
        end

        if finish_reason and finish_reason ~= cjson.null and not ctx.finish_events_sent then
            if ctx.current_tool_index ~= nil then
                output_parts[#output_parts + 1] = sse_frame("content_block_stop", make_content_block_stop(ctx.anthropic_block_index))
                ctx.current_tool_index = nil
            end
            if ctx.text_block_open then
                output_parts[#output_parts + 1] = sse_frame("content_block_stop", make_content_block_stop(0))
                ctx.text_block_open = false
            end
            output_parts[#output_parts + 1] = sse_frame("message_delta", make_message_delta(map_finish_reason(finish_reason), ctx.output_tokens))
            output_parts[#output_parts + 1] = sse_frame("message_stop", make_message_stop())
            ctx.finish_events_sent = true
        end

        ::continue::
    end

    if is_last and not ctx.finish_events_sent then
        if ctx.message_started then
            if ctx.current_tool_index ~= nil then
                output_parts[#output_parts + 1] = sse_frame("content_block_stop", make_content_block_stop(ctx.anthropic_block_index))
            end
            if ctx.text_block_open then
                output_parts[#output_parts + 1] = sse_frame("content_block_stop", make_content_block_stop(0))
            end
            output_parts[#output_parts + 1] = sse_frame("message_delta", make_message_delta("end_turn", ctx.output_tokens))
            output_parts[#output_parts + 1] = sse_frame("message_stop", make_message_stop())
        end
        output_parts[#output_parts + 1] = "data: [DONE]\n\n"
        ctx.finish_events_sent = true
    end

    ngx.arg[1] = table.concat(output_parts)
end

function AIGatewayHandler:access(conf)
    local request_path = kong.request.get_path()
    local suffix = extract_suffix(request_path, conf.route_prefix or "")

    if maybe_return_model_list(conf, suffix) then
        return
    end

    if is_anthropic_messages_path(suffix) then
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

        local openai_req = convert_request(anthropic_req)
        kong.ctx.plugin.anthropic_mode = true
        kong.ctx.plugin.request_model = anthropic_req.model
        kong.ctx.plugin.response_model = nil
        kong.ctx.plugin.is_stream = anthropic_req.stream == true
        kong.ctx.plugin.route_type = "/v1/chat/completions"

        if conf.upstreams then
            local matched_entry = resolve_upstream(conf, openai_req.model)
            if not matched_entry then
                return anthropic_error(400, "invalid_request_error", "No upstream configured for model: " .. tostring(openai_req.model))
            end

            local _, route_err = set_upstream_target(matched_entry)
            if route_err then
                return route_err
            end
            if matched_entry.internal then
                kong.service.request.set_path(build_upstream_path(matched_entry, "/v1/chat/completions"))
            else
                kong.service.request.set_path(build_upstream_path(matched_entry, "/chat/completions"))
            end
            openai_req.model = matched_entry.model_mapping[openai_req.model]
        else
            kong.service.request.set_path(string.gsub(request_path, "/anthropic/v1/messages/?$", "/v1/chat/completions"))
        end

        local openai_json = cjson.encode(openai_req)
        kong.service.request.set_raw_body(openai_json)
        kong.service.request.set_header("Content-Type", "application/json")
        kong.service.request.clear_header("anthropic-version")
        kong.service.request.clear_header("anthropic-beta")

        if not kong.ctx.plugin.is_stream then
            kong.service.request.enable_buffering()
        else
            kong.ctx.plugin.message_started = false
            kong.ctx.plugin.text_block_open = false
            kong.ctx.plugin.current_tool_index = nil
            kong.ctx.plugin.anthropic_block_index = 0
            kong.ctx.plugin.sse_buffer = ""
            kong.ctx.plugin.input_tokens = 0
            kong.ctx.plugin.output_tokens = 0
            kong.ctx.plugin.finish_events_sent = false
        end

        return
    end

    local route_type
    if conf.route_type and type(conf.route_type) == "string" then
        if not string.match(request_path, conf.route_type .. "$") then
            kong.ctx.plugin.skip = true
            return
        end
        route_type = conf.route_type
    else
        route_type = detect_route_type(request_path)
        if not route_type then
            kong.ctx.plugin.skip = true
            return
        end
    end

    kong.ctx.plugin.route_type = route_type

    local content_type = kong.request.get_header("Content-Type") or "application/json"
    if not string.find(content_type, "application/json", nil, true) then
        return fail(400, "only support content-type is application/json")
    end

    local request_body = kong.request.get_raw_body()
    if request_body == nil then
        return fail(400, "request body can not be null")
    end

    local ai_request, err = cjson.decode(request_body)
    if err then
        return fail(400, "request body is not json format")
    end

    kong.ctx.plugin.request_model = ai_request.model

    if conf.upstreams then
        if not ai_request.model or ai_request.model == "" then
            return fail(400, "missing 'model' field in request body")
        end

        local matched_entry = resolve_upstream(conf, ai_request.model)
        if not matched_entry then
            return fail(400, "No upstream configured for model: " .. ai_request.model)
        end

        local _, route_err = set_upstream_target(matched_entry)
        if route_err then
            return route_err
        end

        if matched_entry.internal then
            kong.service.request.set_path(build_upstream_path(matched_entry, suffix))
        else
            kong.service.request.set_path(build_upstream_path(matched_entry, strip_api_version_prefix(suffix)))
        end
        ai_request.model = matched_entry.model_mapping[ai_request.model]
        local new_body = cjson.encode(ai_request)
        if new_body then
            kong.service.request.set_raw_body(new_body)
        end
    end

    if not ai_request.stream then
        kong.service.request.enable_buffering()
    else
        local prompt_tokens, cost_err = ai_shared.calculate_cost(ai_request or {}, {}, 1.0)
        if cost_err then
            kong.log.err("unable to estimate request token cost: ", cost_err)
            return fail(500)
        end
        kong.ctx.plugin.prompt_tokens = prompt_tokens
    end
end

function AIGatewayHandler:header_filter(conf)
    if kong.ctx.plugin.skip then
        return
    end

    local response_status = kong.service.response.get_status()
    if response_status ~= 200 then
        return
    end

    if kong.ctx.plugin.anthropic_mode then
        if kong.ctx.plugin.is_stream then
            kong.response.set_header("Content-Type", "text/event-stream")
            kong.response.clear_header("Content-Length")
        else
            kong.response.clear_header("Content-Length")
            kong.response.set_header("Content-Type", "application/json")
        end
        return
    end

    local content_type = kong.service.response.get_header("Content-Type")
    local normalized_content_type = content_type and content_type:sub(1, (content_type:find(";") or 0) - 1)
    if normalized_content_type and normalized_content_type == "text/event-stream" then
        kong.ctx.plugin.sse_body_buffer = buffer.new()
        return true
    end

    handle_openai_json_response()
end

function AIGatewayHandler:body_filter(conf)
    if kong.ctx.plugin.skip then
        return
    end

    local response_status = kong.service.response.get_status()
    if response_status ~= 200 then
        return
    end

    if kong.ctx.plugin.anthropic_mode then
        if kong.ctx.plugin.is_stream then
            handle_anthropic_stream_body()
        else
            handle_anthropic_non_stream_body()
        end
        return
    end

    handle_openai_stream_response(ngx.arg[1], ngx.arg[2])
end

function AIGatewayHandler:log(conf)
    if kong.ctx.plugin.skip then
        return
    end

    local response_status = kong.service.response.get_status()
    if response_status ~= 200 then
        return
    end

    local meta = {
        plugin_id = conf.__plugin_id,
        request_model = kong.ctx.plugin.request_model,
        response_model = kong.ctx.plugin.response_model,
    }
    kong.log.set_serialize_value("ai.statistics.meta", meta)

    local usage
    if kong.ctx.plugin.anthropic_mode then
        usage = {
            completion_tokens = kong.ctx.plugin.output_tokens or 0,
            prompt_tokens = kong.ctx.plugin.input_tokens or 0,
            total_tokens = (kong.ctx.plugin.input_tokens or 0) + (kong.ctx.plugin.output_tokens or 0),
        }
    else
        usage = {
            completion_tokens = kong.ctx.plugin.completions_tokens,
            prompt_tokens = kong.ctx.plugin.prompt_tokens,
            total_tokens = kong.ctx.plugin.total_tokens,
        }
    end
    kong.log.set_serialize_value("ai.statistics.usage", usage)
end

return AIGatewayHandler
