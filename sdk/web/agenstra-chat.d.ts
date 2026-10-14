import type { AgenstraClient, RunSource } from "./agenstra-client.js";
export class AgenstraChat extends HTMLElement {
  client: AgenstraClient;
  /** Select a new message's explicit scope; retries retain that frozen scope. */
  getSources?: () => RunSource[] | Promise<RunSource[]>;
}
export function mountAgenstraChat(container: HTMLElement, options: {
  client: AgenstraClient; title?: string; locale?: "zh-CN" | "en";
  subtitle?: string; emptyTitle?: string; emptyHint?: string; placeholder?: string;
  getSources?: () => RunSource[] | Promise<RunSource[]>;
}): { element: AgenstraChat; unmount(): void };
