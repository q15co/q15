// Native no-restricted-imports checks literal imports. Reject computed imports so
// an expression cannot hide a dependency from the layer allowlists.
interface ImportNode {
  source: { type: string; value?: unknown } | null;
}
interface RuleContext {
  report: (diagnostic: { node: ImportNode; message: string }) => void;
}

export default {
  meta: { name: "q15" },
  rules: {
    "literal-imports": {
      meta: { type: "problem", schema: [] },
      create(context: RuleContext) {
        return {
          ImportExpression(node: ImportNode) {
            if (node.source?.type !== "Literal") {
              context.report({
                node,
                message: "Use a literal import so Oxlint can enforce module boundaries.",
              });
            }
          },
          "ImportDeclaration, ExportNamedDeclaration, ExportAllDeclaration, ImportExpression"(
            node: ImportNode,
          ) {
            const path = node.source?.value;
            if (
              typeof path === "string" &&
              /\.\/\.\.\/|\/\.\.\/|\/\.\/|\\|[?#]|^\/|\.test([./]|$)|\.spec([./]|$)/u.test(path)
            ) {
              context.report({
                node,
                message: "Use a canonical import path; production modules cannot import tests.",
              });
            }
          },
        };
      },
    },
  },
};
