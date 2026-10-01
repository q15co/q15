import { clsx } from "clsx";
import styles from "./app.module.css";
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
    <div className={styles.appShell}>
      <a className={styles.skipLink} href="#message-input">
        Skip to message input
      </a>
      {menu && (
        <button
          className={styles.sidebarBackdrop}
          aria-label="Close navigation"
          onClick={() => setMenu(false)}
        />
      )}
      <aside
        className={clsx(styles.sidebar, menu && styles.sidebarOpen)}
        aria-label="Chat navigation"
      >
        <a className={styles.brand} href="/">
          <span className={styles.brandIcon}>
            <Sparkles size={19} />
          </span>
          <span>
            q15<span className={styles.brandDot}>.</span>
          </span>
          <span className={styles.brandCaption}>YOUR OWN SPACE</span>
        </a>
        <Button
          className={styles.mobileClose}
          variant="ghost"
          size="icon"
          aria-label="Close navigation"
          onClick={() => setMenu(false)}
        >
          <X />
        </Button>
        <div className={styles.sidebarSection}>
          <span className={styles.sidebarLabel}>WORKSPACE</span>
          <a
            className={styles.navItem + " " + styles.navActive}
            href="#message-input"
            onClick={() => setMenu(false)}
          >
            <MessageSquare size={17} />
            <span>Your conversation</span>
            <span className={styles.navDot} />
          </a>
        </div>
        {recent.length > 0 && (
          <div className={styles.sidebarSection + " " + styles.recentSection}>
            <span className={styles.sidebarLabel}>RECENT THOUGHTS</span>
            {recent.map((m) => (
              <a
                className={styles.recentLink}
                key={m.key}
                href={`#message-${m.key}`}
                onClick={() => setMenu(false)}
              >
                <span className={styles.recentText}>
                  {m.parts
                    .filter((p) => p.part_type === "text")
                    .map((p) => p.text)
                    .join("") || "Message"}
                </span>
                <ArrowUpRight size={13} />
              </a>
            ))}
          </div>
        )}
        <div className={styles.sidebarNote}>
          <span className={styles.noteStars}>✦</span>
          <p>
            Room to think.
            <br />
            Space to make things happen.
          </p>
        </div>
        <div className={styles.sidebarFooter}>
          <div className={styles.ownerAvatar}>Y</div>
          <div>
            <strong>Your personal agent</strong>
            <span>
              <i className={clsx(styles.statusDot, !connected && styles.disconnected)} />
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
      <main className={styles.mainPanel}>
        <header className={styles.topbar}>
          <div className={styles.topbarTitle}>
            <Button
              className={styles.mobileMenu}
              variant="ghost"
              size="icon"
              aria-label="Open navigation"
              onClick={() => setMenu(true)}
            >
              <Menu />
            </Button>
            <span className={styles.topbarIcon}>
              <MessageSquare size={17} />
            </span>
            <span>Chat</span>
            <span className={styles.topbarDivider}>/</span>
            <span className={styles.topbarSubtitle}>Your conversation</span>
          </div>
          <div className={styles.topbarRight}>
            {preview && <span className={styles.previewBadge}>Preview</span>}
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
            <span className={styles.privateBadge}>
              <span />
              Just for you
            </span>
          </div>
        </header>
        <div className={styles.conversationTitle}>
          <div>
            <span className={styles.eyebrow}>THINK OUT LOUD</span>
            <h2>A conversation with q15</h2>
          </div>
          <span className={styles.conversationStatus}>
            <span className={clsx(styles.statusDot, !connected && styles.disconnected)} />
            {state.active !== null
              ? "Working on it"
              : connected
                ? "Here when you need me"
                : "Connecting"}
          </span>
        </div>
        {!connected && (
          <output className={styles.connectionBanner}>
            <WifiOff size={15} />
            {state.connection === "offline"
              ? "Disconnected. Refresh to reconnect."
              : "Reconnecting to q15. Your draft is safe here."}
          </output>
        )}
        {(state.notice || state.error) && (
          <div
            className={clsx(styles.noticeBanner, state.error && styles.errorBanner)}
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
