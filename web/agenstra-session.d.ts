export function createAgenstraSessionHandler(options: {
  endpoint: string;
  authenticateRequest(request: Request): string | null | Promise<string | null>;
  /** Validate the host's CSRF token and/or exact trusted origin. */
  verifyRequest(request: Request): boolean | Promise<boolean>;
  resolveAPIKey(owner: string): string | null | Promise<string | null>;
  fetch?: typeof fetch;
}): (request: Request) => Promise<Response>;
