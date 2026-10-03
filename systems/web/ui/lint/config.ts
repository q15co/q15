import type { UserConfig } from "vite-plus";

// Paths are relative to vite.config.ts, including when lint-changed supplies files.
// Allow only canonical relative paths, so ./../ and nested traversal cannot bypass a layer.
type LintRules = NonNullable<NonNullable<UserConfig["lint"]>["rules"]>;
function imports(allowed: readonly string[]): LintRules["eslint/no-restricted-imports"] {
  return [
    "error",
    {
      patterns: [
        {
          group: ["**", ...allowed.map((path) => `!${path}`)],
          message: "Import from an allowed layer. See README.md: Architecture and linting.",
        },
        {
          regex: "\\./\\.\\./|/\\.\\./|/\\./|\\\\|[?#]|^/|\\.test([./]|$)|\\.spec([./]|$)",
          message: "Use a canonical import path; production modules cannot import tests.",
        },
      ],
    },
  ];
}

const browserIOGlobals = [
  "globalThis",
  "window",
  "document",
  "navigator",
  "location",
  "localStorage",
  "sessionStorage",
  "fetch",
  "WebSocket",
  "XMLHttpRequest",
  "Worker",
  "SharedWorker",
  "caches",
  "indexedDB",
];
const pureGlobals = [
  ...browserIOGlobals,
  "Date",
  "performance",
  "crypto",
  "setTimeout",
  "clearTimeout",
  "setInterval",
  "clearInterval",
  "requestAnimationFrame",
  "cancelAnimationFrame",
  "queueMicrotask",
  "console",
  "process",
];

const browser = [
  "./**",
  "../domain/**",
  "../application/**",
  "../shared/**",
  "../generated/**",
  "../components/**",
  "../theme",
  "react",
  "react-dom",
  "react-dom/**",
  "motion/**",
  "lucide-react",
  "clsx",
  "react-markdown",
  "remark-gfm",
];

