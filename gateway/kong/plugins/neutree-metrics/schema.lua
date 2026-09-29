local typedefs = require("kong.db.schema.typedefs")

return {
    name = "neutree-metrics",
    fields = {
        { protocols = typedefs.protocols_http },
        { consumer = typedefs.no_consumer },
        { service = typedefs.no_service },
        { route = typedefs.no_route },
        { config = { type = "record", fields = {} } },
    },
}
