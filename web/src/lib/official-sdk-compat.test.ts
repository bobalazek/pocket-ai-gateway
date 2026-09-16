import Anthropic from "@anthropic-ai/sdk";
import { GoogleGenAI } from "@google/genai";
import { spawn, type ChildProcess } from "node:child_process";
import { createInterface } from "node:readline";
import OpenAI, { toFile } from "openai";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";

let gateway: ChildProcess;
let baseURL = "";
let apiKey = "";
let otherApiKey = "";
let noFilesApiKey = "";
let noBatchesApiKey = "";

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
  ({
    url: baseURL,
    key: apiKey,
    otherKey: otherApiKey,
    noFilesKey: noFilesApiKey,
    noBatchesKey: noBatchesApiKey,
  } = JSON.parse(line));
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

  it("decodes native OpenAI legacy Completions", async () => {
    const result = await openAI().completions.create({
      model: "target-openai-completion",
      prompt: "Complete this",
      max_tokens: 8,
      logprobs: 2,
    });

    expect(result).toMatchObject({
      id: "cmpl_1",
      object: "text_completion",
      model: "target-openai-completion",
      choices: [{ text: "Legacy completion", index: 0, logprobs: null, finish_reason: "stop" }],
      usage: { prompt_tokens: 3, completion_tokens: 2, total_tokens: 5 },
    });
  });

  it("streams native OpenAI legacy Completions with terminal usage", async () => {
    const stream = await openAI().completions.create({
      model: "target-openai-completion",
      prompt: "Complete this",
      max_tokens: 8,
      stream: true,
      stream_options: { include_obfuscation: false, include_usage: true },
    });
    const chunks = [];
    for await (const chunk of stream) chunks.push(chunk);

    expect(chunks.flatMap((chunk) => chunk.choices).map((choice) => choice.text).join("")).toBe("Legacy completion");
    expect(chunks.every((chunk) => chunk.model === "target-openai-completion")).toBe(true);
    expect(chunks.at(-1)).toMatchObject({
      object: "text_completion",
      model: "target-openai-completion",
      choices: [],
      usage: { prompt_tokens: 3, completion_tokens: 2, total_tokens: 5 },
    });
  });

  it("manages gateway-stored Chat Completions", async () => {
    const client = openAI();
    const created = await client.chat.completions.create({
      model: "target-anthropic",
      messages: [{ role: "user", content: "Store this" }],
      store: true,
      metadata: { suite: "sdk" },
    });
    expect(created).toMatchObject({ object: "chat.completion", model: "target-anthropic", metadata: { suite: "sdk" } });
    expect(created.id).toMatch(/^chatcmpl_/);
    expect((await client.chat.completions.retrieve(created.id)).id).toBe(created.id);
    expect(await client.chat.completions.update(created.id, { metadata: { suite: "updated" } })).toMatchObject({ metadata: { suite: "updated" } });
    const listed = await client.chat.completions.list({ model: "target-anthropic", metadata: { suite: "updated" } });
    expect(listed.data.map((item) => item.id)).toContain(created.id);
    const messages = await client.chat.completions.messages.list(created.id);
    expect(messages.data[0]).toMatchObject({ role: "user", content: "Store this", content_parts: null });
    expect(await client.chat.completions.delete(created.id)).toMatchObject({ id: created.id, object: "chat.completion.deleted", deleted: true });
    await expect(client.chat.completions.retrieve(created.id)).rejects.toMatchObject({ status: 404 });
  });

  it("manages gateway-owned OpenAI batch files", async () => {
    const client = openAI();
    const contents = [
      Buffer.from('{"custom_id":"request-1","method":"POST","url":"/v1/responses","body":{"model":"target-openai","input":"Hello"}}\n'),
      Buffer.from('{"custom_id":"request-2","method":"POST","url":"/v1/responses","body":{"model":"target-openai","input":"World"}}\n'),
      Buffer.from('{"custom_id":"request-3","method":"POST","url":"/v1/responses","body":{"model":"target-openai","input":"Again"}}\n'),
    ];
    const created = [];
    for (const [index, content] of contents.entries()) {
      created.push(await client.files.create({
        file: await toFile(content, `batch-${index + 1}.jsonl`, { type: "application/jsonl" }),
        purpose: "batch",
        expires_after: { anchor: "created_at", seconds: 3600 },
      }));
    }

    expect(created[0]).toMatchObject({
      object: "file",
      bytes: contents[0].length,
      filename: "batch-1.jsonl",
      purpose: "batch",
      status: "processed",
    });
    expect(created[0].id).toMatch(/^file_/);
    expect(created[0].expires_at).toBe(created[0].created_at + 3600);
    expect(await client.files.waitForProcessing(created[0].id, { pollInterval: 1, maxWait: 1000 })).toMatchObject({
      id: created[0].id,
      status: "processed",
    });
    expect((await client.files.retrieve(created[0].id)).id).toBe(created[0].id);

    const userData = await client.files.create({
      file: await toFile(Buffer.from("gateway notes"), "notes.txt", { type: "text/plain" }),
      purpose: "user_data",
    });
    expect(userData).toMatchObject({ filename: "notes.txt", purpose: "user_data", status: "processed" });
    expect((await client.files.list({ purpose: "user_data" })).data.map((file) => file.id)).toContain(userData.id);

    const ascending = await client.files.list({ purpose: "batch", order: "asc", limit: 100 });
    const descending = await client.files.list({ purpose: "batch", order: "desc", limit: 100 });
    expect(descending.data.map((file) => file.id)).toEqual(ascending.data.map((file) => file.id).reverse());
    expect((await client.files.list({ purpose: "batch_output" })).data).toEqual([]);
    const firstPage = await client.files.list({ purpose: "batch", order: "asc", limit: 1 });
    expect(firstPage.data).toHaveLength(1);
    expect(firstPage.has_more).toBe(true);
    const automaticallyPaginated = [];
    for await (const file of client.files.list({ purpose: "batch", order: "asc", limit: 1 })) {
      automaticallyPaginated.push(file.id);
    }
    expect(automaticallyPaginated).toEqual(ascending.data.map((file) => file.id));

    const downloaded = await client.files.content(created[0].id);
    expect(Buffer.from(await downloaded.arrayBuffer())).toEqual(contents[0]);
    await expect(openAI(otherApiKey).files.retrieve(created[0].id)).rejects.toMatchObject({ status: 404 });
    await expect(openAI(noFilesApiKey).files.list()).rejects.toMatchObject({ status: 403 });
    expect(await client.files.delete(created[0].id)).toEqual({ id: created[0].id, object: "file", deleted: true });
    await expect(client.files.retrieve(created[0].id)).rejects.toMatchObject({ status: 404 });
    expect(await client.files.delete(userData.id)).toMatchObject({ id: userData.id, deleted: true });
  });

  it("manages gateway-owned OpenAI Vector Store metadata", async () => {
    const client = openAI();
    const first = await client.vectorStores.create({
      name: "Knowledge",
      description: "Product documentation",
      metadata: { suite: "sdk" },
      expires_after: { anchor: "last_active_at", days: 7 },
    });
    const second = await client.vectorStores.create({ name: "Archive" });

    expect(first).toMatchObject({
      object: "vector_store",
      name: "Knowledge",
      status: "completed",
      usage_bytes: 0,
      metadata: { suite: "sdk" },
      expires_after: { anchor: "last_active_at", days: 7 },
      file_counts: { in_progress: 0, completed: 0, failed: 0, cancelled: 0, total: 0 },
    });
    expect(first.id).toMatch(/^vs_/);
    expect((await client.vectorStores.retrieve(first.id)).id).toBe(first.id);
    expect(await client.vectorStores.update(first.id, { name: "Updated", metadata: { suite: "updated" }, expires_after: null })).toMatchObject({
      id: first.id,
      name: "Updated",
      metadata: { suite: "updated" },
    });
    const page = await client.vectorStores.list({ limit: 1, order: "asc" });
    expect(page.data).toHaveLength(1);
    expect(page.has_more).toBe(true);
    const listed = [];
    for await (const store of client.vectorStores.list({ limit: 1, order: "asc" })) listed.push(store.id);
    expect(listed).toEqual(expect.arrayContaining([first.id, second.id]));

    const source = await client.files.create({
      file: await toFile(Buffer.from("vector store notes"), "vector-store.txt", { type: "text/plain" }),
      purpose: "user_data",
    });
    const attached = await client.vectorStores.files.create(first.id, {
      file_id: source.id,
      attributes: { suite: "sdk", priority: 2, active: true },
      chunking_strategy: { type: "auto" },
    });
    expect(attached).toMatchObject({
      id: source.id,
      object: "vector_store.file",
      vector_store_id: first.id,
      status: "completed",
      usage_bytes: source.bytes,
      attributes: { suite: "sdk", priority: 2, active: true },
      chunking_strategy: { type: "other" },
    });
    expect(await client.vectorStores.files.retrieve(source.id, { vector_store_id: first.id })).toMatchObject({ id: source.id });
    expect(await client.vectorStores.files.update(source.id, { vector_store_id: first.id, attributes: { suite: "updated" } })).toMatchObject({
      attributes: { suite: "updated" },
    });
    expect((await client.vectorStores.files.list(first.id)).data.map((file) => file.id)).toContain(source.id);
    const parsed = [];
    for await (const item of client.vectorStores.files.content(source.id, { vector_store_id: first.id })) parsed.push(item);
    expect(parsed).toEqual([{ type: "text", text: "vector store notes" }]);
    const search = await client.vectorStores.search(first.id, {
      query: "vector notes",
      filters: { type: "eq", key: "suite", value: "updated" },
      max_num_results: 5,
      ranking_options: { ranker: "none", score_threshold: 0.1 },
    });
    expect(search.object).toBe("vector_store.search_results.page");
    expect(search.data[0]).toMatchObject({ file_id: source.id, filename: "vector-store.txt", attributes: { suite: "updated" } });
    expect(search.data[0]?.score).toBeGreaterThan(0);
    expect(search.data[0]?.content).toEqual([{ type: "text", text: "vector store notes" }]);
    expect(await client.vectorStores.retrieve(first.id)).toMatchObject({
      usage_bytes: source.bytes,
      file_counts: { completed: 1, total: 1 },
    });
    await expect(openAI(otherApiKey).vectorStores.retrieve(first.id)).rejects.toMatchObject({ status: 404 });
    await expect(openAI(otherApiKey).vectorStores.files.retrieve(source.id, { vector_store_id: first.id })).rejects.toMatchObject({ status: 404 });
    await expect(openAI(noFilesApiKey).vectorStores.list()).rejects.toMatchObject({ status: 403 });
    await expect(client.vectorStores.create({ file_ids: ["file_missing"] })).rejects.toMatchObject({ status: 400, code: "unsupported_feature" });
    expect(await client.vectorStores.files.delete(source.id, { vector_store_id: first.id })).toEqual({ id: source.id, object: "vector_store.file.deleted", deleted: true });
    expect((await client.vectorStores.retrieve(first.id)).file_counts.total).toBe(0);
    await client.files.delete(source.id);
    expect(await client.vectorStores.delete(first.id)).toEqual({ id: first.id, object: "vector_store.deleted", deleted: true });
    expect(await client.vectorStores.delete(second.id)).toEqual({ id: second.id, object: "vector_store.deleted", deleted: true });
  });

  it("assembles gateway-owned OpenAI Uploads in the requested Part order", async () => {
    const client = openAI();
    const upload = await client.uploads.create({
      bytes: 11,
      filename: "assembled.jsonl",
      mime_type: "application/jsonl",
      purpose: "batch",
      expires_after: { anchor: "created_at", seconds: 3600 },
    });
    const last = await client.uploads.parts.create(upload.id, { data: await toFile(Buffer.from("world"), "last") });
    const first = await client.uploads.parts.create(upload.id, { data: await toFile(Buffer.from("hello "), "first") });
    const completed = await client.uploads.complete(upload.id, { part_ids: [first.id, last.id] });

    expect(completed).toMatchObject({ id: upload.id, object: "upload", bytes: 11, status: "completed" });
    expect(completed.file).toMatchObject({ object: "file", filename: "assembled.jsonl", purpose: "batch", status: "processed" });
    if (!completed.file) throw new Error("Completed Upload File is missing");
    expect(Buffer.from(await (await client.files.content(completed.file.id)).arrayBuffer())).toEqual(Buffer.from("hello world"));
    await expect(openAI(otherApiKey).uploads.cancel(upload.id)).rejects.toMatchObject({ status: 404 });

    const cancelled = await client.uploads.create({ bytes: 1, filename: "cancelled.jsonl", mime_type: "application/jsonl", purpose: "batch" });
    expect(await client.uploads.cancel(cancelled.id)).toMatchObject({ id: cancelled.id, status: "cancelled" });
    await expect(client.uploads.parts.create(cancelled.id, { data: await toFile(Buffer.from("x"), "rejected") })).rejects.toMatchObject({ status: 400 });
  });

  it("runs gateway-owned OpenAI Responses, Chat Completions, legacy Completions, Embeddings, Moderation, and Image batches", async () => {
    const client = openAI();
    const upload = async (content: string, name: string) => client.files.create({
      file: await toFile(Buffer.from(content), name, { type: "application/jsonl" }),
      purpose: "batch",
    });
    const waitForTerminal = async (id: string) => {
      for (let attempt = 0; attempt < 400; attempt += 1) {
        const batch = await client.batches.retrieve(id);
        if (["completed", "failed", "expired", "cancelled"].includes(batch.status)) return batch;
        await new Promise((resolve) => setTimeout(resolve, 25));
      }
      throw new Error(`Batch ${id} did not reach a terminal state`);
    };
    const readJSONLines = async (id: string) => {
      const content = (await (await client.files.content(id)).text()).trim();
      return content ? content.split("\n").map((line) => JSON.parse(line) as Record<string, unknown>) : [];
    };

    const input = await upload([
      '{"custom_id":"success","method":"POST","url":"/v1/responses","body":{"model":"target-openai","input":"Batch success","max_output_tokens":8}}',
      '{"custom_id":"failure","method":"POST","url":"/v1/responses","body":{"model":"target-openai","input":"Batch fail","max_output_tokens":8}}',
      "",
    ].join("\n"), "responses-batch.jsonl");
    const created = await client.batches.create({
      input_file_id: input.id,
      endpoint: "/v1/responses",
      completion_window: "24h",
      metadata: { suite: "sdk" },
      output_expires_after: { anchor: "created_at", seconds: 3600 },
    });
    expect(created).toMatchObject({
      object: "batch",
      endpoint: "/v1/responses",
      input_file_id: input.id,
      completion_window: "24h",
      model: "target-openai",
      metadata: { suite: "sdk" },
    });
    expect(created.id).toMatch(/^batch_/);

    const completed = await waitForTerminal(created.id);
    expect(completed).toMatchObject({
      id: created.id,
      status: "completed",
      request_counts: { total: 2, completed: 1, failed: 1 },
    });
    if (!completed.output_file_id || !completed.error_file_id) throw new Error("Batch result Files are missing");
    const successLines = await readJSONLines(completed.output_file_id);
    const errorLines = await readJSONLines(completed.error_file_id);
    expect(successLines).toHaveLength(1);
    expect(successLines[0]).toMatchObject({
      custom_id: "success",
      response: {
        status_code: 200,
        body: { object: "response", status: "completed", model: "target-openai" },
      },
      error: null,
    });
    expect(successLines[0]).toMatchObject({ response: { request_id: expect.stringMatching(/^req_/) } });
    expect(errorLines).toHaveLength(1);
    expect(errorLines[0]).toMatchObject({
      custom_id: "failure",
      response: null,
      error: { code: expect.any(String), message: expect.any(String) },
    });
    expect(await client.files.retrieve(completed.output_file_id)).toMatchObject({ purpose: "batch_output" });
    expect(await client.files.retrieve(completed.error_file_id)).toMatchObject({ purpose: "batch_output" });

    const chatInput = await upload([
      '{"custom_id":"chat-success","method":"POST","url":"/v1/chat/completions","body":{"model":"target-openai","messages":[{"role":"user","content":"Batch chat success"}],"max_completion_tokens":8}}',
      "",
    ].join("\n"), "chat-batch.jsonl");
    const chatCreated = await client.batches.create({
      input_file_id: chatInput.id,
      endpoint: "/v1/chat/completions",
      completion_window: "24h",
    });
    const chatCompleted = await waitForTerminal(chatCreated.id);
    expect(chatCompleted).toMatchObject({
      endpoint: "/v1/chat/completions",
      status: "completed",
      request_counts: { total: 1, completed: 1, failed: 0 },
      usage: { input_tokens: 2, output_tokens: 1, total_tokens: 3 },
    });
    if (!chatCompleted.output_file_id) throw new Error("Chat Batch output File is missing");
    expect(chatCompleted.error_file_id).toBeNull();
    expect(await readJSONLines(chatCompleted.output_file_id)).toMatchObject([{
      custom_id: "chat-success",
      response: {
        status_code: 200,
        request_id: expect.stringMatching(/^req_/),
        body: { object: "chat.completion", model: "target-openai", usage: { prompt_tokens: 2, completion_tokens: 1, total_tokens: 3 } },
      },
      error: null,
    }]);

    const completionInput = await upload(
      '{"custom_id":"completion-success","method":"POST","url":"/v1/completions","body":{"model":"target-openai-completion","prompt":"Batch completion","max_tokens":8}}\n',
      "completion-batch.jsonl",
    );
    const completionCreated = await client.batches.create({
      input_file_id: completionInput.id,
      endpoint: "/v1/completions",
      completion_window: "24h",
    });
    expect(completionCreated).toMatchObject({ endpoint: "/v1/completions", model: "target-openai-completion" });
    const completionCompleted = await waitForTerminal(completionCreated.id);
    expect(completionCompleted).toMatchObject({
      endpoint: "/v1/completions",
      model: "target-openai-completion",
      status: "completed",
      request_counts: { total: 1, completed: 1, failed: 0 },
      usage: { input_tokens: 3, output_tokens: 2, total_tokens: 5 },
    });
    if (!completionCompleted.output_file_id) throw new Error("Completion Batch output File is missing");
    expect(completionCompleted.error_file_id).toBeNull();
    expect(await readJSONLines(completionCompleted.output_file_id)).toMatchObject([{
      custom_id: "completion-success",
      response: {
        status_code: 200,
        request_id: expect.stringMatching(/^req_/),
        body: {
          id: "cmpl_1",
          object: "text_completion",
          model: "target-openai-completion",
          choices: [{ text: "Legacy completion", index: 0, logprobs: null, finish_reason: "stop" }],
          usage: { prompt_tokens: 3, completion_tokens: 2, total_tokens: 5 },
        },
      },
      error: null,
    }]);

    const embeddingInput = await upload(
      '{"custom_id":"embedding-success","method":"POST","url":"/v1/embeddings","body":{"model":"target-openai-embedding","input":["one","two"],"dimensions":2,"encoding_format":"float","user":"sdk"}}\n',
      "embedding-batch.jsonl",
    );
    const embeddingCreated = await client.batches.create({
      input_file_id: embeddingInput.id,
      endpoint: "/v1/embeddings",
      completion_window: "24h",
    });
    const embeddingCompleted = await waitForTerminal(embeddingCreated.id);
    expect(embeddingCompleted).toMatchObject({
      endpoint: "/v1/embeddings",
      status: "completed",
      request_counts: { total: 1, completed: 1, failed: 0 },
      usage: { input_tokens: 2, output_tokens: 0, total_tokens: 2 },
    });
    if (!embeddingCompleted.output_file_id) throw new Error("Embedding Batch output File is missing");
    expect(await readJSONLines(embeddingCompleted.output_file_id)).toMatchObject([{
      custom_id: "embedding-success",
      response: {
        status_code: 200,
        request_id: expect.stringMatching(/^req_/),
        body: {
          object: "list",
          model: "target-openai-embedding",
          data: [
            { object: "embedding", embedding: [0.1, 0.2], index: 0 },
            { object: "embedding", embedding: [0.3, 0.4], index: 1 },
          ],
          usage: { prompt_tokens: 2, total_tokens: 2 },
        },
      },
      error: null,
    }]);

    const moderationInput = await upload(
      '{"custom_id":"moderation-success","method":"POST","url":"/v1/moderations","body":{"model":"target-openai","input":["violent text"]}}\n',
      "moderation-batch.jsonl",
    );
    const moderationCreated = await client.batches.create({
      input_file_id: moderationInput.id,
      endpoint: "/v1/moderations",
      completion_window: "24h",
    });
    const moderationCompleted = await waitForTerminal(moderationCreated.id);
    expect(moderationCompleted).toMatchObject({
      endpoint: "/v1/moderations",
      status: "completed",
      request_counts: { total: 1, completed: 1, failed: 0 },
      usage: null,
    });
    if (!moderationCompleted.output_file_id) throw new Error("Moderation Batch output File is missing");
    expect(await readJSONLines(moderationCompleted.output_file_id)).toMatchObject([{
      custom_id: "moderation-success",
      response: {
        status_code: 200,
        request_id: expect.stringMatching(/^req_/),
        body: {
          id: "modr_1",
          model: "target-openai",
          results: [{ flagged: true, categories: { violence: true }, category_scores: { violence: 0.9 } }],
        },
      },
      error: null,
    }]);

    const imageInput = await upload(
      '{"custom_id":"image-success","method":"POST","url":"/v1/images/generations","body":{"model":"target-openai","prompt":"A black dot","n":1,"output_compression":80}}\n',
      "image-batch.jsonl",
    );
    const imageCreated = await client.batches.create({
      input_file_id: imageInput.id,
      endpoint: "/v1/images/generations",
      completion_window: "24h",
    });
    expect(imageCreated).toMatchObject({ endpoint: "/v1/images/generations", model: "target-openai" });
    const imageCompleted = await waitForTerminal(imageCreated.id);
    expect(imageCompleted).toMatchObject({
      endpoint: "/v1/images/generations",
      model: "target-openai",
      status: "completed",
      request_counts: { total: 1, completed: 1, failed: 0 },
      usage: { input_tokens: 5, output_tokens: 7, total_tokens: 12 },
    });
    if (!imageCompleted.output_file_id) throw new Error("Image Generation Batch output File is missing");
    const imageLines = await readJSONLines(imageCompleted.output_file_id);
    expect(imageLines).toMatchObject([{
      custom_id: "image-success",
      response: {
        status_code: 200,
        request_id: expect.stringMatching(/^req_/),
        body: {
          created: 1764967971,
          data: [{ b64_json: "eA==" }],
          usage: { input_tokens: 5, output_tokens: 7, total_tokens: 12 },
        },
      },
      error: null,
    }]);
    expect((imageLines[0]?.response as { body?: Record<string, unknown> })?.body).not.toHaveProperty("model");

    const imageEditInput = await upload(
      '{"custom_id":"image-edit-success","method":"POST","url":"/v1/images/edits","body":{"model":"target-openai-edit","images":[{"image_url":"https://example.com/source.png"},{"image_url":"data:image/png;base64,iVBORw0KGgo="}],"prompt":"Add a hat","n":1}}\n',
      "image-edit-batch.jsonl",
    );
    const imageEditCreated = await client.batches.create({
      input_file_id: imageEditInput.id,
      endpoint: "/v1/images/edits",
      completion_window: "24h",
    });
    expect(imageEditCreated).toMatchObject({ endpoint: "/v1/images/edits", model: "target-openai-edit" });
    const imageEditCompleted = await waitForTerminal(imageEditCreated.id);
    expect(imageEditCompleted).toMatchObject({
      endpoint: "/v1/images/edits",
      model: "target-openai-edit",
      status: "completed",
      request_counts: { total: 1, completed: 1, failed: 0 },
      usage: { input_tokens: 3, output_tokens: 2, total_tokens: 5 },
    });
    if (!imageEditCompleted.output_file_id) throw new Error("Image Edit Batch output File is missing");
    const imageEditLines = await readJSONLines(imageEditCompleted.output_file_id);
    expect(imageEditLines).toMatchObject([{
      custom_id: "image-edit-success",
      response: {
        status_code: 200,
        request_id: expect.stringMatching(/^req_/),
        body: {
          created: 1764967971,
          data: [{ b64_json: "ZWRpdA==" }],
          usage: { input_tokens: 3, output_tokens: 2, total_tokens: 5 },
        },
      },
      error: null,
    }]);
    expect((imageEditLines[0]?.response as { body?: Record<string, unknown> })?.body).not.toHaveProperty("model");

    const blockerInput = await upload(
      '{"custom_id":"blocker","method":"POST","url":"/v1/responses","body":{"model":"target-openai","input":"Cancel me blocker","max_output_tokens":8}}\n',
      "blocker-batch.jsonl",
    );
    await client.batches.create({
      input_file_id: blockerInput.id,
      endpoint: "/v1/responses",
      completion_window: "24h",
    });
    await new Promise((resolve) => setTimeout(resolve, 75));
    const cancelInput = await upload(
      '{"custom_id":"cancel","method":"POST","url":"/v1/responses","body":{"model":"target-openai","input":"Queued cancellation","max_output_tokens":8}}\n',
      "cancel-batch.jsonl",
    );
    const pending = await client.batches.create({
      input_file_id: cancelInput.id,
      endpoint: "/v1/responses",
      completion_window: "24h",
    });
    expect(await client.batches.cancel(pending.id)).toMatchObject({
      id: pending.id,
      status: expect.stringMatching(/^(cancelling|cancelled)$/),
    });
    expect((await waitForTerminal(pending.id)).status).toBe("cancelled");

    const firstPage = await client.batches.list({ limit: 1 });
    expect(firstPage.data).toHaveLength(1);
    expect(firstPage.has_more).toBe(true);
    const automaticallyPaginated = [];
    for await (const batch of client.batches.list({ limit: 1 })) automaticallyPaginated.push(batch.id);
    expect(automaticallyPaginated).toEqual(expect.arrayContaining([created.id, pending.id]));

    await expect(openAI(otherApiKey).batches.retrieve(created.id)).rejects.toMatchObject({ status: 404 });
    await expect(openAI(noBatchesApiKey).batches.list()).rejects.toMatchObject({ status: 403 });
    const foreignInput = await openAI(otherApiKey).files.create({
      file: await toFile(Buffer.from('{"custom_id":"foreign","method":"POST","url":"/v1/responses","body":{"model":"target-openai","input":"Foreign"}}\n'), "foreign-batch.jsonl", { type: "application/jsonl" }),
      purpose: "batch",
    });
    await expect(client.batches.create({
      input_file_id: foreignInput.id,
      endpoint: "/v1/responses",
      completion_window: "24h",
    })).rejects.toMatchObject({ status: 404 });
  }, 20_000);

  it.each(models)("decodes Anthropic Messages through %s", async (model) => {
    const result = await anthropic().messages.create({ model, max_tokens: 8, messages: [{ role: "user", content: "Hi" }] });
    expect(result.content[0]).toMatchObject({ type: "text", text: "Hello" });
  });

  it("preserves native Anthropic prompt-cache usage", async () => {
    const result = await anthropic().messages.create({
      model: "target-anthropic",
      max_tokens: 8,
      system: [{ type: "text", text: "Stable system prompt", cache_control: { type: "ephemeral", ttl: "5m" } }],
      messages: [{ role: "user", content: "Hi" }],
    });
    expect(result.usage).toMatchObject({ input_tokens: 2, cache_creation_input_tokens: 3, cache_read_input_tokens: 5, output_tokens: 1 });
  });

  it("preserves bounded native Anthropic basic web search", async () => {
    const result = await anthropic().messages.create({
      model: "target-anthropic",
      max_tokens: 128,
      messages: [{ role: "user", content: "Find a source about Pocket AI Gateway" }],
      tools: [{
        type: "web_search_20250305",
        name: "web_search",
        max_uses: 1,
        allowed_callers: ["direct"],
        allowed_domains: ["example.com"],
        user_location: { type: "approximate", country: "US" },
      }],
    });

    expect(result.model).toBe("target-anthropic");
    expect(result.content.find((block) => block.type === "server_tool_use")).toMatchObject({
      id: "srvtoolu_1",
      name: "web_search",
      caller: { type: "direct" },
      input: { query: "Pocket AI Gateway" },
    });
    expect(result.content.find((block) => block.type === "web_search_tool_result")).toMatchObject({
      tool_use_id: "srvtoolu_1",
      caller: { type: "direct" },
      content: [{
        type: "web_search_result",
        url: "https://example.com/source",
        title: "Example source",
        encrypted_content: "encrypted-result",
      }],
    });
    expect(result.content.find((block) => block.type === "text")).toMatchObject({
      citations: [{
        type: "web_search_result_location",
        url: "https://example.com/source",
        title: "Example source",
        encrypted_index: "encrypted-index",
      }],
    });
    expect(result.usage.server_tool_use?.web_search_requests).toBe(1);
  });

  it("streams bounded native Anthropic basic web search", async () => {
    const stream = anthropic().messages.stream({
      model: "target-anthropic",
      max_tokens: 128,
      messages: [{ role: "user", content: "Find a source about Pocket AI Gateway" }],
      tools: [{
        type: "web_search_20250305",
        name: "web_search",
        max_uses: 1,
        allowed_callers: ["direct"],
      }],
    });
    const events = [];
    for await (const event of stream) events.push(event);
    const result = await stream.finalMessage();

    const serverTool = result.content.find((block) => block.type === "server_tool_use");
    const toolResult = result.content.find((block) => block.type === "web_search_tool_result");
    expect(result.model).toBe("target-anthropic");
    expect(serverTool).toMatchObject({
      name: "web_search",
      caller: { type: "direct" },
      input: { query: "Pocket AI Gateway" },
    });
    expect(toolResult).toMatchObject({
      tool_use_id: serverTool?.id,
      caller: { type: "direct" },
      content: [{
        type: "web_search_result",
        url: "https://example.com/source",
        encrypted_content: "encrypted-result",
      }],
    });
    expect(result.content.find((block) => block.type === "text")).toMatchObject({
      citations: [{
        type: "web_search_result_location",
        url: "https://example.com/source",
        encrypted_index: "encrypted-index",
      }],
    });
    expect(result.usage.server_tool_use?.web_search_requests).toBe(1);

    const terminal = events.find((event) => event.type === "message_delta");
    expect(terminal?.delta.stop_reason).toBe("end_turn");
    expect(terminal?.usage).toMatchObject({
      input_tokens: expect.any(Number),
      output_tokens: expect.any(Number),
      server_tool_use: { web_search_requests: 1 },
    });
  });

  it("preserves bounded native Anthropic web fetch", async () => {
    const result = await anthropic().messages.create({
      model: "target-anthropic",
      max_tokens: 128,
      messages: [{ role: "user", content: "Fetch https://example.com/page" }],
      tools: [{
        type: "web_fetch_20250910",
        name: "web_fetch",
        max_uses: 1,
        max_content_tokens: 1024,
        allowed_domains: ["example.com"],
        citations: { enabled: true },
      }],
    });

    expect(result.model).toBe("target-anthropic");
    expect(result.content.find((block) => block.type === "server_tool_use")).toMatchObject({
      id: "srvtoolu_fetch_1",
      name: "web_fetch",
      caller: { type: "direct" },
      input: { url: "https://example.com/page" },
    });
    expect(result.content.find((block) => block.type === "web_fetch_tool_result")).toMatchObject({
      tool_use_id: "srvtoolu_fetch_1",
      caller: { type: "direct" },
      content: {
        type: "web_fetch_result",
        url: "https://example.com/page",
        retrieved_at: null,
        content: {
          type: "document",
          source: { type: "text", media_type: "text/plain", data: "Fetched page" },
          title: "Example page",
          citations: { enabled: true },
        },
      },
    });
    expect(result.usage).toMatchObject({
      input_tokens: 9,
      output_tokens: 3,
      server_tool_use: { web_fetch_requests: 1, web_search_requests: 0 },
    });
  });

  it("streams bounded native Anthropic web fetch", async () => {
    const stream = anthropic().messages.stream({
      model: "target-anthropic",
      max_tokens: 128,
      messages: [{ role: "user", content: "Fetch https://example.com/page" }],
      tools: [{
        type: "web_fetch_20250910",
        name: "web_fetch",
        max_uses: 1,
        max_content_tokens: 1024,
        blocked_domains: ["blocked.example"],
        citations: { enabled: true },
      }],
    });
    const events = [];
    for await (const event of stream) events.push(event);
    const result = await stream.finalMessage();

    const serverTool = result.content.find((block) => block.type === "server_tool_use");
    expect(result.model).toBe("target-anthropic");
    expect(serverTool).toMatchObject({ name: "web_fetch", input: { url: "https://example.com/page" } });
    expect(result.content.find((block) => block.type === "web_fetch_tool_result")).toMatchObject({
      tool_use_id: serverTool?.id,
      content: {
        type: "web_fetch_result",
        url: "https://example.com/page",
        content: { source: { type: "text", data: "Fetched page" } },
      },
    });
    expect(result.usage.server_tool_use?.web_fetch_requests).toBe(1);
    expect(events.find((event) => event.type === "message_delta")?.usage).toMatchObject({
      input_tokens: expect.any(Number),
      output_tokens: expect.any(Number),
      server_tool_use: { web_fetch_requests: 1, web_search_requests: 0 },
    });
  });

  it("manages gateway-owned Anthropic Message Batches", async () => {
    const client = anthropic();
    const created = await client.messages.batches.create({
      requests: [{
        custom_id: "sdk-success",
        params: {
          model: "target-anthropic",
          max_tokens: 8,
          messages: [{ role: "user", content: "Batch hello" }],
        },
      }],
    });
    expect(created).toMatchObject({
      type: "message_batch",
      processing_status: "in_progress",
      request_counts: {
        processing: 1,
        succeeded: 0,
        errored: 0,
        canceled: 0,
        expired: 0,
      },
      archived_at: null,
      results_url: null,
    });
    expect(created.id).toMatch(/^msgbatch_/);
    expect(Date.parse(created.expires_at) - Date.parse(created.created_at)).toBe(24 * 60 * 60 * 1000);

    let completed = created;
    for (let attempt = 0; attempt < 100 && completed.processing_status !== "ended"; attempt++) {
      await new Promise((resolve) => setTimeout(resolve, 20));
      completed = await client.messages.batches.retrieve(created.id);
    }
    expect(completed).toMatchObject({
      id: created.id,
      processing_status: "ended",
      request_counts: {
        processing: 0,
        succeeded: 1,
        errored: 0,
        canceled: 0,
        expired: 0,
      },
      archived_at: null,
    });
    expect(completed.results_url).toMatch(new RegExp(`^${baseURL}/api/anthropic/v1/messages/batches/${created.id}/results$`));

    const page = await client.messages.batches.list({ limit: 1 });
    expect(page.data.map((batch) => batch.id)).toContain(created.id);
    const results = [];
    for await (const result of await client.messages.batches.results(created.id)) results.push(result);
    expect(results).toEqual([expect.objectContaining({
      custom_id: "sdk-success",
      result: expect.objectContaining({
        type: "succeeded",
        message: {
          id: "msg_1",
          type: "message",
          role: "assistant",
          model: "anthropic-upstream",
          container: null,
          content: [{ type: "text", text: "Hello", citations: null }],
          stop_details: null,
          stop_reason: "end_turn",
          stop_sequence: null,
          usage: {
            cache_creation: null,
            cache_creation_input_tokens: null,
            cache_read_input_tokens: null,
            inference_geo: null,
            input_tokens: 2,
            output_tokens: 1,
            output_tokens_details: null,
            server_tool_use: null,
            service_tier: "standard",
          },
        },
      }),
    })]);
    expect(await client.messages.batches.delete(created.id)).toEqual({ id: created.id, type: "message_batch_deleted" });
    await expect(client.messages.batches.retrieve(created.id)).rejects.toMatchObject({ status: 404 });

    const pending = await client.messages.batches.create({
      requests: [{
        custom_id: "sdk-canceled",
        params: {
          model: "target-anthropic",
          max_tokens: 8,
          messages: [{ role: "user", content: "Cancel me" }],
        },
      }],
    });
    let canceled = await client.messages.batches.cancel(pending.id);
    for (let attempt = 0; attempt < 100 && canceled.processing_status !== "ended"; attempt++) {
      await new Promise((resolve) => setTimeout(resolve, 20));
      canceled = await client.messages.batches.retrieve(pending.id);
    }
    expect(canceled).toMatchObject({
      processing_status: "ended",
      request_counts: {
        processing: 0,
        succeeded: 0,
        errored: 0,
        canceled: 1,
        expired: 0,
      },
    });
    const canceledResults = [];
    for await (const result of await client.messages.batches.results(pending.id)) canceledResults.push(result);
    expect(canceledResults).toEqual([{ custom_id: "sdk-canceled", result: { type: "canceled" } }]);
  });

  it.each(models)("decodes Google Gen AI through %s", async (model) => {
    const result = await gemini().models.generateContent({ model, contents: "Hi" });
    expect(result.text).toBe("Hello");
  });

  it("decodes a stateless Google Gen AI Interaction", async () => {
    const result = await gemini().interactions.create({ model: "target-gemini", input: "Hi", store: false });
    expect(result).toMatchObject({
      model: "target-gemini",
      output_text: "Hello",
      steps: [{ type: "model_output", content: [{ type: "text", text: "Hello" }] }],
      usage: { total_input_tokens: 3, total_output_tokens: 2, total_cached_tokens: 1, total_tokens: 5 },
    });
  });

  it.each(models)("decodes stateless OpenAI Responses through %s", async (model) => {
    const result = await openAI().responses.create({ model, input: "Hi", store: false });
    expect(result.output_text).toBe("Hello");
  });

  it("creates, retrieves, and deletes a gateway-stored Response", async () => {
    const client = openAI();
    const created = await client.responses.create({ model: "target-anthropic", input: "Hi" });
    expect(created).toMatchObject({ object: "response", store: true, background: false });
    expect((await client.responses.retrieve(created.id)).id).toBe(created.id);
    expect(await client.responses.delete(created.id)).toMatchObject({ id: created.id, deleted: true });
    await expect(client.responses.retrieve(created.id)).rejects.toMatchObject({ status: 404 });
  });

  it("uses bounded native web search for stateless, stored, and background Responses", async () => {
    const client = openAI();
    const input = {
      model: "target-openai-web",
      input: "Find a source about Pocket AI Gateway",
      tools: [{
        type: "web_search" as const,
        search_context_size: "low" as const,
        filters: { allowed_domains: ["example.com"] },
        user_location: { type: "approximate" as const, country: "US" },
      }, {
        type: "function" as const,
        name: "lookup_local_note",
        description: "Looks up a local note",
        parameters: { type: "object", properties: {}, additionalProperties: false },
        strict: true,
      }],
      include: ["web_search_call.action.sources" as const],
      max_tool_calls: 1,
      max_output_tokens: 128,
    };

    const stateless = await client.responses.create({ ...input, store: false });
    const search = stateless.output.find((item) => item.type === "web_search_call");
    expect(search).toMatchObject({
      status: "completed",
      action: { type: "search", sources: [{ type: "url", url: "https://example.com/source" }] },
    });
    expect(stateless).toMatchObject({ model: "target-openai-web", usage: { input_tokens: 8, output_tokens: 4 } });

    const stored = await client.responses.create(input);
    expect(stored).toMatchObject({ store: true, background: false });
    const retrieved = await client.responses.retrieve(stored.id);
    expect(retrieved.id).toBe(stored.id);
    expect(retrieved.output.find((item) => item.type === "web_search_call")).toMatchObject({ status: "completed" });

    const background = await client.responses.create({ ...input, background: true });
    let completed = background;
    for (let attempt = 0; attempt < 50 && completed.status !== "completed"; attempt++) {
      await new Promise((resolve) => setTimeout(resolve, 20));
      completed = await client.responses.retrieve(background.id);
    }
    expect(completed).toMatchObject({ id: background.id, status: "completed", background: true });
    expect(completed.output.find((item) => item.type === "web_search_call")).toMatchObject({ status: "completed" });
  });

  it("runs and cancels gateway-owned background Responses", async () => {
    const client = openAI();
    const created = await client.responses.create({ model: "target-openai", input: "Background", background: true });
    expect(created).toMatchObject({ status: "queued", background: true, store: true });
    let completed = created;
    for (let attempt = 0; attempt < 50 && completed.status !== "completed"; attempt++) {
      await new Promise((resolve) => setTimeout(resolve, 20));
      completed = await client.responses.retrieve(created.id);
    }
    expect(completed).toMatchObject({ id: created.id, status: "completed", background: true });
    const input = await client.responses.inputItems.list(created.id);
    expect(input.data[0]).toMatchObject({ type: "message", role: "user" });

    const pending = await client.responses.create({ model: "target-openai", input: "Cancel me", background: true });
    expect(await client.responses.cancel(pending.id)).toMatchObject({ id: pending.id, status: "cancelled" });
  });

  it("decodes native OpenAI response compaction", async () => {
    const result = await openAI().responses.compact({ model: "target-openai", input: "Compact this" });
    expect(result).toMatchObject({ object: "response.compaction", output: [{ type: "compaction" }] });
  });

  it("decodes native OpenAI moderations", async () => {
		const result = await openAI().moderations.create({ model: "target-openai", input: "violent text" });
		expect(result.results[0]?.flagged).toBe(true);
	});

  it("counts native OpenAI Responses input tokens", async () => {
    const result = await openAI().responses.inputTokens.count({ model: "target-openai", input: "hello" });
    expect(result).toMatchObject({ object: "response.input_tokens", input_tokens: 12 });
  });

  it("decodes native OpenAI image generation", async () => {
    const result = await openAI().images.generate({ model: "target-openai", prompt: "A black dot", n: 1 });
    expect(result).toMatchObject({ data: [{ b64_json: "eA==" }], usage: { input_tokens: 5, input_tokens_details: { image_tokens: 0, text_tokens: 5 }, output_tokens: 7 } });
  });

  it("streams native OpenAI image generation", async () => {
    const events = [];
    for await (const event of await openAI().images.generate({
      model: "target-openai-image",
      prompt: "A black dot",
      n: 1,
      stream: true,
      partial_images: 1,
    })) {
      events.push(event);
    }

    expect(events).toEqual([
      {
        type: "image_generation.partial_image",
        b64_json: "cGFydGlhbA==",
        background: "opaque",
        created_at: 1764967971,
        output_format: "png",
        partial_image_index: 0,
        quality: "medium",
        size: "1024x1024",
      },
      {
        type: "image_generation.completed",
        b64_json: "ZmluYWw=",
        background: "opaque",
        created_at: 1764967971,
        output_format: "png",
        quality: "medium",
        size: "1024x1024",
        usage: {
          input_tokens: 5,
          input_tokens_details: { image_tokens: 0, text_tokens: 5 },
          output_tokens: 7,
          total_tokens: 12,
        },
      },
    ]);
  });

  it("decodes native OpenAI image edits", async () => {
    const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=", "base64");
    const result = await openAI().images.edit({ model: "target-openai-edit", image: await toFile(png, "source.png"), prompt: "Add a hat" });
    expect(result.data?.[0]?.b64_json).toBe("ZWRpdA==");
  });

  it("decodes native OpenAI image variations", async () => {
    const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=", "base64");
    const result = await openAI().images.createVariation({ model: "target-openai-variation", image: await toFile(png, "source.png") });
    expect(result.data?.[0]?.b64_json).toBe("dmFyaWF0aW9u");
  });

  it("downloads native OpenAI speech", async () => {
    const result = await openAI().audio.speech.create({ model: "target-openai", input: "Hello", voice: "alloy", instructions: "Warm" });
    expect(Buffer.from(await result.arrayBuffer()).toString()).toBe("ID3gateway-audio");
    expect(result.headers.get("content-type")).toBe("audio/mpeg");
  });

  it("decodes native OpenAI audio transcription", async () => {
    const result = await openAI().audio.transcriptions.create({ model: "target-openai", file: await toFile(Buffer.from("RIFFaudio"), "recording.wav") });
    expect(result.text).toBe("gateway transcript");
  });

  it("streams native OpenAI audio transcription", async () => {
    let text = "";
    for await (const event of await openAI().audio.transcriptions.create({ model: "target-openai", file: await toFile(Buffer.from("RIFFaudio"), "recording.wav"), stream: true })) {
      if (event.type === "transcript.text.delta") text += event.delta;
    }
    expect(text).toBe("gateway stream");
  });

  it("decodes native OpenAI audio translation", async () => {
    const result = await openAI().audio.translations.create({ model: "target-openai", file: await toFile(Buffer.from("RIFFaudio"), "recording.wav") });
    expect(result.text).toBe("gateway translation");
  });

  it("manages gateway-owned OpenAI conversations", async () => {
    const client = openAI();
    const empty = await client.conversations.create();
    await client.conversations.delete(empty.id);
    const conversation = await client.conversations.create({
      metadata: { topic: "sdk" },
      items: [{ role: "user", content: "Hello" }],
    });
    expect(conversation).toMatchObject({ object: "conversation", metadata: { topic: "sdk" } });
    const response = await client.responses.create({ model: "target-anthropic", input: "Continue", store: false, conversation: conversation.id });
    expect(response.conversation).toEqual({ id: conversation.id });
    const responseItemID = response.output[0]?.id;
    expect(responseItemID).toMatch(/^citem_/);
    const background = await client.responses.create({ model: "target-openai", input: "Later", background: true, conversation: conversation.id });
    expect(background.conversation).toEqual({ id: conversation.id });
    let backgroundCompleted = background;
    for (let attempt = 0; attempt < 50 && backgroundCompleted.status !== "completed"; attempt++) {
      await new Promise((resolve) => setTimeout(resolve, 20));
      backgroundCompleted = await client.responses.retrieve(background.id);
    }
    expect(backgroundCompleted).toMatchObject({ status: "completed", conversation: { id: conversation.id } });
    const backgroundItemID = backgroundCompleted.output[0]?.id;
    expect(backgroundItemID).toMatch(/^citem_/);
    let streamedText = "";
    for await (const event of await client.responses.create({ model: "target-anthropic", input: "Stream", store: false, stream: true, conversation: conversation.id })) {
      if (event.type === "response.output_text.delta") streamedText += event.delta;
    }
    expect(streamedText).toBe("Hello");
		const beforeFailure = await client.conversations.items.list(conversation.id, { order: "asc", limit: 100 });
		let failedEvent = false;
		for await (const event of await client.responses.create({ model: "target-openai", input: "Fail stream", store: false, stream: true, conversation: conversation.id })) {
			if (event.type === "response.failed") failedEvent = true;
		}
		expect(failedEvent).toBe(true);
		expect((await client.conversations.items.list(conversation.id, { order: "asc", limit: 100 })).data).toHaveLength(beforeFailure.data.length);
    const added = await client.conversations.items.create(conversation.id, {
      items: [
        { type: "message", role: "user", content: [{ type: "input_text", text: "Next" }] },
        { type: "function_call_output", call_id: "call_1", output: "done" },
      ],
    });
    const itemID = added.data[0]?.id;
    expect(itemID).toMatch(/^citem_/);
    expect(added.data[1]).toMatchObject({ type: "function_call_output", status: "completed" });
    if (!itemID) throw new Error("conversation item ID is missing");
    expect((await client.conversations.items.retrieve(itemID, { conversation_id: conversation.id })).id).toBe(itemID);
    const page = await client.conversations.items.list(conversation.id, { order: "asc", limit: 1 });
    expect(page.data).toHaveLength(1);
    expect(page.has_more).toBe(true);
    const conversationItems = await client.conversations.items.list(conversation.id, { order: "asc", limit: 100 });
    expect(conversationItems.data.some((item) => item.id === responseItemID)).toBe(true);
    expect(conversationItems.data.some((item) => item.id === backgroundItemID)).toBe(true);
    expect((await client.conversations.update(conversation.id, { metadata: { topic: "updated" } })).metadata).toEqual({ topic: "updated" });
    expect((await client.conversations.items.delete(itemID, { conversation_id: conversation.id })).id).toBe(conversation.id);
    expect(await client.conversations.delete(conversation.id)).toMatchObject({ id: conversation.id, deleted: true });
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

    text = "";
    for await (const event of await openAI().responses.create({ model: "target-anthropic", input: "Hi", store: false, stream: true })) {
      if (event.type === "response.output_text.delta") text += event.delta;
    }
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
