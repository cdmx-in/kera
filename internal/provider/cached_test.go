package provider

import (
	"context"
	"testing"
)

// The cached part of the prompt is reported when the server says it (chat-completions
// prompt_tokens_details, Responses input_tokens_details) and stays nil when it does not.
func TestCachedTokens(t *testing.T) {
	cases := []struct {
		name, kind string
		body       []string
		want       *int
	}{
		{"openai reports", KindOpenAI, sse(`{"choices":[{"index":0,"delta":{"content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":16}}}`, `[DONE]`), intp(16)},
		{"openai silent", KindOpenAI, sse(`{"choices":[{"index":0,"delta":{"content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":2}}`, `[DONE]`), nil},
		{"openai zero is a report", KindOpenAI, sse(`{"choices":[{"index":0,"delta":{"content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":0}}}`, `[DONE]`), intp(0)},
		{"responses reports", KindResponses, []string{"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":30,\"output_tokens\":4,\"input_tokens_details\":{\"cached_tokens\":24}}}}\n\n"}, intp(24)},
		{"responses silent", KindResponses, []string{"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":30,\"output_tokens\":4}}}\n\n"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := stub(t, 200, "text/event-stream", c.body...)
			evs, err := collect(client(t, c.kind, srv.URL).Stream(context.Background(), Request{Model: "m", Messages: history}))
			if err != nil {
				t.Fatal(err)
			}
			got := usageOf(evs).CachedTokens
			if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
				t.Fatalf("cached tokens %v, want %v", got, c.want)
			}
		})
	}
}

func intp(n int) *int { return &n }
