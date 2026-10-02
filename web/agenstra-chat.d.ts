import type { AgenstraClient } from "./agenstra-client.js";
export class AgenstraChat extends HTMLElement {
  client: AgenstraClient;
}
export function mountAgenstraChat(container: HTMLElement, options: {
  client: AgenstraClient; title?: string; locale?: "zh-CN" | "en";
}): { element: AgenstraChat; unmount(): void };
