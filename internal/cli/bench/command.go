package bench

// BenchTarget contains the target-specific information shared by every case.
// runner and cleanup are intentionally private so they cannot enter the local
// JSON manifest.
type BenchTarget struct {
	Platform  string
	RuntimeID string
	BaseURL   string
	Model     string
	// Tokenizer is the model path the engine reads inside its own container.
	// vllm bench runs there too, so it tokenizes prompt lengths from the same
	// files the server serves. It does not forward the engine's tokenizer
	// overrides (--tokenizer-mode, --trust-remote-code).
	Tokenizer string

	runner  remoteRunner
	cleanup func() error
}

func (t *BenchTarget) Close() error {
	if t == nil || t.cleanup == nil {
		return nil
	}

	return t.cleanup()
}
