import { createAgenstraClient } from "/web/assets/agenstra-client.js";
import { mountAgenstraChat } from "/web/assets/agenstra-chat.js";

const state = { page: "orders", status: "all", selected_order: "" };
const rows = await fetch("/demo/orders").then(response => response.json()).then(data => data.orders);
const labels = { pending: "待处理", completed: "已完成" };
const content = document.querySelector("#host-content");
function render() {
  content.replaceChildren();
  document.querySelector("#filter").value = state.status;
  document.querySelector("#page-title").textContent = state.page === "order_detail" ? "订单详情" : "订单工作台";
  if (state.page === "order_detail") {
    const order = rows.find(row => row.id === state.selected_order);
    const detail = document.createElement("div");detail.className = "detail";
    const title = document.createElement("h3");title.textContent = "订单 " + order.id;detail.append(title);
    for (const text of [order.customer, labels[order.status], "金额：¥ " + order.amount.toLocaleString("zh-CN")]) { const p = document.createElement("p");p.textContent = text;detail.append(p); }
    content.append(detail);return;
  }
  const table = document.createElement("table");
  const head = table.createTHead().insertRow();
  for (const text of ["订单", "客户", "状态", "金额"]) { const th = document.createElement("th");th.scope = "col";th.textContent = text;head.append(th); }
  for (const order of rows.filter(row => state.status === "all" || row.status === state.status)) {
    const tr = table.insertRow();
    for (const text of [order.id, order.customer, labels[order.status], "¥ " + order.amount.toLocaleString("zh-CN")]) tr.insertCell().textContent = text;
    tr.cells[2].className = "status";
  }
  content.append(table);
}
const client = createAgenstraClient({
  integration: "orders-web", browser: true, handlerVersion: "1",
  getSession: async () => {
    const response = await fetch("/web/v1/token", { method: "POST", credentials: "same-origin" });
    if (!response.ok) throw new Error("Session unavailable");
    return response.json();
  },
  getPageObservation: () => ({ ...state })
});
client.registerActions({
  "ui.show_orders": async ({ status }) => {
    state.page = "orders";state.status = status;state.selected_order = "";render();
    return { page: state.page, status, visible_count: rows.filter(row => status === "all" || row.status === status).length };
  },
  "ui.open_order": async ({ id }) => {
    if (!rows.some(row => row.id === id)) throw new Error("Order unavailable");
    state.page = "order_detail";state.selected_order = id;render();return { page: state.page, id };
  }
});
client.on("action", ({ command, status }) => {
  const evidence = document.querySelector("#evidence");
  evidence.textContent = "前端动作：" + command.action + (status === "succeeded" ? " · 页面已更新，回执已提交" : status === "unknown" ? " · 结果待确认" : " · 正在执行");
});
client.on("error", error => { if (error.name !== "AbortError") document.querySelector("#host-error").textContent = "操作未完成：" + (error.code || error.message); });
mountAgenstraChat(document.querySelector("#chat"), { client, title: "订单助手" });
const conversationList = document.querySelector("#conversation-list");
async function refreshConversations() {
  const selected = await client.getConversation();
  const items = await client.listConversations();
  conversationList.replaceChildren();
  for (const item of items) {
    const option = document.createElement("option");
    option.value = item.id;option.textContent = "会话 " + item.id.slice(0, 8);
    option.selected = item.id === selected.id;conversationList.append(option);
  }
  conversationList.disabled = false;
}
conversationList.addEventListener("change", async () => {
  conversationList.disabled = true;
  try { await client.selectConversation(conversationList.value); }
  catch (error) { document.querySelector("#host-error").textContent = error.message; }
  finally { await refreshConversations(); }
});
document.querySelector("#new-conversation").addEventListener("click", async event => {
  const button = event.currentTarget;button.disabled = true;
  try { await client.createConversation();await refreshConversations(); }
  catch (error) { document.querySelector("#host-error").textContent = error.message; }
  finally { button.disabled = false; }
});
refreshConversations().catch(error => { document.querySelector("#host-error").textContent = error.message; });
document.querySelector("#filter").addEventListener("change", async event => {
  state.page = "orders";state.selected_order = "";state.status = event.target.value;render();
  try { await client.updatePageObservation(state); } catch (error) { document.querySelector("#host-error").textContent = error.message; }
});
for (const button of document.querySelectorAll("[data-prompt]")) button.addEventListener("click", () => {
  const input = document.querySelector("agenstra-chat").shadowRoot.querySelector("textarea");
  input.value = button.dataset.prompt;input.focus();
});
render();
