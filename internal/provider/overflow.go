package provider

import (
	"errors"
	"net/http"
	"strings"
)

// overflowMarkers are what model servers say, in lower case, when the prompt does not fit the context window: OpenAI
// and vLLM ("maximum context length"), Ollama ("prompt too long; exceeded max context length", or the chat template's
// "no user query found in messages" once the server cut every user message out of a prompt that was too long), LiteLLM
// ("ContextWindowExceededError") and Anthropic ("prompt is too long").
var overflowMarkers = []string{
	"no user query found",
	"context length",
	"context_length_exceeded",
	"context window",
	"contextwindowexceeded",
	"maximum context",
	"too many tokens",
	"prompt is too long",
	"prompt too long",
	"input is too long",
	"input too long",
	"reduce the length of the messages",
}

// IsContextOverflow reports whether err says the prompt did not fit the model's context window: an HTTP 413, or any
// failed call whose answer carries one of the usual messages. Sending the same prompt again fails the same way, so
// the caller must make it smaller first.
func IsContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	var he *HTTPError
	if errors.As(err, &he) {
		if he.Status == http.StatusRequestEntityTooLarge {
			return true
		}
		text = he.Body // the URL and status text must not match
	}
	text = strings.ToLower(text)
	for _, m := range overflowMarkers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}
