/* v8 ignore file */
import { clsx } from "clsx";

import { highlightShell, shellKeys } from "./shell";

import styles from "./toolcall.module.css";

type Scalar = string | number | boolean | null;
type Fields = { [key: string]: unknown };

const chipLimit = 16;
const itemLimit = 8;
const blockLimit = 400;

const unparseable = Symbol("unparseable");

function isScalar(value: unknown): value is Scalar {
  return (
    value === null ||
    typeof value === "string" ||
    typeof value === "number" ||
    typeof value === "boolean"
  );
}

function isFields(value: unknown): value is Fields {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function parseJson(raw: string): unknown {
  if (raw.trim() === "") return unparseable;
  try {
    const parsed: unknown = JSON.parse(raw);
    return parsed;
  } catch {
    return unparseable;
  }
}

/** Stable keys for a slice of values that may repeat. */
function keyed<T>(values: readonly T[]): { key: string; value: T }[] {
  const seen = new Map<string, number>();
  const rows: { key: string; value: T }[] = [];
  for (const value of values) {
    const label = JSON.stringify(value);
    const count = seen.get(label) ?? 0;
    seen.set(label, count + 1);
    rows.push({ key: `${label}#${count}`, value });
  }
  return rows;
}

function ShellBlock({ code }: { code: string }) {
  return (
    <pre className={styles.shell}>
      <code>
        {highlightShell(code).map((token) => (
          <span className={styles[token.kind]} key={token.at}>
            {token.text}
          </span>
        ))}
      </code>
    </pre>
  );
}

function Chip({ value }: { value: Scalar }) {
  const kind = value === null ? "null" : typeof value;
  return (
    <span className={styles.chip} data-kind={kind}>
      <span className={styles.chipText}>{String(value)}</span>
    </span>
  );
}

function TextValue({ name, value, depth }: { name: string; value: string; depth: number }) {
  if (shellKeys.has(name.toLowerCase())) return <ShellBlock code={value} />;
  if (value === "") return <span className={styles.empty}>empty</span>;
  if (value.includes("\n") || value.length > blockLimit)
    return <pre className={styles.block}>{value}</pre>;
  // Long strings stretch a pill to its own line; keep chips for short scalars.
  if (depth === 1 && value.length <= 48) return <Chip value={value} />;
  return <span className={styles.string}>{value}</span>;
}

function ItemList({ values, depth }: { values: readonly unknown[]; depth: number }) {
  if (values.length === 0) return <span className={styles.empty}>empty</span>;
  const scalars: Scalar[] = [];
  for (const value of values) if (isScalar(value)) scalars.push(value);
  if (scalars.length === values.length)
    return (
      <ul className={styles.chips}>
        {keyed(scalars.slice(0, chipLimit)).map((row) => (
          <li key={row.key}>
            <Chip value={row.value} />
          </li>
        ))}
        {values.length > chipLimit && (
          <li className={styles.more}>+{values.length - chipLimit} more</li>
        )}
      </ul>
    );
  return (
    <ul className={styles.items}>
      {keyed(values.slice(0, itemLimit)).map((row) => (
        <li className={styles.item} key={row.key}>
          <ValueView name="" value={row.value} depth={depth + 1} />
        </li>
      ))}
      {values.length > itemLimit && (
        <li className={styles.more}>+{values.length - itemLimit} more</li>
      )}
    </ul>
  );
}

function ValueView({ name, value, depth }: { name: string; value: unknown; depth: number }) {
  if (typeof value === "string") return <TextValue name={name} value={value} depth={depth} />;
  if (isScalar(value)) return <Chip value={value} />;
  if (Array.isArray(value)) return <ItemList values={value} depth={depth} />;
  if (isFields(value)) return <FieldList entries={Object.entries(value)} depth={depth + 1} />;
  return null;
}

function FieldList({
  entries,
  depth,
  className,
}: {
  entries: readonly (readonly [string, unknown])[];
  depth: number;
  className?: string | undefined;
}) {
  if (entries.length === 0) return <span className={clsx(styles.empty, className)}>empty</span>;
  return (
    <dl className={clsx(styles.fields, depth > 1 && styles.nested, className)}>
      {entries.map(([key, value]) => (
        <div className={styles.field} key={key}>
          <dt className={styles.key}>{key}</dt>
          <dd className={styles.value}>
            <ValueView name={key} value={value} depth={depth} />
          </dd>
        </div>
      ))}
    </dl>
  );
}

function JsonBlock({ raw, className }: { raw: string; className?: string | undefined }) {
  const value = parseJson(raw);
  if (value === unparseable)
    return <pre className={clsx(styles.root, styles.raw, className)}>{raw}</pre>;
  if (isFields(value))
    return (
      <FieldList
        entries={Object.entries(value)}
        depth={1}
        className={clsx(styles.root, className)}
      />
    );
  return (
    <div className={clsx(styles.root, className)}>
      <ValueView name="" value={value} depth={1} />
    </div>
  );
}

/** A tool call's arguments, rendered as readable fields rather than raw JSON. */
export function ToolArguments({
  raw,
  className,
}: {
  raw?: string | undefined;
  className?: string | undefined;
}) {
  return <JsonBlock raw={raw ?? ""} className={className} />;
}

/** A tool result, structured when it happens to be JSON and verbatim otherwise. */
export function ToolResult({
  content,
  className,
}: {
  content?: string | undefined;
  className?: string | undefined;
}) {
  return <JsonBlock raw={content ?? ""} className={className} />;
}