export const lint = {
  jsPlugins: [{ name: "q15", specifier: "./lint/plugin.ts" }],
  plugins: ["react", "typescript", "jsx-a11y", "import", "promise", "unicorn", "oxc", "vitest"],
  categories: { correctness: "error", suspicious: "error", pedantic: "error", perf: "error" },
  options: { typeAware: true, typeCheck: true, denyWarnings: true },
  rules: {
    "q15/literal-imports": "error",
    // Copy-on-write state transitions must preserve their inputs.
    "oxc/no-map-spread": "off",
    // The automatic JSX runtime does not need a React namespace import.
    "react/react-in-jsx-scope": "off",
    // Count limits do not distinguish a reducer or declarative JSX from complex logic.
    "eslint/max-lines": "off",
    "eslint/max-lines-per-function": "off",
    "import/max-dependencies": "off",
    // Framework event/request types are mutable. Domain inputs use readonly types instead.
    "typescript/prefer-readonly-parameter-types": "off",
    // The type-aware rule understands functions which return an existing Promise.
    "eslint/require-await": "off",
    // Pagination and ordered browser assertions depend on the previous iteration.
    "eslint/no-await-in-loop": "off",
    // Boolean comparisons and event simulators inside fixture tests do not condition assertions.
    // no-conditional-expect and no-conditional-tests still reject skipped expectations/tests.
    "vitest/no-conditional-in-test": "off",
    "typescript/no-confusing-void-expression": ["error", { ignoreArrowShorthand: true }],
    "import/no-unassigned-import": ["error", { allow: ["**/*.css"] }],
    // Some typed APIs require an explicit undefined argument (including mock expectations).
    "unicorn/no-useless-undefined": ["error", { checkArguments: false }],
    "eslint/no-underscore-dangle": ["error", { allow: ["__SHELL__"] }],
    "eslint/eqeqeq": ["error", "always"],
    "eslint/no-console": "error",
    "eslint/no-eval": "error",
    "eslint/no-implied-eval": "error",
    "eslint/no-new-func": "error",
    "eslint/no-param-reassign": ["error", { props: false }],
    "eslint/no-var": "error",
    "eslint/prefer-const": "error",
    "import/no-cycle": "error",
    "import/no-self-import": "error",
    "import/no-duplicates": "error",
    "import/no-mutable-exports": "error",
    "import/no-commonjs": "error",
    "typescript/consistent-type-imports": "error",
    "typescript/consistent-type-exports": "error",
    "typescript/consistent-type-assertions": ["error", { assertionStyle: "never" }],
    "typescript/ban-ts-comment": [
      "error",
      {
        "ts-ignore": true,
        "ts-nocheck": true,
        "ts-expect-error": "allow-with-description",
        minimumDescriptionLength: 10,
      },
    ],
    "typescript/no-explicit-any": "error",
    "typescript/no-non-null-assertion": "error",
    "typescript/no-unsafe-assignment": "error",
    "typescript/no-unsafe-argument": "error",
    "typescript/no-unsafe-call": "error",
    "typescript/no-unsafe-member-access": "error",
    "typescript/no-unsafe-return": "error",
    "typescript/no-unsafe-type-assertion": "error",
    "typescript/no-floating-promises": "error",
    "typescript/no-misused-promises": "error",
    "typescript/no-unnecessary-condition": "error",
    "typescript/no-unnecessary-type-assertion": "error",
    "typescript/only-throw-error": "error",
    "typescript/use-unknown-in-catch-callback-variable": "error",
    "typescript/strict-boolean-expressions": [
      "error",
      { allowString: false, allowNumber: false, allowNullableObject: true },
    ],
    "typescript/restrict-template-expressions": [
      "error",
      {
        allowNumber: true,
        allowBoolean: false,
        allowAny: false,
        allowNullish: false,
        allowRegExp: false,
        allowNever: false,
      },
    ],
    "typescript/restrict-plus-operands": "error",
    "typescript/switch-exhaustiveness-check": "error",
    "react/rules-of-hooks": "error",
    "react/exhaustive-deps": "error",
    "react/no-danger": "error",
    "react/no-array-index-key": "error",
    "react/button-has-type": "error",
    "react/only-export-components": ["error", { allowConstantExport: true }],
    "react/unsupported-syntax": "error",
    "react/rule-suppression": "error",
  },
  ignorePatterns: ["src/generated/**"],
  overrides: [
    { files: ["src/**"], rules: { "eslint/no-restricted-imports": imports(browser) } },
    {
      files: ["src/*.ts", "src/*.tsx"],
      rules: {
        "eslint/no-restricted-imports": imports([
          ...browser.filter((path) => path !== "./**"),
          "./components/**",
          "./application/**",
          "./domain/**",
          "./shared/**",
          "./generated/**",
          "./theme",
          "./*.module.css",
        ]),
      },
    },
    {
      files: ["src/domain/**", "src/shared/**"],
      rules: {
        "eslint/no-restricted-imports": imports([
          "./**",
          "../domain/**",
          "../shared/**",
          "../generated/**",
        ]),
        "eslint/no-restricted-globals": [
          "error",
          ...pureGlobals.map((name) => ({
            name,
            message:
              "Domain and shared logic must be deterministic and platform independent. Pass values through a port.",
          })),
        ],
        "eslint/no-param-reassign": ["error", { props: true }],
        "eslint/no-restricted-properties": [
          "error",
          {
            object: "Math",
            property: "random",
            message: "Pass randomness in from the application boundary.",
          },
        ],
      },
    },
    {
      files: ["src/shared/**"],
      rules: { "eslint/no-restricted-imports": imports(["./**", "../generated/**"]) },
    },
    {
      files: ["src/infrastructure/transport.ts"],
      // This adapter owns and clears single socket callbacks, including injected test sockets.
      rules: { "unicorn/prefer-add-event-listener": "off" },
    },
    {
      files: ["src/application/**"],
      rules: {
        "eslint/no-restricted-globals": [
          "error",
          ...browserIOGlobals.map((name) => ({
            name,
            message: "Access browser I/O through an injected port.",
          })),
        ],
        "eslint/no-restricted-imports": imports([
          "./**",
          "../domain/**",
          "../shared/**",
          "../generated/**",
        ]),
      },
    },
    {
      files: ["src/infrastructure/**"],
      rules: {
        "eslint/no-restricted-imports": imports([
          "./**",
          "../application/**",
          "../domain/**",
          "../shared/**",
          "../generated/**",
        ]),
      },
    },
    {
      files: ["src/infrastructure/mock-transport.ts"],
      rules: {
        "eslint/no-restricted-imports": imports([
          "./**",
          "../application/**",
          "../domain/**",
          "../shared/**",
          "../generated/**",
          "../fixtures/**",
        ]),
      },
    },
    {
      files: ["worker/**"],
      rules: { "eslint/no-restricted-imports": imports(["./**", "../src/shared/**"]) },
    },
    {
      files: ["build/**"],
      rules: {
        "eslint/no-restricted-imports": imports([
          "./**",
          "../src/shared/**",
          "node:crypto",
          "node:fs",
          "node:path",
          "parse5",
          "vite-plus",
        ]),
      },
    },
    {
      files: ["src/main.tsx"],
      rules: {
        "eslint/no-restricted-imports": imports([
          ...browser,
          "./infrastructure/**",
          "@fontsource-variable/recursive/**",
        ]),
      },
    },
    {
      files: ["e2e/**"],
      rules: { "unicorn/prefer-number-coercion": "off" },
    },
    {
      files: [
        "src/**/*.test.ts",
        "src/**/*.test.tsx",
        "build/**/*.test.ts",
        "lint/**/*.test.ts",
        "e2e/**",
      ],
      rules: {
        "eslint/no-restricted-imports": "off",
        "eslint/no-restricted-globals": "off",
        "eslint/no-restricted-properties": "off",
        "react/only-export-components": "off",
      },
    },
  ],
} satisfies NonNullable<UserConfig["lint"]>;
