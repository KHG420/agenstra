import type { AgenstraClient } from "./agenstra-client.js";
export class AgenstraChat extends HTMLElement {
  client: AgenstraClient;
}
export function mountAgenstraChat(container: HTMLElement, options: {
  client: AgenstraClient; title?: string; locale?: "zh-CN" | "en";
  subtitle?: string; emptyTitle?: string; emptyHint?: string; placeholder?: string;
}): { element: AgenstraChat; unmount(): void };
