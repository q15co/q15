import { ArrowUp, ListPlus, Square, CornerDownLeft, Paperclip, X } from "lucide-react";
import { AnimatePresence } from "motion/react";
import * as motion from "motion/react-m";
import { useRef, useState } from "react";

import type { ChatStore } from "../application/chat-store";
import type { ChatState } from "../domain/chat";

import { FileIcon } from "./file-icon";
import { Button } from "./ui/button";
import { useMotionPreference } from "./ui/motion-preference";

import styles from "./composer.module.css";

function fileSize(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
}

export function Composer({ store, state }: { store: ChatStore; state: ChatState }) {
  const reduced = useMotionPreference();
  const [text, setText] = useState("");
  const [files, setFiles] = useState<readonly { id: string; file: File }[]>([]);
  const [uploading, setUploading] = useState(false);
  const picker = useRef<HTMLInputElement>(null);
  const input = useRef<HTMLTextAreaElement>(null);
  const clear = () => {
    setText("");
    setFiles([]);
    input.current?.focus();
  };
  const sendFiles = async () => {
    setUploading(true);
    try {
      if (
        await store.sendFiles(
          text,
          files.map((entry) => entry.file),
        )
      )
        clear();
    } finally {
      setUploading(false);
    }
  };
  const submit = () => {
    if (uploading) return;
    if (files.length > 0) {
      void sendFiles();
      return;
    }
    if (store.send(text)) clear();
  };
  const busy =
    state.active !== null ||
    state.pending.some((p) => p.state === "sending" || p.state === "accepted");
  return (
    <div className={styles.composerArea}>
      <form
        className={styles.composer}
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        {files.length > 0 && (
          <section className={styles.attachmentTray} aria-label="Selected attachments">
            <div className={styles.attachmentHeader}>
              <Paperclip size={14} aria-hidden="true" />
              <span>Attachments</span>
              <span className={styles.attachmentCount}>{files.length}</span>
            </div>
            <ul className={styles.attachments}>
              {files.map(({ file, id }) => (
                <li key={id} className={styles.attachmentCard}>
                  <span className={styles.fileIcon}>
                    <FileIcon filename={file.name} contentType={file.type} />
                  </span>
                  <div className={styles.fileDetails}>
                    <span className={styles.filename} title={file.name}>
                      {file.name}
                    </span>
                    <small className={styles.fileMeta}>
                      {file.name.match(/\.([a-z\d]{1,8})$/iu)?.[1]?.toUpperCase() ?? "File"}
                      {" · "}
                      {fileSize(file.size)}
                    </small>
                  </div>
                  <Button
                    className={styles.removeAttachment}
                    variant="ghost"
                    size="icon"
                    disabled={uploading}
                    aria-label={`Remove ${file.name}`}
                    onClick={() => setFiles(files.filter((entry) => entry.id !== id))}
                  >
                    <X size={14} />
                  </Button>
                </li>
              ))}
            </ul>
          </section>
        )}
        <label htmlFor="message-input" className="sr-only">
          Message q15
        </label>
        <textarea
          id="message-input"
          ref={input}
          value={text}
          disabled={uploading}
          rows={1}
          placeholder="Message q15"
          onChange={(event) => setText(event.target.value)}
          onKeyDown={(event) => {
            if (
              event.key === "Enter" &&
              !event.shiftKey &&
              !event.nativeEvent.isComposing &&
              !matchMedia("(pointer: coarse)").matches
            ) {
              event.preventDefault();
              submit();
            }
          }}
        />
        <input
          ref={picker}
          type="file"
          multiple
          hidden
          aria-label="Choose attachments"
          disabled={uploading}
          onChange={(event) => {
            setFiles([
              ...files,
              ...Array.from(event.target.files ?? []).map((file) => ({
                id: crypto.randomUUID(),
                file,
              })),
            ]);
            event.target.value = "";
          }}
        />
        <div className={styles.composerToolbar}>
          {busy && <span className={styles.composerHint}>Next message will be queued</span>}
          <div className={styles.composerButtons}>
            <Button
              variant="ghost"
              size="icon"
              aria-label="Attach files"
              disabled={uploading || state.connection !== "connected"}
              onClick={() => picker.current?.click()}
            >
              <Paperclip />
            </Button>
            {uploading && <output>Uploading…</output>}
            {busy && (
              <motion.span
                className={styles.stopControl}
                initial={reduced ? false : { opacity: 0 }}
                animate={{ opacity: 1 }}
                transition={{ duration: reduced ? 0 : 0.15 }}
              >
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => store.abort()}
                  disabled={state.connection !== "connected"}
                  aria-label="Stop response"
                >
                  <Square />
                  Stop
                </Button>
              </motion.span>
            )}
            <Button
              type="submit"
              size="icon"
              disabled={
                uploading ||
                (text.trim() === "" && files.length === 0) ||
                state.connection !== "connected"
              }
              aria-label={busy ? "Queue message" : "Send message"}
            >
              <AnimatePresence initial={false} mode="wait">
                <motion.span
                  key={busy ? "queue" : "send"}
                  aria-hidden="true"
                  initial={reduced ? false : { opacity: 0, y: 4 }}
                  animate={{ opacity: 1, y: 0 }}
                  exit={{ opacity: 0, y: reduced ? 0 : -4 }}
                  transition={{ duration: reduced ? 0 : 0.12 }}
                >
                  {busy ? <ListPlus /> : <ArrowUp />}
                </motion.span>
              </AnimatePresence>
            </Button>
          </div>
        </div>
      </form>
      <div className={styles.composerFooter}>
        <span className={styles.keyboardHint}>
          <CornerDownLeft size={12} /> send<span>Shift + Enter for a new line</span>
        </span>
      </div>
    </div>
  );
}
