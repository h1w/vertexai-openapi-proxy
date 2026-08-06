# Vertex tool thought-signature recovery — design

## Goal

Make the OpenAI-compatible `/v1/chat/completions` route work with Vertex Gemini tool calls for clients that preserve standard OpenAI tool-call fields but discard Vertex's opaque `extra_content.google.thought_signature`. This includes Pi.

## Root cause

Vertex emits a `thought_signature` within a streamed tool call. The signature is required when that assistant tool call is included in the following request together with tool results. Pi deliberately models only standard OpenAI tool-call fields, so it drops `extra_content`; Vertex then rejects the next request with `INVALID_ARGUMENT`.

## Scope

The proxy captures Vertex thought signatures from Chat Completions responses and restores a missing signature to matching assistant tool calls on later Chat Completions requests. It leaves every other OpenAI request and response field unchanged.

This does not claim that every publisher model supports tools or image input. Capability remains a model and upstream concern.

## Components and data flow

### Thought-signature store

A concurrency-safe, bounded-lifetime in-memory store maps `tool_call.id` to the signature emitted by Vertex. Entries expire after a fixed short TTL. Capturing a newer signature for the same ID replaces the old value. Expired entries are removed during normal store access; no background goroutine is needed.

The signature is opaque. The proxy neither interprets nor logs it.

### Inbound recovery

For `POST /v1/chat/completions`, the proxy reads and parses the JSON body. It visits assistant messages' `tool_calls` and, where a tool call has a known ID but lacks `extra_content.google.thought_signature`, injects the stored value. A signature supplied by the client is preserved unchanged. Malformed JSON is passed through untouched so Vertex retains authority over request validation.

### Upstream capture

For non-streaming JSON responses, the proxy parses a copy of the response body and records signatures in tool calls. For Server-Sent Event streams, it observes complete `data:` events while forwarding their original bytes and records a signature whenever it appears. It does not buffer the full stream, delay headers, or alter event framing.

## Errors and boundaries

- Missing or expired entries remain absent; the upstream receives the original client request and can return its authoritative error.
- Store lookup and capture failures are local no-ops; they cannot corrupt a request or response.
- The proxy never exposes or logs signatures.
- The existing ADC authentication, client API-key middleware, streaming semantics, upstream statuses, and headers remain unchanged.

## Verification

Unit tests cover:

1. Capturing a signature from an SSE tool-call event and restoring it in a subsequent tool-result request with the same ID.
2. Multiple tool calls and signatures in one response.
3. Preserving a client-provided signature.
4. Leaving unknown and expired IDs untouched.
5. Preserving the original SSE bytes and response streaming behavior.

An integration smoke test configures Pi with the proxy, asks Gemini to use `read`, and verifies that Pi completes the tool-result turn without the Vertex missing-signature `400`.
