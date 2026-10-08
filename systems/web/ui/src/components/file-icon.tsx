// Glyphs adapted from catppuccin/vscode-icons at b6915da9; see file-icons-LICENSE.
const glyphs = {
  pdf: (
    <path
      stroke="var(--destructive)"
      d="M2.8 14.34c1.81-1.25 3.02-3.16 3.91-5.5.9-2.33 1.86-4.33 1.44-6.63-.06-.36-.57-.73-.83-.7-1.02.06-.95 1.21-.85 1.9.24 1.71 1.56 3.7 2.84 5.56 1.27 1.87 2.32 2.16 3.78 2.26.5.03 1.25-.14 1.37-.58.77-2.8-9.02-.54-12.28 2.08-.4.33-.86 1-.6 1.46.2.36.87.4 1.23.15h0Z"
    />
  ),
  image: (
    <g>
      <path
        stroke="var(--warning)"
        d="M11.5 6A1.5 1.5 0 0110 7.5 1.5 1.5 0 018.5 6 1.5 1.5 0 0110 4.5 1.5 1.5 0 0111.5 6"
      />
      <path stroke="var(--green)" d="M7.5 13.5 11 10c.5-.5 1.5-.5 2 0l1.5 1.5" />
      <path stroke="var(--green)" d="m1.5 9.5 2-2C4 7 5 7 5.5 7.5l4 4" />
      <path
        stroke="var(--sapphire)"
        d="M3 2.5h10c.83 0 1.5.67 1.5 1.5v8c0 .83-.67 1.5-1.5 1.5H3A1.5 1.5 0 011.5 12V4c0-.83.67-1.5 1.5-1.5"
      />
    </g>
  ),
  audio: (
    <g stroke="var(--maroon)">
      <path d="M5.5 12.5a2 2 0 01-2 2 2 2 0 01-2-2 2 2 0 012-2 2 2 0 012 2m9-2a2 2 0 01-2 2 2 2 0 01-2-2 2 2 0 012-2 2 2 0 012 2" />
      <path d="M5.5 12.5V5c0-.54.44-1.21 1.35-1.5l6.3-2c.9 0 1.35.88 1.35 1.5v7.58m-9-3.08 9-3" />
    </g>
  ),
  video: (
    <g stroke="var(--sapphire)">
      <path d="M3 2.5h10c.83 0 1.5.67 1.5 1.5v9c0 .83-.67 1.5-1.5 1.5H3A1.5 1.5 0 011.5 13V4c0-.83.67-1.5 1.5-1.5m-1.5 3h13" />
      <path d="m3.5 5.5 2-3m1.5 3 2-3m1.5 3 2-3M6.5 8v4l4-2z" />
    </g>
  ),
  archive: (
    <path
      stroke="var(--foreground)"
      d="m5.5 10v1m1-2v1m-1-2v1m1-2v1m-1-2v1m1-2v1m-1-2v1m0-3v1m1 0v1m7 2.5v6c0 1.105-0.8954 2-2 2h-7c-1.105 0-2-0.8954-2-2v-9c0-1.1 0.9-2 2-2h4.01m-0.01 0 5 5h-4c-0.5523 0-1-0.4477-1-1z"
    />
  ),
  file: (
    <g stroke="var(--foreground)">
      <path d="M13.5 6.5v6a2 2 0 01-2 2h-7a2 2 0 01-2-2v-9c0-1.1.9-2 2-2h4.01" />
      <path d="m8.5 1.5 5 5h-4a1 1 0 01-1-1zm-3 10h5m-5-3h5m-5-3h1" />
    </g>
  ),
};

function fileIconKind(filename: string, contentType: string): keyof typeof glyphs {
  if (contentType === "application/pdf" || /\.pdf$/iu.test(filename)) return "pdf";
  if (contentType.startsWith("image/")) return "image";
  if (contentType.startsWith("audio/")) return "audio";
  if (contentType.startsWith("video/")) return "video";
  if (/\.(?:zip|7z|rar|gz|tar|bz2|xz)$/iu.test(filename)) return "archive";
  return "file";
}

export function FileIcon({ filename, contentType }: { filename: string; contentType: string }) {
  return (
    <svg
      viewBox="0 0 16 16"
      fill="none"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      {glyphs[fileIconKind(filename, contentType)]}
    </svg>
  );
}
