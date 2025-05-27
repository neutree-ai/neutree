local PLUGIN_NAME = "neutree-ai-statistics"

local schema = {
  name = PLUGIN_NAME,
  fields = {
    { config = {
        type = "record",
        fields = {
          {
            route_type = {
              type = "string",
              required = true,
              one_of = {"/v1/chat/completions","/v1/embeddings","/v1/rerank"},
            },
          },
        },
      },
    },
  }
}

return schema