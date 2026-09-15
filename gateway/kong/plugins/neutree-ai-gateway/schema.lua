local PLUGIN_NAME = "neutree-ai-gateway"

local upstream_entry = {
  type = "record",
  fields = {
    { name = { type = "string", required = false } },
    {
      scheme = {
        type = "string",
        required = true,
        one_of = { "http", "https" },
      },
    },
    {
      host = {
        type = "string",
        required = true,
      },
    },
    {
      port = {
        type = "integer",
        required = true,
      },
    },
    {
      path = {
        type = "string",
        required = true,
        default = "/",
      },
    },
    {
      auth_header = {
        type = "string",
        required = false,
      },
    },
    {
      model_mapping = {
        type = "map",
        required = true,
        keys = { type = "string" },
        values = { type = "string" },
      },
    },
    {
      internal = {
        type = "boolean",
        required = false,
        default = false,
      },
    },
  },
}

local model_route_target = {
  type = "record",
  fields = {
    { upstream = { type = "string", required = true } },
    { upstream_model = { type = "string", required = true } },
    { priority = { type = "integer", required = false, default = 0, between = { 0, 2147483647 } } },
    { weight = { type = "integer", required = false, default = 1, between = { 1, 2147483647 } } },
    { max_inflight_requests = { type = "integer", required = false, default = 0, between = { 0, 2147483647 } } },
  },
}

local model_route = {
  type = "record",
  fields = {
    { model = { type = "string", required = true } },
    { retryable_conditions = {
        type = "array",
        required = false,
        elements = { type = "string", one_of = { "http_429", "http_502", "http_503", "http_504", "timeout" } },
      } },
    { max_attempts = { type = "integer", required = false, default = 0, between = { 0, 2147483647 } } },
    { targets = { type = "array", required = true, elements = model_route_target } },
  },
}

local schema = {
  name = PLUGIN_NAME,
  fields = {
    { config = {
        type = "record",
        fields = {
          {
            route_type = {
              type = "string",
              required = false,
            },
          },
          {
            route_prefix = {
              type = "string",
              required = false,
            },
          },
          {
            -- IE/EE dimension this route serves ("internal" | "external"). Stashed
            -- into kong.ctx.shared for the consumer-scoped neutree-ai-access plugin
            -- to enforce endpoint-level model allowlists.
            endpoint_type = {
              type = "string",
              required = false,
              one_of = { "internal", "external" },
            },
          },
          {
            endpoint_name = {
              type = "string",
              required = false,
            },
          },
          {
            upstreams = {
              type = "array",
              required = false,
              elements = upstream_entry,
            },
          },
          {
            model_routes = {
              type = "array",
              required = false,
              elements = model_route,
            },
          },
        },
      },
    },
  },
}

return schema
