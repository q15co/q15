import { useEffect, useEffectEvent, useRef, useState } from "react";
import Markdown from "react-markdown";
import remarkGfm from "remark-gfm";

import styles from "./markdown.module.css";

const plugins = [remarkGfm];

function useStreamingText(text: string, streaming: boolean) {
  const [rendered, setRendered] = useState(text);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const commit = useEffectEvent(() => setRendered(text));
  useEffect(() => {
    if (!streaming) {
      clearTimeout(timer.current);
      timer.current = undefined;
    } else if (text !== rendered && timer.current === undefined) {
      // Parse the complete latest Markdown, including edits to earlier blocks.
      // A fixed window bounds staleness even when frames arrive continuously.
      timer.current = setTimeout(() => {
        timer.current = undefined;
        commit();
      }, 24);
    }
  }, [text, rendered, streaming]);
  useEffect(() => () => clearTimeout(timer.current), []);
  return streaming ? rendered : text;
}

export function MarkdownView({ text, streaming: live }: { text: string; streaming?: boolean }) {
  const streaming = live ?? false;
  const rendered = useStreamingText(text, streaming);
  return (
    <div className={styles.markdown}>
      <Markdown remarkPlugins={plugins}>{rendered}</Markdown>
    </div>
  );
}
