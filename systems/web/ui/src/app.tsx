import { useEffect, useState, useSyncExternalStore } from "react";
import {
  Sparkles,
  MessageSquare,
  Moon,
  Sun,
  Menu,
  X,
  ArrowUpRight,
  Download,
  WifiOff,
  CircleAlert,
} from "lucide-react";
import type { ChatStore } from "./chat-store";
import { Composer } from "./components/composer";
import { Transcript } from "./components/transcript";
import { Button } from "./components/ui/button";
import { applyTheme, initialTheme } from "./theme";

interface InstallEvent extends Event {
  prompt(): Promise<void>;
}

export function App({ store, preview = false }: { store: ChatStore; preview?: boolean }) {
  const state = useSyncExternalStore(store.subscribe, store.getSnapshot);
  const [theme, setTheme] = useState(initialTheme);
  const [menu, setMenu] = useState(false);
  const [install, setInstall] = useState<InstallEvent | null>(null);
  useEffect(() => {
    applyTheme(theme);
  }, [theme]);
  useEffect(() => {
    store.start();
    const presence = () => store.presence(!document.hidden);
    const onInstall = (event: Event) => {
      event.preventDefault();
      setInstall(event as InstallEvent);
    };
    document.addEventListener("visibilitychange", presence);
    window.addEventListener("beforeinstallprompt", onInstall);
    return () => {
      store.stop();
      document.removeEventListener("visibilitychange", presence);
      window.removeEventListener("beforeinstallprompt", onInstall);
    };
  }, [store]);
  const recent = state.messages
    .filter((m) => m.role === "user")
    .slice(-6)
    .reverse();
  const connected = state.connection === "connected";
  return (
    <div className="app-shell">
      <a className="skip-link" href="#message-input">
        Skip to message input
      </a>
      {menu && (
        <button
          className="sidebar-backdrop"
          aria-label="Close navigation"
          onClick={() => setMenu(false)}
        />
      )}
      <aside className={`sidebar ${menu ? "sidebar-open" : ""}`} aria-label="Chat navigation">
        <a className="brand" href="/">
          <span className="brand-icon">
            <Sparkles size={19} />
          </span>
          <span>
            q15<span className="brand-dot">.</span>
          </span>
          <span className="brand-caption">YOUR OWN SPACE</span>
        </a>
        <Button
          className="mobile-close"
          variant="ghost"
          size="icon"
          aria-label="Close navigation"
          onClick={() => setMenu(false)}
        >
          <X />
        </Button>
        <div className="sidebar-section">
          <span className="sidebar-label">WORKSPACE</span>
          <a className="nav-item nav-active" href="#message-input" onClick={() => setMenu(false)}>
            <MessageSquare size={17} />
            <span>Your conversation</span>
            <span className="nav-dot" />
          </a>
        </div>
        {recent.length > 0 && (
          <div className="sidebar-section recent-section">
            <span className="sidebar-label">RECENT THOUGHTS</span>
            {recent.map((m) => (
              <a
                className="recent-link"
                key={m.key}
                href={`#message-${m.key}`}
                onClick={() => setMenu(false)}
              >
                {m.parts
                  .filter((p) => p.part_type === "text")
                  .map((p) => p.text)
                  .join("") || "Message"}
                <ArrowUpRight size={13} />
              </a>
            ))}
          </div>
        )}
        <div className="sidebar-note">
          <span className="note-stars">✦</span>
          <p>
            Room to think.
            <br />
            Space to make things happen.
          </p>
        </div>
        <div className="sidebar-footer">
          <div className="owner-avatar">Y</div>
          <div>
            <strong>Your personal agent</strong>
            <span>
              <i className={`status-dot ${connected ? "" : "disconnected"}`} />
              {preview ? "Offline preview" : connected ? "Connected" : "Reconnecting…"}
            </span>
          </div>
          <Button
            variant="ghost"
            size="icon"
            aria-label={theme === "mocha" ? "Switch to Latte theme" : "Switch to Mocha theme"}
            onClick={() => setTheme(theme === "mocha" ? "latte" : "mocha")}
          >
            {theme === "mocha" ? <Sun /> : <Moon />}
          </Button>
        </div>
      </aside>
      <main className="main-panel">
        <header className="topbar">
          <div className="topbar-title">
            <Button
              className="mobile-menu"
              variant="ghost"
              size="icon"
              aria-label="Open navigation"
              onClick={() => setMenu(true)}
            >
              <Menu />
            </Button>
            <span className="topbar-icon">
              <MessageSquare size={17} />
            </span>
            <span>Chat</span>
            <span className="topbar-divider">/</span>
            <span className="topbar-subtitle">Your conversation</span>
          </div>
          <div className="topbar-right">
            {preview && <span className="preview-badge">Preview</span>}
            {install && (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  void install.prompt().then(() => setInstall(null));
                }}
              >
                <Download />
                Install
              </Button>
            )}
            <span className="private-badge">
              <span />
              Just for you
            </span>
          </div>
        </header>
        <div className="conversation-title">
          <div>
            <span className="eyebrow">THINK OUT LOUD</span>
            <h2>A conversation with q15</h2>
          </div>
          <span className="conversation-status">
            <span className={`status-dot ${connected ? "" : "disconnected"}`} />
            {state.active !== null
              ? "Working on it"
              : connected
                ? "Here when you need me"
                : "Connecting"}
          </span>
        </div>
        {!connected && (
          <output className="connection-banner">
            <WifiOff size={15} />
            {state.connection === "offline"
              ? "Disconnected. Refresh to reconnect."
              : "Reconnecting to q15. Your draft is safe here."}
          </output>
        )}
        {(state.notice || state.error) && (
          <div
            className={`notice-banner ${state.error ? "error-banner" : ""}`}
            role={state.error ? "alert" : "status"}
          >
            <CircleAlert size={15} />
            <span>{state.error ?? state.notice}</span>
            <Button
              variant="ghost"
              size="sm"
              aria-label="Dismiss notice"
              onClick={() => store.dismiss()}
            >
              <X />
            </Button>
          </div>
        )}
        <Transcript state={state} store={store} />
        <Composer state={state} store={store} />
      </main>
    </div>
  );
}
