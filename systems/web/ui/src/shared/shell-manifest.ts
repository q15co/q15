import { isRecord } from "./type-guards";

export interface ShellManifest {
  version: string;
  paths: string[];
}

function isShellManifest(value: unknown): value is ShellManifest {
  return (
    isRecord(value) &&
    typeof value.version === "string" &&
    Array.isArray(value.paths) &&
    value.paths.every((path: unknown) => typeof path === "string")
  );
}

export function parseShellManifest(value: unknown): ShellManifest {
  if (!isShellManifest(value)) throw new Error("Unsupported shell manifest.");
  return value;
}
