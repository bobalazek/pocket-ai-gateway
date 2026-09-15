import type { PlaygroundProtocol } from "@/features/playground/types/playground.types";
import { gatewayTransport, type RequestOptions } from "@/lib/api-client";

function request(protocol: PlaygroundProtocol, key: string, model: string, prompt: string, stream: boolean, signal?: AbortSignal) {
  if (protocol === "openai") return { path: "/api/openai/v1/chat/completions", options: { method: "POST", authorization: `Bearer ${key}`, redirectOnUnauthorized: false, signal, body: { model, stream, messages: [{ role: "user", content: prompt }] } } satisfies RequestOptions };
  if (protocol === "anthropic") return { path: "/api/anthropic/v1/messages", options: { method: "POST", anthropicKey: key, redirectOnUnauthorized: false, signal, body: { model, stream, max_tokens: 256, messages: [{ role: "user", content: prompt }] } } satisfies RequestOptions };
  return { path: `/api/gemini/v1beta/models/${encodeURIComponent(model)}:${stream ? "streamGenerateContent" : "generateContent"}`, options: { method: "POST", geminiKey: key, redirectOnUnauthorized: false, signal, body: { contents: [{ role: "user", parts: [{ text: prompt }] }] } } satisfies RequestOptions };
}

export const playgroundClient = {
  generate: (protocol: PlaygroundProtocol, key: string, model: string, prompt: string, signal?: AbortSignal) => {
    const value = request(protocol, key, model, prompt, false, signal);
    return gatewayTransport.request<unknown>(value.path, value.options);
  },
  stream: (protocol: PlaygroundProtocol, key: string, model: string, prompt: string, onChunk: (chunk: string) => void, signal: AbortSignal) => {
    const value = request(protocol, key, model, prompt, true, signal);
    return gatewayTransport.stream(value.path, value.options, onChunk);
  },
};
