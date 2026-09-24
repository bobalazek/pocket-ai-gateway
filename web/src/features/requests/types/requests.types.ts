export type RequestFilters = {
  request_id?: string;
  user_id?: string;
  key_id?: string;
  model_id?: string;
  dialect?: string;
  cursor?: string;
};

export type RequestPriceProvenance = {
  id: string;
  source: string;
  input_usd_per_million: string;
  cache_read_usd_per_million: string | null;
  output_usd_per_million: string;
  web_search_usd_per_call: string | null;
  effective_from: string;
  effective_to: string | null;
};

export type RequestAttempt = {
  id: string;
  ordinal: number;
  connection_id: string;
  model_id: string;
  upstream_model_id: string;
  target_dialect: string;
  target_operation: string;
  translation_applied: boolean;
  request_tool_count: number;
  response_tool_call_count: number;
  tool_call_status: string;
  selection_reason: string;
  rejected_candidates: { upstream_model_id: string; connection_id: string; reason: string }[];
  state: string;
  usage_status: string;
  input_tokens: number | null;
  output_tokens: number | null;
  cache_creation_input_tokens: number | null;
  cache_read_input_tokens: number | null;
  cache_creation_5m_input_tokens: number | null;
  cache_creation_1h_input_tokens: number | null;
  web_search_max_calls: number | null;
  web_search_call_count: number | null;
  estimated_cost_usd: string | null;
  as_recorded_cost_usd: string | null;
  restated_cost_usd: string | null;
  cost_usd: string | null;
  recorded_price: RequestPriceProvenance | null;
  restated_price: RequestPriceProvenance | null;
  started_at: string;
};

export type GatewayRequest = {
  id: string;
  owner_user_id: string;
  key_id: string;
  operation: string;
  dialect: string;
  model_id: string;
  state: string;
  started_at: string;
  finished_at: string | null;
  attempts: RequestAttempt[];
};
