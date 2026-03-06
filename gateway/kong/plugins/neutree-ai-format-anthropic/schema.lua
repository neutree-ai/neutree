local PLUGIN_NAME = "neutree-ai-format-anthropic"

local schema = {
  name = PLUGIN_NAME,
  fields = {
    { config = {
        type = "record",
        fields = {},
      },
    },
  },
}

return schema
