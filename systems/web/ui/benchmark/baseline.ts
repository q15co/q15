export function baselineAdapter(selected: string | undefined) {
  return selected ?? 'export { codec } from "./base-codec";\n';
}
