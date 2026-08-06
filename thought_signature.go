package main

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
	"time"
)

const thoughtSignatureTTL = 30 * time.Minute

type thoughtSignatureEntry struct {
	signature string
	expiresAt time.Time
}

type thoughtSignatureStore struct {
	mutex   sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]thoughtSignatureEntry
}

func newThoughtSignatureStore(ttl time.Duration, now func() time.Time) *thoughtSignatureStore {
	if ttl <= 0 {
		ttl = thoughtSignatureTTL
	}
	if now == nil {
		now = time.Now
	}
	return &thoughtSignatureStore{
		ttl:     ttl,
		now:     now,
		entries: make(map[string]thoughtSignatureEntry),
	}
}

func (store *thoughtSignatureStore) put(id, signature string) {
	if id == "" || signature == "" {
		return
	}

	store.mutex.Lock()
	defer store.mutex.Unlock()

	now := store.now()
	store.removeExpired(now)
	store.entries[id] = thoughtSignatureEntry{
		signature: signature,
		expiresAt: now.Add(store.ttl),
	}
}

func (store *thoughtSignatureStore) get(id string) (string, bool) {
	store.mutex.Lock()
	defer store.mutex.Unlock()

	now := store.now()
	store.removeExpired(now)
	entry, ok := store.entries[id]
	return entry.signature, ok
}

func restoreThoughtSignatures(body []byte, store *thoughtSignatureStore) ([]byte, bool) {
	if store == nil {
		return body, false
	}

	var request map[string]json.RawMessage
	if err := json.Unmarshal(body, &request); err != nil {
		return body, false
	}

	messagesBody, ok := request["messages"]
	if !ok {
		return body, false
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(messagesBody, &messages); err != nil {
		return body, false
	}

	changed := false
	for messageIndex, messageBody := range messages {
		var message map[string]json.RawMessage
		if err := json.Unmarshal(messageBody, &message); err != nil {
			continue
		}
		var role string
		if err := json.Unmarshal(message["role"], &role); err != nil || role != "assistant" {
			continue
		}

		toolCallsBody, ok := message["tool_calls"]
		if !ok {
			continue
		}
		var toolCalls []json.RawMessage
		if err := json.Unmarshal(toolCallsBody, &toolCalls); err != nil {
			continue
		}

		messageChanged := false
		for callIndex, callBody := range toolCalls {
			var toolCall map[string]json.RawMessage
			if err := json.Unmarshal(callBody, &toolCall); err != nil {
				continue
			}
			var id string
			if err := json.Unmarshal(toolCall["id"], &id); err != nil || id == "" || hasThoughtSignature(toolCall["extra_content"]) {
				continue
			}
			signature, found := store.get(id)
			if !found {
				continue
			}
			extraContent, err := withThoughtSignature(toolCall["extra_content"], signature)
			if err != nil {
				continue
			}
			toolCall["extra_content"] = extraContent
			encodedToolCall, err := json.Marshal(toolCall)
			if err != nil {
				continue
			}
			toolCalls[callIndex] = encodedToolCall
			messageChanged = true
		}

		if !messageChanged {
			continue
		}
		encodedToolCalls, err := json.Marshal(toolCalls)
		if err != nil {
			continue
		}
		message["tool_calls"] = encodedToolCalls
		encodedMessage, err := json.Marshal(message)
		if err != nil {
			continue
		}
		messages[messageIndex] = encodedMessage
		changed = true
	}

	if !changed {
		return body, false
	}
	encodedMessages, err := json.Marshal(messages)
	if err != nil {
		return body, false
	}
	request["messages"] = encodedMessages
	recoveredBody, err := json.Marshal(request)
	if err != nil {
		return body, false
	}
	return recoveredBody, true
}

func hasThoughtSignature(extraContentBody json.RawMessage) bool {
	var extraContent map[string]json.RawMessage
	if err := json.Unmarshal(extraContentBody, &extraContent); err != nil {
		return false
	}
	var google map[string]json.RawMessage
	if err := json.Unmarshal(extraContent["google"], &google); err != nil {
		return false
	}
	var signature string
	return json.Unmarshal(google["thought_signature"], &signature) == nil && signature != ""
}

func withThoughtSignature(extraContentBody json.RawMessage, signature string) (json.RawMessage, error) {
	extraContent := make(map[string]json.RawMessage)
	if len(extraContentBody) > 0 && !isJSONNull(extraContentBody) {
		if err := json.Unmarshal(extraContentBody, &extraContent); err != nil {
			return nil, err
		}
	}
	google := make(map[string]json.RawMessage)
	if googleBody := extraContent["google"]; len(googleBody) > 0 && !isJSONNull(googleBody) {
		if err := json.Unmarshal(googleBody, &google); err != nil {
			return nil, err
		}
	}
	encodedSignature, err := json.Marshal(signature)
	if err != nil {
		return nil, err
	}
	google["thought_signature"] = encodedSignature
	encodedGoogle, err := json.Marshal(google)
	if err != nil {
		return nil, err
	}
	extraContent["google"] = encodedGoogle
	return json.Marshal(extraContent)
}

func isJSONNull(body json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(body), []byte("null"))
}

