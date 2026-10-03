import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { App } from "./app";

import "@fontsource-variable/recursive/full.css";
import "./styles.css";
import { ChatStore } from "./application/chat-store";
import { MotionProvider } from "./components/ui/motion";
import { fetchHistory } from "./infrastructure/history";
import { SocketTransport } from "./infrastructure/transport";

async function previewStore() {
  const { MockTransport } = await import("./infrastructure/mock-transport");
  const transport = new MockTransport();
  return new ChatStore(transport, transport.history);
}
const preview = import.meta.env.DEV && new URLSearchParams(location.search).has("preview");
const store = preview ? await previewStore() : new ChatStore(new SocketTransport(), fetchHistory);
const root = document.querySelector("#root");
if (!root) throw new Error("The chat shell is missing its root element.");
createRoot(root).render(
  <StrictMode>
    <MotionProvider>
      <App store={store} preview={preview} />
    </MotionProvider>
  </StrictMode>,
);
if (import.meta.env.PROD && "serviceWorker" in navigator) {
  // Registration only caches the compiled shell; transcripts never leave memory.
  try {
    await navigator.serviceWorker.register("/sw.js");
  } catch {
    /* Chat works without installation. */
  }
}
