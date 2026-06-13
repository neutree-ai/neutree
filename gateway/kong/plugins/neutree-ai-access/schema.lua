local PLUGIN_NAME = "neutree-ai-access"

local schema = {
  name = PLUGIN_NAME,
  fields = {
    { config = {
        type = "record",
        fields = {
          -- Base URL of neutree-api (exposes /api/v1/rpc/get_api_key_access).
          {
            api_url = {
              type = "string",
              required = true,
              default = "http://neutree-api:3000",
            },
          },
          -- service_role JWT used to authenticate the access-gate lookup.
          -- No default/referenceable: an empty-string default trips kong's
          -- vault-reference validation and aborts plugin-schema load. The
          -- handler no-ops when it is unset.
          {
            service_token = {
              type = "string",
              required = false,
            },
          },
          -- Seconds to cache a key's resolved access gate. Bounds the sync
          -- window between management plane and gateway.
          {
            cache_ttl = {
              type = "number",
              required = false,
              default = 5,
            },
          },
          -- Upstream lookup timeout in milliseconds.
          {
            timeout = {
              type = "number",
              required = false,
              default = 2000,
            },
          },
        },
      },
    },
  },
}

return schema
