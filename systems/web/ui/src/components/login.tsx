import { KeyRound, Moon, Sun, ArrowRight } from "lucide-react";
import { useLayoutEffect, useState } from "react";

import type { OwnerAuthentication } from "../application/auth";

import { applyTheme, initialTheme } from "../theme";
import { Brand } from "./brand";
import { Button } from "./ui/button";

import styles from "./login.module.css";

export function Login({ authentication }: { authentication: OwnerAuthentication }) {
  const [theme, setTheme] = useState(initialTheme);
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("");
  const [options, setOptions] = useState("");
  const [response, setResponse] = useState("");
  useLayoutEffect(() => applyTheme(theme), [theme]);
  const signIn = async () => {
    setBusy(true);
    setStatus("Waiting for authenticator");
    try {
      await authentication.signIn();
    } catch (error) {
      setStatus(
        error instanceof Error
          ? error.message
          : "Sign-in failed. The authenticator returned no response. Try your device or security key again.",
      );
    } finally {
      setBusy(false);
    }
  };
  const enroll = async () => {
    setBusy(true);
    setStatus("Waiting for device or security key");
    try {
      setResponse(await authentication.createCredential(options));
      setOptions("");
      setStatus("Paste the response into the waiting command on the host, then sign in.");
    } catch (error) {
      setStatus(
        error instanceof Error
          ? error.message
          : "Credential creation failed. The authenticator returned no credential. Check the options and try again.",
      );
    } finally {
      setBusy(false);
    }
  };
  return (
    <main className={styles.screen}>
      <header className={styles.header}>
        <a className={styles.brand} href="/" aria-label="q15 home">
          <Brand />
        </a>
        <Button
          variant="ghost"
          size="icon"
          aria-label={theme === "mocha" ? "Switch to Latte theme" : "Switch to Mocha theme"}
          onClick={() => setTheme(theme === "mocha" ? "latte" : "mocha")}
        >
          {theme === "mocha" ? <Sun /> : <Moon />}
        </Button>
      </header>
      <section className={styles.card} aria-labelledby="signin-title">
        <Brand lockup />
        <h1 id="signin-title">Sign in to q15</h1>
        <p>Sign in with your enrolled device or security key.</p>
        <Button
          className={styles.signin}
          disabled={busy}
          onClick={() => {
            void signIn();
          }}
        >
          Sign in
          <ArrowRight aria-hidden="true" size={18} />
        </Button>
        <p className={styles.hint}>Your PIN or biometric check stays on your authenticator.</p>
        <output className={styles.status} aria-live="polite">
          {status}
        </output>
        <details className={styles.enrollment}>
          <summary>
            <KeyRound aria-hidden="true" size={16} />
            Enroll on the host
          </summary>
          <p>
            Run <code>q15-web auth enroll DEVICE_NAME</code> on the host, then paste its options
            here. Enrollment completes only on the host.
          </p>
          <p className={styles.hint}>
            Use a device-bound authenticator or security key with a PIN. Synced credentials are
            refused.
          </p>
          <label htmlFor="options">Options from the host</label>
          <textarea
            id="options"
            value={options}
            onChange={(event) => setOptions(event.target.value)}
            spellCheck={false}
            autoComplete="off"
          />
          <Button
            variant="outline"
            disabled={busy || options.trim() === ""}
            onClick={() => {
              void enroll();
            }}
          >
            Create device credential
          </Button>
          <label htmlFor="response">Response to paste into the host</label>
          <textarea id="response" value={response} readOnly spellCheck={false} autoComplete="off" />
        </details>
      </section>
    </main>
  );
}
