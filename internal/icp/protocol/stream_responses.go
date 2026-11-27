package protocol

func (e *streamEncoder) response(status string) wireObject {
	output := make([]wireObject, 0, len(e.order))
	for _, b := range e.order {
		output = append(output, e.responseItem(b, true))
	}

	r := wireObject{"object": "response", "id": e.id, "model": e.model, "created_at": e.created, "status": status, "output": output,
		"error": nil, "incomplete_details": nil, "usage": renderUsage(OpenAIResponses, e.usage)}
	if status == statusIncomplete {
		r["incomplete_details"] = wireObject{"reason": responsesIncompleteReason(e.finish)}
	}

	return r
}

func responsePart(b *streamBlock) wireObject {
	switch b.kind {
	case blockThink, blockRedacted:
		return wireObject{"type": "summary_text", blockText: b.text.String()}
	case blockRefusal:
		return wireObject{"type": blockRefusal, blockRefusal: b.text.String()}
	default:
		return wireObject{"type": "output_text", blockText: b.text.String(), "annotations": []any{}}
	}
}

func (e *streamEncoder) responseItem(b *streamBlock, complete bool) wireObject {
	item := wireObject{"id": itemID(b.kind, e.id, b.position), "status": "in_progress"}
	var parts []wireObject

	if complete {
		item["status"] = statusComplete
		parts = []wireObject{responsePart(b)}
	} else {
		parts = []wireObject{}
	}

	switch b.kind {
	case blockCall:
		item["type"] = itemCall
		item["name"] = b.name
		item["call_id"] = b.callID
		item["arguments"] = b.text.String()
	case blockThink, blockRedacted:
		item["type"] = itemReason
		item["summary"] = parts

		if b.source == OpenAIResponses && b.signature != "" {
			item["encrypted_content"] = b.signature
		}
	default:
		item["type"] = itemMessage
		item["role"] = roleAssistant
		item["content"] = parts
	}

	return item
}

func (e *streamEncoder) coords(b *streamBlock) wireObject {
	a := wireObject{"output_index": b.position, "item_id": itemID(b.kind, e.id, b.position)}

	switch b.kind {
	case blockThink, blockRedacted:
		a["summary_index"] = 0
	case blockText, blockRefusal:
		a["content_index"] = 0
	}

	return a
}

func (w *frameWriter) responseOpen(b *streamBlock) {
	w.send("response.output_item.added", wireObject{"output_index": b.position, "item": w.responseItem(b, false)})

	if b.kind == blockCall {
		return
	}

	args := w.coords(b)
	args["part"] = responsePart(b)

	if b.kind == blockThink || b.kind == blockRedacted {
		w.send("response.reasoning_summary_part.added", args)
	} else {
		w.send("response.content_part.added", args)
	}
}

func responseDeltaName(kind string) string {
	switch kind {
	case blockCall:
		return "response.function_call_arguments"
	case blockThink, blockRedacted:
		return "response.reasoning_summary_text"
	case blockRefusal:
		return "response.refusal"
	default:
		return "response.output_text"
	}
}

func (w *frameWriter) responseDelta(b *streamBlock, delta string) {
	args := w.coords(b)
	args["delta"] = delta
	w.send(responseDeltaName(b.kind)+".delta", args)
}

func (w *frameWriter) responseClose(b *streamBlock) {
	args := w.coords(b)

	switch b.kind {
	case blockCall:
		args["arguments"] = b.text.String()
		args["name"] = b.name
	case blockRefusal:
		args[blockRefusal] = b.text.String()
	default:
		args[blockText] = b.text.String()
	}

	w.send(responseDeltaName(b.kind)+".done", args)

	if b.kind != blockCall {
		args = w.coords(b)
		args["part"] = responsePart(b)

		if b.kind == blockThink || b.kind == blockRedacted {
			w.send("response.reasoning_summary_part.done", args)
		} else {
			w.send("response.content_part.done", args)
		}
	}

	w.send("response.output_item.done", wireObject{"output_index": b.position, "item": w.responseItem(b, true)})
}
