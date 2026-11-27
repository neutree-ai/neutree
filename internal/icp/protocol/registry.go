package protocol

type wireCodec ID

func Codecs() map[ID]Codec {
	return map[ID]Codec{
		OpenAIChat: wireCodec(OpenAIChat), AnthropicMessages: wireCodec(AnthropicMessages), OpenAIResponses: wireCodec(OpenAIResponses),
	}
}

// Path is the native inference path on an upstream that speaks this protocol.
func (id ID) Path() string {
	switch id {
	case OpenAIChat:
		return "/v1/chat/completions"
	case AnthropicMessages:
		return "/v1/messages"
	case OpenAIResponses:
		return "/v1/responses"
	default:
		return ""
	}
}
