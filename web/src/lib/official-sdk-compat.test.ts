import Anthropic from "@anthropic-ai/sdk";
import { GoogleGenAI } from "@google/genai";
import { spawn, type ChildProcess } from "node:child_process";
import { createInterface } from "node:readline";
import OpenAI from "openai";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";

let gateway: ChildProcess;
let baseURL = "";
let apiKey = "";

beforeAll(async () => {
  gateway = spawn("go", ["run", "./internal/integration/sdkserver"], {
    cwd: new URL("../../../", import.meta.url),
    env: { ...process.env, GOCACHE: process.env.GOCACHE || "/tmp/pocket-ai-gateway-sdk-go-cache" },
    stdio: ["ignore", "pipe", "pipe"],
  });
  const errors: Buffer[] = [];
  gateway.stderr?.on("data", (chunk) => errors.push(chunk));
  const line = await new Promise<string>((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error(`SDK gateway did not start: ${Buffer.concat(errors)}`)), 60_000);
    createInterface({ input: gateway.stdout! }).once("line", (value) => {
      clearTimeout(timeout);
      resolve(value);
    });
    gateway.once("exit", (code) => reject(new Error(`SDK gateway exited ${code}: ${Buffer.concat(errors)}`)));
  });
  ({ url: baseURL, key: apiKey } = JSON.parse(line));
}, 70_000);

afterAll(() => gateway?.kill("SIGTERM"));

const openAI = (key = apiKey) => new OpenAI({ apiKey: key, baseURL: `${baseURL}/api/openai/v1`, maxRetries: 0 });
const anthropic = (key = apiKey) => new Anthropic({ apiKey: key, baseURL: `${baseURL}/api/anthropic`, maxRetries: 0 });
const gemini = (key = apiKey) => new GoogleGenAI({ apiKey: key, httpOptions: { baseUrl: `${baseURL}/api/gemini`, apiVersion: "v1beta" } });
const models = ["target-openai", "target-anthropic", "target-gemini"];

describe("official SDK compatibility through the Go gateway", () => {
  it.each(models)("decodes OpenAI Chat through %s", async (model) => {
    const result = await openAI().chat.completions.create({ model, messages: [{ role: "user", content: "Hi" }] });
    expect(result.choices[0]?.message.content).toBe("Hello");
  });

  it.each(models)("decodes Anthropic Messages through %s", async (model) => {
    const result = await anthropic().messages.create({ model, max_tokens: 8, messages: [{ role: "user", content: "Hi" }] });
    expect(result.content[0]).toMatchObject({ type: "text", text: "Hello" });
  });

  it.each(models)("decodes Google Gen AI through %s", async (model) => {
    const result = await gemini().models.generateContent({ model, contents: "Hi" });
    expect(result.text).toBe("Hello");
  });

  it.each(models)("decodes stateless OpenAI Responses through %s", async (model) => {
    const result = await openAI().responses.create({ model, input: "Hi", store: false });
    expect(result.output_text).toBe("Hello");
  });

  it("decodes each client streaming shape", async () => {
    let text = "";
    for await (const event of await openAI().chat.completions.create({ model: "target-anthropic", messages: [{ role: "user", content: "Hi" }], stream: true })) text += event.choices[0]?.delta.content || "";
    expect(text).toBe("Hello");

    text = "";
    for await (const event of await anthropic().messages.create({ model: "target-anthropic", max_tokens: 8, messages: [{ role: "user", content: "Hi" }], stream: true })) {
      if (event.type === "content_block_delta" && event.delta.type === "text_delta") text += event.delta.text;
    }
    expect(text).toBe("Hello");

    text = "";
    for await (const event of await gemini().models.generateContentStream({ model: "target-openai", contents: "Hi" })) text += event.text || "";
    expect(text).toBe("Hello");
  });

  it("lets each SDK decode its native authentication error", async () => {
    await expect(openAI("bad").chat.completions.create({ model: models[0], messages: [{ role: "user", content: "Hi" }] })).rejects.toMatchObject({ status: 401 });
    await expect(anthropic("bad").messages.create({ model: models[1], max_tokens: 8, messages: [{ role: "user", content: "Hi" }] })).rejects.toMatchObject({ status: 401 });
    await expect(gemini("bad").models.generateContent({ model: models[2], contents: "Hi" })).rejects.toBeTruthy();
  });
});

describe("official SDK base URL contracts", () => {
  it("keeps every namespace and version segment exactly once", async () => {
    const urls: string[] = [];
    const fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = input instanceof Request ? input.url : String(input);
      urls.push(url);
      if (url.includes("anthropic")) return new Response(JSON.stringify({ id: "msg_1", type: "message", role: "assistant", content: [], stop_reason: "end_turn", usage: { input_tokens: 1, output_tokens: 0 } }));
      if (url.includes("gemini")) return new Response(JSON.stringify({ candidates: [{ content: { parts: [{ text: "Hi" }] } }] }));
      return new Response(JSON.stringify({ id: "chat_1", choices: [{ message: { role: "assistant", content: "Hi" } }] }));
    });
    await new OpenAI({ apiKey: "key", baseURL: "http://gateway.test/api/openai/v1", fetch }).chat.completions.create({ model: "m", messages: [{ role: "user", content: "Hi" }] });
    await new Anthropic({ apiKey: "key", baseURL: "http://gateway.test/api/anthropic", fetch }).messages.create({ model: "m", max_tokens: 1, messages: [{ role: "user", content: "Hi" }] });
    vi.stubGlobal("fetch", fetch);
    await new GoogleGenAI({ apiKey: "key", httpOptions: { baseUrl: "http://gateway.test/api/gemini", apiVersion: "v1beta" } }).models.generateContent({ model: "m", contents: "Hi" });
    vi.unstubAllGlobals();
    expect(urls).toEqual([
      "http://gateway.test/api/openai/v1/chat/completions",
      "http://gateway.test/api/anthropic/v1/messages",
      "http://gateway.test/api/gemini/v1beta/models/m:generateContent",
    ]);
  });
});
