local PLUGIN_NAME = "neutree-ai-quota"

local schema = {
  name = PLUGIN_NAME,
  fields = {
    { config = {
        type = "record",
        fields = {
          -- Base URL of neutree-api (exposes /api/v1/rpc/get_api_key_remaining).
          {
            api_url = {
              type = "string",
              required = true,
              default = "http://neutree-api:3000",
            },
          },
          -- service_role JWT used to authenticate the remaining-token lookup.
          {
            service_token = {
              type = "string",
              required = false,
              default = "",
              referenceable = true,
            },
          },
          -- Seconds to cache a key's remaining tokens. Bounds the sync window
          -- (and therefore the worst-case overage) between management plane and
          -- gateway.
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
