export type JSONObject = { [key: string]: unknown };
export interface BrowserCommand { id: string; run_id: string; session_id: string; generation: number; action: string; arguments: JSONObject; status: string; context_revision: number; expires_at: number }
export interface Run { run_id: string; status: string; revision: number; state: { runtime: JSONObject } }
export interface ChatMessage { id: string; client_id: string; text: string; run_id: string; status: string; answer_markdown?: string; run?: Run }
export interface ClientOptions {
  endpoint?: string; integration: string; browser?: boolean; handlerVersion?: string;
  getSession(): Promise<string | { token: string; expires_at?: number }>;
  getContext?(): JSONObject | Promise<JSONObject>;
  fetch?: typeof fetch; storage?: Storage | null; pollInterval?: number;
  onListenerError?(error: unknown): void;
}
export class AgenstraError extends Error { code: string; status: number; clientId?: string }
export class AgenstraClient {
  constructor(options: ClientOptions);
  options: ClientOptions;
  id(): string;
  registerActions(actions: Record<string, (args: JSONObject, context: { commandId: string; runId: string }) => JSONObject | Promise<JSONObject>>): this;
  on(name: string, callback: (value: any) => void): () => void;
  connectBrowser(): Promise<unknown>;
  setContext(context: JSONObject): Promise<void>;
  getConversation(): Promise<{ id: string; integration_id: string }>;
  snapshot(): Promise<{ conversation: { id: string }; messages: ChatMessage[] }>;
  send(text: string, options?: { clientId?: string }): Promise<ChatMessage>;
  watchConversation(callback: (snapshot: { messages: ChatMessage[] }) => void): () => void;
  getRun(id: string): Promise<Run>;
  supplyInput(id: string, field: string, text: string, revision: number): Promise<Run>;
  approve(id: string, invocation: JSONObject, revision: number, approved: boolean): Promise<Run>;
  cancelMessage(id: string): Promise<ChatMessage>;
  reconcile(id: string, revision: number): Promise<Run>;
  run(instruction: string, options?: { requestId?: string }): Promise<Run>;
  destroy(options?: { closeSession?: boolean }): Promise<void>;
}
export function createAgenstraClient(options: ClientOptions): AgenstraClient;
