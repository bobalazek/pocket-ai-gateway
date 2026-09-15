export const inferenceScopes = [
  "chat:generate",
  "messages:batches",
  "messages:web_search",
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
  "models:read",
  "tokens:count",
] as const;

export type InferenceScope = (typeof inferenceScopes)[number];
