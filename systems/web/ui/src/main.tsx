import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { App } from "./app";
import { ChatStore } from "./application/chat-store";
import { Login } from "./components/login";

import "@fontsource-variable/recursive/full.css";
import "./styles.css";
import { MotionProvider } from "./components/ui/motion";
import { hasSession, ownerAuthentication } from "./infrastructure/auth";
import { fetchHistory } from "./infrastructure/history";
import { authenticatedFetch, requestProof } from "./infrastructure/proof";
import { logout } from "./infrastructure/session";
import { SocketTransport } from "./infrastructure/transport";

async function previewStore() {
  const { MockTransport } = await import("./infrastructure/mock-transport");
  const transport = new MockTransport();
  return new ChatStore(transport, transport.history);
}
const preview = import.meta.env.DEV && new URLSearchParams(location.search).has("preview");
const signedIn = preview || (await hasSession());
const store = signedIn
  ? preview
    ? await previewStore()
    : new ChatStore(new SocketTransport(), fetchHistory)
  : undefined;
const root = document.querySelector("#root");
if (!root) throw new Error("The chat shell is missing its root element.");
createRoot(root).render(
  <StrictMode>
    <MotionProvider>
      {store ? (
        <App store={store} preview={preview} {...(preview ? {} : { onLogout: logout })} />
      ) : (
        <Login authentication={ownerAuthentication} />
      )}
    </MotionProvider>
  </StrictMode>,
);
if (signedIn && import.meta.env.PROD && "serviceWorker" in navigator) {
  // Registration only caches the compiled shell; transcripts never leave memory.
  try {
    const proof = await requestProof("GET", "/sw.js", location.origin);
    const response = await authenticatedFetch("/auth/worker", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ proof }),
    });
    if (response.status === 204) await navigator.serviceWorker.register("/sw.js");
  } catch {
    /* Chat works without installation. */
  }
}
