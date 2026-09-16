export type ProviderConnection = {
  id: string;
  name: string;
  adapter: string;
  adapter_label: string;
  base_url: string;
  enabled: boolean;
  allow_private_network: boolean;
  timeout_ms: number;
  preset: string;
  capabilities: string[];
  credential_required: boolean;
  credential_state: "missing" | "stored" | "external";
  revision: number;
  created_at: string;
  updated_at: string;
};

export type ProviderAdapter = {
  id: string;
  label: string;
  default: boolean;
  capabilities: string[];
};

export type ProviderPreset = {
  id: string;
  label: string;
  adapter: ProviderConnection["adapter"];
  adapter_label: string;
  base_url?: string;
  base_url_example?: string;
  base_url_required: boolean;
  credential_required: boolean;
  private_network: boolean;
  operations: string[];
  capabilities: string[];
  documentation_url: string;
  reviewed_at: string;
};

export type UpstreamModel = {
  id: string;
  connection_id: string;
  upstream_id: string;
  capabilities: string[];
  active: boolean;
};
