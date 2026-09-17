import { accountClient } from "@/features/account/clients/account.client";
import { auditClient } from "@/features/audit/clients/audit.client";
import { authClient } from "@/features/auth/clients/auth.client";
import { keysClient } from "@/features/keys/clients/keys.client";
import { mediaJobsClient } from "@/features/media-jobs/clients/media-jobs.client";
import { modelsClient } from "@/features/models/clients/models.client";
import { playgroundClient } from "@/features/playground/clients/playground.client";
import { providersClient } from "@/features/providers/clients/providers.client";
import { requestsClient } from "@/features/requests/clients/requests.client";
import { settingsClient } from "@/features/settings/clients/settings.client";
import { statusClient } from "@/features/status/clients/status.client";
import { usageClient } from "@/features/usage/clients/usage.client";
import { usersClient } from "@/features/users/clients/users.client";

export const pocketAIGatewayAdmin = {
  account: accountClient,
  audit: auditClient,
  auth: authClient,
  keys: keysClient,
  mediaJobs: mediaJobsClient,
  models: modelsClient,
  playground: playgroundClient,
  providers: providersClient,
  requests: requestsClient,
  settings: settingsClient,
  status: statusClient,
  usage: usageClient,
  users: usersClient,
} as const;

export { GatewayAPIError } from "@/lib/api-client";
