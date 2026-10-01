import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@fontsource-variable/recursive/full.css";
import "./styles.css";
import { App } from "./app";
import { ChatStore } from "./chat-store";
import { SocketTransport } from "./transport";

const preview = import.meta.env.DEV && new URLSearchParams(location.search).has("preview");
const store = preview
  ? await import("./mock-transport").then(({ MockTransport }) => {
      const transport = new MockTransport();
      return new ChatStore(transport, transport.history);
    })
  : new ChatStore(new SocketTransport());
createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App store={store} preview={preview} />
  </StrictMode>,
);
if (import.meta.env.PROD && "serviceWorker" in navigator) {
  // Registration only caches the compiled shell; transcripts never leave memory.
  void navigator.serviceWorker.register("/sw.js").catch(() => {
    /* Chat works without installation. */
  });
}
