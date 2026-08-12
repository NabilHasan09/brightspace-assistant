package retrieve

// Voyage AI (voyage-4) embeddings as a custom chromem.EmbeddingFunc — chromem
// ships helpers for OpenAI, Cohere, Mistral and Ollama, but not Voyage, so
// this is ~40 lines against their REST API with net/http.
//
// Separate provider and separate API key from Anthropic. 200M free tokens on
// the voyage-4 family, which is effectively free at this corpus size.
//
// MUST return a normalized vector — chromem requires unit length and does not
// normalize for you. Verify empirically whether voyage-4 already returns
// normalized embeddings; if not, normalize here. Getting this wrong does not
// error, it silently degrades ranking, which is the worst failure shape
// available. Assert unit length in a test rather than trusting the provider.
//
// Signatures confirmed against chromem-go v0.7.0:
//
//	type EmbeddingFunc func(ctx context.Context, text string) ([]float32, error)
//
// Build step 4.
