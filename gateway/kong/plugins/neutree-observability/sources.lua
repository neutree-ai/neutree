-- Worker-local, read-only business state providers. Producers register here;
-- consumers never import a producer's implementation or maintain its state.
-- snapshot() returns { models = {...}, targets = {...} }; resolve(route_id,
-- path) optionally identifies requests rejected before the producer ran.
return {}
