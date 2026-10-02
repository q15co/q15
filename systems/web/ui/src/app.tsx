import { clsx } from "clsx";
import styles from "./app.module.css";
import { useEffect, useLayoutEffect, useState, useSyncExternalStore } from "react";
import { flushSync } from "react-dom";
import { AnimatePresence } from "motion/react";
import * as m from "motion/react-m";
import {
  Sparkles,
  MessageSquare,
  Moon,
  Sun,
  Menu,
  X,
  Download,
  WifiOff,
  CircleAlert,
} from "lucide-react";
import type { ChatStore } from "./chat-store";
import { Composer } from "./components/composer";
import { Transcript } from "./components/transcript";
import { Button } from "./components/ui/button";
import { useMotionPreference } from "./components/ui/motion";
import { applyTheme, initialTheme, revealTheme } from "./theme";

interface InstallEvent extends Event {
  prompt(): Promise<void>;
}

export function App({ store, preview = false }: { store: ChatStore; preview?: boolean }) {
  const reduced = useMotionPreference();
  const state = useSyncExternalStore(store.subscribe, store.getSnapshot);
  const [theme, setTheme] = useState(initialTheme);
  const [menu, setMenu] = useState(false);
  const [install, setInstall] = useState<InstallEvent | null>(null);
  useLayoutEffect(() => {
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
  const connected = state.connection === "connected";
  return (
    <div className={styles.appShell}>
      <a className={styles.skipLink} href="#message-input">
        Skip to message input
      </a>
      <AnimatePresence initial={false}>
        {menu && (
          <m.button
            initial={reduced ? false : { opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            transition={{ duration: reduced ? 0 : 0.24 }}
            className={styles.sidebarBackdrop}
            aria-label="Close navigation"
            onClick={() => setMenu(false)}
          />
        )}
      </AnimatePresence>
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
            <span>Chat</span>
            <span className={styles.navDot} />
          </a>
        </div>
        <div className={styles.sidebarFooter}>
          <span className={styles.connectionStatus}>
            <i className={clsx(styles.statusDot, !connected && styles.disconnected)} />
            {preview ? "Offline preview" : connected ? "Connected" : "Reconnecting…"}
          </span>
          <Button
            variant="ghost"
            size="icon"
            aria-label={theme === "mocha" ? "Switch to Latte theme" : "Switch to Mocha theme"}
            onClick={(event) => {
              revealTheme(event.currentTarget, reduced, () => {
                flushSync(() => setTheme((current) => (current === "mocha" ? "latte" : "mocha")));
              });
            }}
          >
            <m.span
              key={theme}
              aria-hidden="true"
              initial={reduced ? false : { opacity: 0, rotate: -90 }}
              animate={{ opacity: 1, rotate: 0 }}
              transition={{ duration: reduced ? 0 : 0.24 }}
            >
              {theme === "mocha" ? <Sun /> : <Moon />}
            </m.span>
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
          </div>
        </header>
        {!connected && (
          <output className={styles.connectionBanner}>
            <WifiOff size={15} />
            {state.connection === "offline"
              ? "Disconnected. Refresh to reconnect."
              : "Reconnecting to q15…"}
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
