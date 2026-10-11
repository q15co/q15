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
import { mediaAdapter } from "./infrastructure/media";
import { authenticatedFetch, requestProof } from "./infrastructure/proof";
import { logout } from "./infrastructure/session";
import { dropExpiredShares, takeSharedFiles } from "./infrastructure/share-inbox";
import { SocketTransport } from "./infrastructure/transport";
import { shareNotice } from "./shared/share-inbox";

async function previewStore() {
  const { MockTransport } = await import("./infrastructure/mock-transport");
  const transport = new MockTransport();
  return new ChatStore(transport, transport.history);
}

function publishManifest() {
  const link = document.createElement("link");
  link.rel = "manifest";
  link.href = "/manifest.webmanifest";
  link.crossOrigin = "use-credentials";
  document.head.append(link);
}
const preview = import.meta.env.DEV && new URLSearchParams(location.search).has("preview");
const signedIn = preview || (await hasSession());
const notice = signedIn
  ? shareNotice(new URLSearchParams(location.search).get("share"))
  : undefined;
// A share the reader cannot use yet stays in the service worker's inbox: signing in reloads this
// page, and the next start collects it. Anything nobody collected is dropped a day after it arrived.
// Both touch Cache Storage, so neither is awaited: the shell has to reach the reader first.
const collectShares = signedIn && !preview ? takeSharedFiles : undefined;
void dropExpiredShares();
if (notice !== undefined) history.replaceState(null, "", location.pathname);
const liveTransport = new SocketTransport();
const store = signedIn
  ? preview
    ? await previewStore()
    : new ChatStore(
        liveTransport,
        (before, signal) => fetchHistory(before, signal, liveTransport.content),
        mediaAdapter(liveTransport.content),
      )
  : undefined;
const root = document.querySelector("#root");
if (!root) throw new Error("The chat shell is missing its root element.");
createRoot(root).render(
  <StrictMode>
    <MotionProvider>
      {store ? (
        <App
          store={store}
          preview={preview}
          {...(preview ? {} : { onLogout: logout })}
          {...(collectShares === undefined ? {} : { collectShares })}
          {...(notice === undefined ? {} : { shareNotice: notice })}
        />
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
    if (response.status === 204) {
      await navigator.serviceWorker.register("/sw.js");
      if (navigator.serviceWorker.controller) publishManifest();
      else
        navigator.serviceWorker.addEventListener("controllerchange", publishManifest, {
          once: true,
        });
    }
  } catch {
    /* Chat works without installation. */
  }
}
