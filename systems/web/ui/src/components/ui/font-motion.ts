// Recursive preserves glyph widths across CASL, wght and slnt. Keep MONO fixed
// and cursive alternates off so animated UI text keeps its layout and legibility.
export function recursiveAxes(casual: number, weight: number, slant = 0) {
  return `"MONO" 0, "CASL" ${casual}, "wght" ${weight}, "slnt" ${slant}, "CRSV" 0`;
}
