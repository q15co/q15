import { clsx } from "clsx";
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
import { AnimatePresence } from "motion/react";
import * as motion from "motion/react-m";
import { useEffect, useLayoutEffect, useState, useSyncExternalStore } from "react";
import { flushSync } from "react-dom";

import type { ChatStore } from "./application/chat-store";

import { Composer } from "./components/composer";
import { Transcript } from "./components/transcript";
import { Button } from "./components/ui/button";
import { useMotionPreference } from "./components/ui/motion-preference";
import { applyTheme, initialTheme, revealTheme } from "./theme";

import styles from "./app.module.css";

interface InstallEvent extends Event {
  prompt(): unknown;
}

function isInstallEvent(event: Event): event is InstallEvent {
  return "prompt" in event && typeof event.prompt === "function";
}

export function App({
  store,
  preview: offlinePreview,
  onLogout,
}: {
  store: ChatStore;
  preview?: boolean;
  onLogout?: () => Promise<void>;
}) {
  const preview = offlinePreview ?? false;
  const reduced = useMotionPreference();
  const state = useSyncExternalStore(store.subscribe, store.getSnapshot);
  const [theme, setTheme] = useState(initialTheme);
  const [menu, setMenu] = useState(false);
  const [logoutError, setLogoutError] = useState<string | null>(null);
  const [install, setInstall] = useState<InstallEvent | null>(null);
  const promptInstall = async (event: InstallEvent) => {
    try {
      await event.prompt();
    } catch {
      // Installation is optional; a dismissed or unavailable prompt leaves chat usable.
    } finally {
      setInstall(null);
    }
  };
  useLayoutEffect(() => {
    applyTheme(theme);
  }, [theme]);
  useEffect(() => {
    store.start();
    const presence = () => store.presence(!document.hidden);
    const onInstall = (event: Event) => {
      if (!isInstallEvent(event)) return;
      event.preventDefault();
      setInstall(event);
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
  const unauthorized = state.connection === "unauthorized";
  return (
    <div className={styles.appShell}>
      <a className={styles.skipLink} href="#message-input">
        Skip to message input
      </a>
      <AnimatePresence initial={false}>
        {menu && (
          <motion.button
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
            {preview
              ? "Offline preview"
              : unauthorized
                ? "Sign-in required"
                : connected
                  ? "Connected"
                  : "Reconnecting…"}
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
            <motion.span
              key={theme}
              aria-hidden="true"
              initial={reduced ? false : { opacity: 0, rotate: -90 }}
              animate={{ opacity: 1, rotate: 0 }}
              transition={{ duration: reduced ? 0 : 0.24 }}
            >
              {theme === "mocha" ? <Sun /> : <Moon />}
            </motion.span>
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
            {logoutError !== null && <span role="alert">{logoutError}</span>}
            {onLogout && (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  void onLogout().catch(() => setLogoutError("Sign-out failed. Try again."));
                }}
              >
                Sign out
              </Button>
            )}
            {preview && <span className={styles.previewBadge}>Preview</span>}
            {install && (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  void promptInstall(install);
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
            {unauthorized ? (
              <a href="/">Session expired or revoked. Sign in again.</a>
            ) : state.connection === "offline" ? (
              "Disconnected. Refresh to reconnect."
            ) : (
              "Reconnecting to q15…"
            )}
          </output>
        )}
        {(state.notice !== null || state.error !== null) && (
          <div
            className={clsx(styles.noticeBanner, state.error !== null && styles.errorBanner)}
            role={state.error === null ? "status" : "alert"}
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
