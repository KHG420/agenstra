export type JSONObject = { [key: string]: unknown };
export interface BrowserCommand { id: string; run_id: string; session_id: string; generation: number; action: string; arguments: JSONObject; status: string; context_revision: number; expires_at: number }
export interface Run { run_id: string; status: string; revision: number; state: { runtime: JSONObject } }
export interface ChatConversation { id: string; integration_id: string; created_at: number }
export interface ChatMessage { id: string; conversation_id: string; client_id: string; text: string; run_id: string; status: string; answer_markdown?: string; run?: Run }
export interface ConversationSnapshot { conversation: ChatConversation; messages: ChatMessage[] }
export interface ClientOptions {
  endpoint?: string; integration: string; browser?: boolean; handlerVersion?: string;
  getSession(): Promise<string | { token: string; expires_at?: number }>;
  /** Current page data for browser actions, never the agent's conversation context. */
  getPageObservation?(): JSONObject | Promise<JSONObject>;
  fetch?: typeof fetch; storage?: Storage | null; pollInterval?: number;
  onListenerError?(error: unknown): void;
}
export class AgenstraError extends Error { code: string; status: number; clientId?: string }
/** Only for a failure whose lack of committed side effects the host can prove. */
export class AgenstraActionError extends Error { constructor(code: string, message?: string); code: string }
export class AgenstraClient {
  constructor(options: ClientOptions);
  options: ClientOptions;
  id(): string;
  registerActions(actions: Record<string, (args: JSONObject, context: { commandId: string; runId: string }) => JSONObject | Promise<JSONObject>>): this;
  on(name: string, callback: (value: any) => void): () => void;
  connectBrowser(): Promise<unknown>;
  updatePageObservation(observation: JSONObject): Promise<void>;
  listConversations(): Promise<ChatConversation[]>;
  createConversation(): Promise<ChatConversation>;
  /** Select a framework-owned conversation. Context restoration is server-side. */
  selectConversation(id: string): Promise<ChatConversation>;
  getConversation(): Promise<ChatConversation>;
  snapshot(): Promise<ConversationSnapshot>;
  send(text: string, options?: { clientId?: string }): Promise<ChatMessage>;
  watchConversation(callback: (snapshot: ConversationSnapshot) => void): () => void;
  getRun(id: string): Promise<Run>;
  supplyInput(id: string, field: string, text: string, revision: number): Promise<Run>;
  approve(id: string, invocation: JSONObject, revision: number, approved: boolean): Promise<Run>;
  cancelMessage(id: string): Promise<ChatMessage>;
  reconcile(id: string, revision: number): Promise<Run>;
  run(instruction: string, options?: { requestId?: string }): Promise<Run>;
  destroy(options?: { closeSession?: boolean }): Promise<void>;
}
export function createAgenstraClient(options: ClientOptions): AgenstraClient;
