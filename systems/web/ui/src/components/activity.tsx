import {
  Brain,
  ChevronDown,
  CircleAlert,
  Globe,
  LoaderCircle,
  Terminal,
  Wrench,
} from "lucide-react";
import { clsx } from "clsx";
import * as m from "motion/react-m";
import type { ChatMessage } from "../chat-store";
import type { Part } from "../generated/protocol";
import { MessageView } from "./message";
import { PartView, pretty } from "./parts";
import { useMotionPreference } from "./ui/motion";
import { recursiveAxes } from "./ui/font-motion";
import styles from "./activity.module.css";

interface Source {
  key: string;
  message: ChatMessage;
  part: Part;
}
interface ActivityItem {
  source: Source;
  results: Source[];
}

// History separates calls and results into messages; live drafts contain both.
// Present either shape without changing canonical message or part identities.
export function presentTurn(messages: ChatMessage[]) {
  const sources = messages
    .filter((m) => m.role !== "user")
    .flatMap((message) =>
      message.parts.map((part, i) => ({ key: `${message.key}/${i}`, message, part })),
    );
  const lastTool = sources.findLastIndex(
    (s) => s.part.part_type === "tool_call" || s.part.part_type === "tool_result",
  );
  const lastAnswer = sources.findLast(
    (s, i) =>
      i > lastTool &&
      s.message.role === "assistant" &&
      s.part.part_type === "text" &&
      !s.part.disposition,
  );
  const answerKeys = new Set<string>();
  const answerParts = new Map<string, Part[]>();
  const activity: ActivityItem[] = [];
  const calls = new Map<string, ActivityItem[]>();
  sources.forEach((source, i) => {
    const { part, message } = source;
    const final =
      part.part_type === "text" &&
      message.role === "assistant" &&
      (part.disposition === "final" ||
        (!part.disposition && i > lastTool && message.key === lastAnswer?.message.key));
    if (final || !["text", "reasoning", "tool_call", "tool_result"].includes(part.part_type)) {
      answerKeys.add(message.key);
      const parts = answerParts.get(message.key) ?? [];
      parts.push(part);
      answerParts.set(message.key, parts);
    } else if (
      part.part_type === "tool_result" &&
      part.tool_call_id &&
      calls.get(part.tool_call_id)?.length
    ) {
      // Match the nearest preceding unmatched call, even if a provider reuses IDs.
      calls.get(part.tool_call_id)!.pop()!.results.push(source);
    } else {
      const item = { source, results: [] as Source[] };
      activity.push(item);
      if (part.part_type === "tool_call" && part.tool_call?.id) {
        const pending = calls.get(part.tool_call.id) ?? [];
        pending.push(item);
        calls.set(part.tool_call.id, pending);
      }
    }
  });
  return {
    activity,
    answers: messages
      .filter((m) => answerKeys.has(m.key))
      .map((m) => ({ ...m, parts: answerParts.get(m.key)! })),
  };
}

function context(part: Part) {
  try {
    const args: unknown = JSON.parse(part.tool_call?.arguments ?? "");
    if (!args || typeof args !== "object" || Array.isArray(args)) return "";
    const fields = args as Record<string, unknown>;
    for (const key of ["command", "query", "url", "path", "file_path"])
      if (typeof fields[key] === "string") return fields[key];
  } catch {
    /* Invalid arguments remain visible in the expanded row. */
  }
  return "";
}

function toolPresentation(item: ActivityItem, working: boolean) {
  const name = item.source.part.tool_call?.name ?? "";
  const error = item.source.part.is_error || item.results.some((s) => s.part.is_error);
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
        : name
          ? name.replaceAll("_", " ")
          : "Tool result";
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

function ToolActivity({
  item,
  working,
  anchors,
}: {
  item: ActivityItem;
  working: boolean;
  anchors: Set<string>;
}) {
  const reduced = useMotionPreference();
  const { name, title, status, error, running, Icon } = toolPresentation(item, working);
  const part = item.source.part;
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
        {context(part) && <span className={styles.context}>{context(part)}</span>}
        <m.span
          key={status}
          className={styles.status}
          data-status={status}
          initial={reduced ? false : { opacity: 0 }}
          animate={{ opacity: 1 }}
          transition={{ duration: reduced ? 0 : 0.18 }}
        >
          {status}
        </m.span>
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
          <section key={result.key} className={result.part.is_error ? styles.error : undefined}>
            {result !== item.source && anchors.has(result.key) && <MessageAnchor source={result} />}
            <h3>{result.part.is_error ? "Error" : "Output"}</h3>
            <pre>{result.part.content ?? ""}</pre>
          </section>
        ))}
        {!results.length && (
          <p className={styles.noResult}>
            {working ? "Waiting for the tool result…" : "No result was recorded for this call."}
          </p>
        )}
      </div>
    </details>
  );
}

export function TurnView({ messages, working }: { messages: ChatMessage[]; working: boolean }) {
  const reduced = useMotionPreference();
  const { activity, answers } = presentTurn(messages);
  const usingTool = activity.some(
    (item) => item.source.part.part_type === "tool_call" && toolPresentation(item, working).running,
  );
  const phase = usingTool ? "tool" : "thinking";
  const toolCount = activity.filter((item) =>
    ["tool_call", "tool_result"].includes(item.source.part.part_type),
  ).length;
  const errors = activity.filter(
    (item) => item.source.part.is_error || item.results.some((s) => s.part.is_error),
  ).length;
  const status = messages.findLast((m) => m.status)?.status;
  const label = working
    ? usingTool
      ? "Using tools…"
      : "Thinking…"
    : status === "aborted"
      ? "Work stopped"
      : status === "failed"
        ? "Work failed"
        : toolCount
          ? `Used ${toolCount} ${toolCount === 1 ? "tool" : "tools"}`
          : "Thought process";
  const anchored = new Set(answers.map((m) => m.key));
  const anchors = new Set<string>();
  for (const item of activity)
    for (const source of [item.source, ...item.results]) {
      if (!anchored.has(source.message.key)) {
        anchors.add(source.key);
        anchored.add(source.message.key);
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
            <m.span
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
              <m.span
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
              </m.span>
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
            </m.span>
          </summary>
          <div className={styles.timeline}>
            {activity.map((item) =>
              ["tool_call", "tool_result"].includes(item.source.part.part_type) ? (
                <ToolActivity
                  item={item}
                  working={working}
                  anchors={anchors}
                  key={item.source.key}
                />
              ) : (
                <div className={styles.commentary} key={item.source.key}>
                  {anchors.has(item.source.key) && <MessageAnchor source={item.source} />}
                  <PartView part={item.source.part} />
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
    </>
  );
}
