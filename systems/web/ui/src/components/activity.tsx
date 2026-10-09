import { clsx } from "clsx";
import {
  Brain,
  ChevronDown,
  CircleAlert,
  Globe,
  LoaderCircle,
  Terminal,
  Wrench,
} from "lucide-react";
import * as motion from "motion/react-m";
import { memo } from "react";

import type { Source, ToolActivityItem } from "../domain/activity";
import type { ChatMessage } from "../domain/chat";
import type { Part } from "../generated/protocol";

import { presentTurn } from "../domain/activity";
import { isRecord } from "../shared/type-guards";
import { pretty } from "./format";
import { MessageView } from "./message";
import { PartView } from "./parts";
import { recursiveAxes } from "./ui/font-motion";
import { useMotionPreference } from "./ui/motion-preference";

import styles from "./activity.module.css";

function context(part: Part) {
  try {
    const args: unknown = JSON.parse(part.tool_call?.arguments ?? "");
    if (!isRecord(args)) return "";
    for (const key of ["command", "query", "url", "path", "file_path"])
      if (typeof args[key] === "string") return args[key];
  } catch {
    /* Invalid arguments remain visible in the expanded row. */
  }
  return "";
}

function toolPresentation(item: ToolActivityItem, working: boolean) {
  const name = item.source.part.tool_call?.name ?? "";
  const error =
    item.source.part.is_error === true || item.results.some((s) => s.part.is_error === true);
  const finished = item.source.part.part_type === "tool_result" || item.results.length > 0;
  const running = working && !finished;
  const command = ["exec", "bash", "exec_command"].includes(name);
  const web = ["web_search", "web_fetch"].includes(name);
  const title = command
    ? running
      ? "Running command"
      : "Ran command"
    : name === "web_search"
      ? running
        ? "Searching the web"
        : "Searched the web"
      : name === "web_fetch"
        ? running
          ? "Reading page"
          : "Read page"
        : name === ""
          ? "Tool result"
          : name.replaceAll("_", " ");
  return {
    name,
    title,
    error,
    running,
    status: error ? "Failed" : finished ? "Completed" : running ? "Running" : "No result",
    Icon: error ? CircleAlert : running ? LoaderCircle : command ? Terminal : web ? Globe : Wrench,
  };
}

function MessageAnchor({ source }: { source: Source }) {
  return (
    <span
      className={styles.anchor}
      id={`message-${source.message.key}`}
      data-message-key={source.message.key}
    />
  );
}

const ToolActivity = memo(
  function ToolActivity({
    item,
    working,
    anchors,
  }: {
    item: ToolActivityItem;
    working: boolean;
    anchors: Set<string>;
  }) {
    const reduced = useMotionPreference();
    const { name, title, status, error, running, Icon } = toolPresentation(item, working);
    const part = item.source.part;
    const description = context(part);
    const results = part.part_type === "tool_result" ? [item.source] : item.results;
    return (
      <details
        className={clsx(styles.tool, error && styles.error)}
        data-tool-call-id={part.tool_call?.id ?? part.tool_call_id}
        data-running={running || undefined}
      >
        <summary>
          {anchors.has(item.source.key) && <MessageAnchor source={item.source} />}
          <Icon size={16} className={running ? styles.spinning : undefined} aria-hidden="true" />
          <span className={styles.toolTitle}>{title}</span>
          {description !== "" && <span className={styles.context}>{description}</span>}
          <motion.span
            key={status}
            className={styles.status}
            data-status={status}
            initial={reduced ? false : { opacity: 0 }}
            animate={{ opacity: 1 }}
            transition={{ duration: reduced ? 0 : 0.18 }}
          >
            {status}
          </motion.span>
          <ChevronDown size={13} className={styles.chevron} aria-hidden="true" />
        </summary>
        <div className={styles.toolBody}>
          {part.tool_call && (
            <section>
              <h3>{name} · Input</h3>
              <pre>{pretty(part.tool_call.arguments)}</pre>
            </section>
          )}
          {results.map((result) => (
            <section
              key={result.key}
              className={result.part.is_error === true ? styles.error : undefined}
            >
              {result !== item.source && anchors.has(result.key) && (
                <MessageAnchor source={result} />
              )}
              <h3>{result.part.is_error === true ? "Error" : "Output"}</h3>
              <pre>{result.part.content ?? ""}</pre>
            </section>
          ))}
          {results.length === 0 && (
            <p className={styles.noResult}>
              {working ? "Waiting for the tool result…" : "No result was recorded for this call."}
            </p>
          )}
        </div>
      </details>
    );
  },
  (previous, next) =>
    previous.working === next.working &&
    previous.item.source.key === next.item.source.key &&
    previous.item.source.part === next.item.source.part &&
    previous.anchors.has(previous.item.source.key) === next.anchors.has(next.item.source.key) &&
    previous.item.results.length === next.item.results.length &&
    previous.item.results.every(
      (result, index) =>
        result.part === next.item.results[index]?.part &&
        result.key === next.item.results[index].key &&
        previous.anchors.has(result.key) === next.anchors.has(result.key),
    ),
);

