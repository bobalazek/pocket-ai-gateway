const setupRoutes = ["/_/setup", "/_/setup/"];
const publicAuthRoutes = [...setupRoutes, "/_/login", "/_/login/", "/_/activate", "/_/activate/"];

export function isSetupRoute(path: string) { return setupRoutes.includes(path); }
export function isPublicAuthRoute(path: string) { return publicAuthRoutes.includes(path); }

export function unauthenticatedDestination(path: string) {
  if (isSetupRoute(path)) return "/_/login/";
  return isPublicAuthRoute(path) ? null : "/_/login/?reason=session-expired";
}
