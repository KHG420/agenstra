// The example uses the optional SDK component. Keep its former local entry point
// available for hosts following the older example without duplicating rendering.
import { AgenstraChat } from "/web/assets/agenstra-chat.js";
export class DemoChat extends AgenstraChat {}
if (!customElements.get("demo-chat")) customElements.define("demo-chat", DemoChat);
export function mountDemoChat(container, { client, title, locale = "zh-CN" }) {
  const element = document.createElement("demo-chat");
  element.lang = locale;
  if (title) element.setAttribute("title", title);
  element.client = client;
  container.append(element);
  return { element, unmount: () => element.remove() };
}
