import Markdown from "react-markdown";
import remarkGfm from "remark-gfm";

import styles from "./markdown.module.css";

export function MarkdownView({ text }: { text: string }) {
  return (
    <div className={styles.markdown}>
      <Markdown remarkPlugins={[remarkGfm]}>{text}</Markdown>
    </div>
  );
}