export function TurnView({
  messages,
  working,
}: {
  messages: readonly ChatMessage[];
  working: boolean;
}) {
  const reduced = useMotionPreference();
  const { activity, answers } = presentTurn(messages);
  const usingTool = activity.some(
    (item) =>
      item.kind === "tool" &&
      item.source.part.part_type === "tool_call" &&
      toolPresentation(item, working).running,
  );
  const phase = usingTool ? "tool" : "thinking";
  const toolCount = activity.filter((item) => item.kind === "tool").length;
  const errors = activity.filter(
    (item) =>
      item.source.part.is_error === true ||
      (item.kind === "tool" && item.results.some((s) => s.part.is_error === true)),
  ).length;
  const status = messages.findLast((m) => (m.status ?? "") !== "")?.status;
  const label = working
    ? usingTool
      ? "Using tools…"
      : "Thinking…"
    : status === "aborted"
      ? "Work stopped"
      : status === "failed"
        ? "Work failed"
        : toolCount > 0
          ? `Used ${toolCount} ${toolCount === 1 ? "tool" : "tools"}`
          : "Thought process";
  const anchored = new Set(answers.map((m) => m.key));
  const anchors = new Set<string>();
  for (const item of activity) {
    const sources = item.kind === "tool" ? [item.source, ...item.results] : [item.source];
    for (const source of sources) {
      if (!anchored.has(source.message.key)) {
        anchors.add(source.key);
        anchored.add(source.message.key);
      }
    }
  }
  const empty = messages.filter((m) => m.role !== "user" && !anchored.has(m.key));
  return (
    <>
      {messages
        .filter((m) => m.role === "user")
        .map((m) => (
          <MessageView message={m} key={m.key} />
        ))}
      {(working || messages.some((m) => m.role !== "user")) && (
        <article className={styles.turn} data-agent-turn aria-label="Agent response">
          {(activity.length > 0 || working || empty.length > 0) && (
            <details
              className={styles.activity}
              data-agent-activity
              data-working={working || undefined}
              data-phase={phase}
            >
              <summary>
                {empty.map((m) => (
                  <span
                    key={m.key}
                    className={styles.anchor}
                    id={`message-${m.key}`}
                    data-message-key={m.key}
                  />
                ))}
                <motion.span
                  className={styles.summaryContent}
                  initial={reduced ? false : { y: 0 }}
                  animate={{ y: working && !reduced ? [0, -3, 0] : 0 }}
                  transition={{
                    duration: reduced ? 0 : working ? 4.8 : 0.3,
                    repeat: working && !reduced ? Infinity : 0,
                    ease: "easeInOut",
                  }}
                >
                  {usingTool ? (
                    <Wrench size={15} className={styles.thinking} aria-hidden="true" />
                  ) : (
                    <Brain
                      size={15}
                      className={working ? styles.thinking : undefined}
                      aria-hidden="true"
                    />
                  )}
                  <motion.span
                    className={styles.activityLabel}
                    initial={reduced ? false : { fontVariationSettings: recursiveAxes(0.2, 500) }}
                    animate={{
                      fontVariationSettings:
                        working && !reduced
                          ? [
                              recursiveAxes(0.2, 500),
                              recursiveAxes(0.9, 570, -4),
                              recursiveAxes(0.2, 500),
                            ]
                          : recursiveAxes(0.2, 500),
                    }}
                    transition={{
                      duration: reduced ? 0 : working ? 4.8 : 0.3,
                      repeat: working && !reduced ? Infinity : 0,
                      ease: "easeInOut",
                    }}
                  >
                    {label}
                  </motion.span>
                  {working && (
                    <span className={styles.workingDots} aria-hidden="true">
                      <i />
                      <i />
                      <i />
                    </span>
                  )}
                  {working && toolCount > 0 && (
                    <span className={styles.status}>
                      {toolCount} {toolCount === 1 ? "tool" : "tools"}
                    </span>
                  )}
                  {errors > 0 && (
                    <span className={styles.error}>
                      {errors} {errors === 1 ? "error" : "errors"}
                    </span>
                  )}
                  <ChevronDown size={14} className={styles.chevron} aria-hidden="true" />
                </motion.span>
              </summary>
              <div className={styles.timeline}>
                {activity.map((item) =>
                  item.kind === "tool" ? (
                    <ToolActivity item={item} working={working} anchors={anchors} key={item.key} />
                  ) : (
                    <div className={styles.commentary} key={item.key}>
                      {anchors.has(item.source.key) && <MessageAnchor source={item.source} />}
                      <PartView
                        part={item.source.part}
                        streaming={item.source.message.status === "streaming"}
                      />
                    </div>
                  ),
                )}
                {working && activity.length === 0 && (
                  <span className={styles.noResult}>q15 is thinking…</span>
                )}
              </div>
            </details>
          )}
          {answers.map((m) => (
            <MessageView message={m} key={m.key} />
          ))}
        </article>
      )}
    </>
  );
}