func captureThoughtSignatures(body []byte, store *thoughtSignatureStore) {
	if store == nil {
		return
	}

	var response struct {
		Choices []struct {
			Message signatureToolCallContainer `json:"message"`
			Delta   signatureToolCallContainer `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return
	}
	for _, choice := range response.Choices {
		captureToolCallSignatures(choice.Message.ToolCalls, store)
		captureToolCallSignatures(choice.Delta.ToolCalls, store)
	}
}

type signatureToolCallContainer struct {
	ToolCalls []signatureToolCall `json:"tool_calls"`
}

type signatureToolCall struct {
	ID           string `json:"id"`
	ExtraContent struct {
		Google struct {
			ThoughtSignature string `json:"thought_signature"`
		} `json:"google"`
	} `json:"extra_content"`
}

func captureToolCallSignatures(toolCalls []signatureToolCall, store *thoughtSignatureStore) {
	for _, toolCall := range toolCalls {
		store.put(toolCall.ID, toolCall.ExtraContent.Google.ThoughtSignature)
	}
}

func captureThoughtSignaturesFromSSEEvent(event []byte, store *thoughtSignatureStore) {
	var dataLines [][]byte
	for _, line := range bytes.Split(event, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		dataLines = append(dataLines, bytes.TrimPrefix(line, []byte("data: ")))
	}
	payload := bytes.Join(dataLines, []byte("\n"))
	if bytes.Equal(payload, []byte("[DONE]")) {
		return
	}
	captureThoughtSignatures(payload, store)
}

type thoughtSignatureCapturingReadCloser struct {
	io.ReadCloser
	pending []byte
	store   *thoughtSignatureStore
}

func newThoughtSignatureCapturingReadCloser(body io.ReadCloser, store *thoughtSignatureStore) io.ReadCloser {
	return &thoughtSignatureCapturingReadCloser{ReadCloser: body, store: store}
}

func (body *thoughtSignatureCapturingReadCloser) Read(buffer []byte) (int, error) {
	n, err := body.ReadCloser.Read(buffer)
	if n > 0 {
		body.pending = append(body.pending, buffer[:n]...)
		body.captureCompleteEvents()
	}
	return n, err
}

func (body *thoughtSignatureCapturingReadCloser) captureCompleteEvents() {
	for {
		eventEnd, delimiterLength := nextSSEEventEnd(body.pending)
		if eventEnd < 0 {
			return
		}
		captureThoughtSignaturesFromSSEEvent(body.pending[:eventEnd], body.store)
		body.pending = body.pending[eventEnd+delimiterLength:]
	}
}

func nextSSEEventEnd(body []byte) (int, int) {
	lfEnd := bytes.Index(body, []byte("\n\n"))
	crlfEnd := bytes.Index(body, []byte("\r\n\r\n"))
	if lfEnd < 0 {
		if crlfEnd < 0 {
			return -1, 0
		}
		return crlfEnd, len("\r\n\r\n")
	}
	if crlfEnd < 0 || lfEnd < crlfEnd {
		return lfEnd, len("\n\n")
	}
	return crlfEnd, len("\r\n\r\n")
}

const maxThoughtSignatureJSONResponseBytes = 1 << 20

func captureThoughtSignaturesFromJSONResponse(body io.ReadCloser, store *thoughtSignatureStore) io.ReadCloser {
	prefix, err := io.ReadAll(io.LimitReader(body, maxThoughtSignatureJSONResponseBytes+1))
	if err != nil || len(prefix) > maxThoughtSignatureJSONResponseBytes {
		return &preservingReadCloser{Reader: io.MultiReader(bytes.NewReader(prefix), body), closer: body}
	}
	captureThoughtSignatures(prefix, store)
	return &preservingReadCloser{Reader: bytes.NewReader(prefix), closer: body}
}

type preservingReadCloser struct {
	io.Reader
	closer io.Closer
}

func (body *preservingReadCloser) Close() error {
	return body.closer.Close()
}

func (store *thoughtSignatureStore) removeExpired(now time.Time) {
	for id, entry := range store.entries {
		if !entry.expiresAt.After(now) {
			delete(store.entries, id)
		}
	}
}
