export const inferenceScopes = [
  "chat:generate",
  "messages:batches",
  "messages:web_search",
  "messages:web_fetch",
  "responses:generate",
  "responses:web_search",
  "embeddings:generate",
  "moderations:classify",
  "images:generate",
  "images:edit",
  "images:variation",
  "audio:speech",
  "audio:transcribe",
  "audio:translate",
  "batches:manage",
  "files:manage",
  "models:read",
  "tokens:count",
] as const;

export type InferenceScope = (typeof inferenceScopes)[number];
