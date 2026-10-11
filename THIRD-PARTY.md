# Third-party licences

The software q15 ships with, and the licence each part is under. Generated on 2026-10-11 from
`go list -deps` across the four binaries and a walk of the browser bundle's runtime closure.
Versions are those the repository pins at `origin/main` = `ef57904`.

## Go modules linked into the binaries

### Apache-2.0 modules

- `cloud.google.com/go/auth` v0.18.2
- `cloud.google.com/go/compute/metadata` v0.9.0
- `cloud.google.com/go` v0.123.0
- `github.com/go-logr/logr` v1.4.3
- `github.com/go-logr/stdr` v1.2.2
- `github.com/google/go-tpm` v0.9.8
- `github.com/google/s2a-go` v0.1.9
- `github.com/googleapis/enterprise-certificate-proxy` v0.3.11
- `github.com/klauspost/compress` v1.19.1
- `github.com/openai/openai-go/v3` v3.22.0
- `github.com/qdrant/go-client` v1.17.1
- `github.com/wk8/go-ordered-map/v2` v2.1.8
- `go.opentelemetry.io/auto/sdk` v1.2.1
- `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` v0.65.0
- `go.opentelemetry.io/otel/metric` v1.44.0
- `go.opentelemetry.io/otel/trace` v1.44.0
- `go.opentelemetry.io/otel` v1.44.0
- `google.golang.org/genai` v1.56.0
- `google.golang.org/genproto/googleapis/rpc` v0.0.0-20260526163538-3dc84a4a5aaa
- `google.golang.org/grpc` v1.83.2
- `gopkg.in/yaml.v2` v2.3.0

### BSD-2-Clause modules

- `github.com/andybalholm/cascadia` v1.3.3
- `github.com/bahlo/generic-list-go` v0.2.0
- `github.com/elazarl/goproxy` v1.8.2
- `github.com/go-webauthn/webauthn` v0.18.2
- `github.com/go-webauthn/x` v0.3.1
- `github.com/google/go-cmp` v0.7.0
- `github.com/google/uuid` v1.6.0
- `github.com/googleapis/gax-go/v2` v2.17.0
- `github.com/gorilla/websocket` v1.5.3
- `golang.org/x/crypto` v0.57.0
- `golang.org/x/net` v0.60.0
- `golang.org/x/sys` v0.48.0
- `golang.org/x/text` v0.42.0
- `google.golang.org/protobuf` v1.36.11

### ISC modules

- `github.com/coder/websocket` v1.8.14

### MIT modules

- `codeberg.org/readeck/go-readability/v2` v2.1.1
- `github.com/JohannesKaufmann/dom` v0.2.0
- `github.com/JohannesKaufmann/html-to-markdown/v2` v2.5.0
- `github.com/andybalholm/brotli` v1.2.2
- `github.com/araddon/dateparse` v0.0.0-20210429162001-6b43995a97de
- `github.com/buger/jsonparser` v1.2.0
- `github.com/cespare/xxhash/v2` v2.3.0
- `github.com/felixge/httpsnoop` v1.0.4
- `github.com/fxamacker/cbor/v2` v2.9.4
- `github.com/go-shiori/dom` v0.0.0-20230515143342-73569d674e1c
- `github.com/go-viper/mapstructure/v2` v2.5.0
- `github.com/gogs/chardet` v0.0.0-20211120154057-b7413eaefb8f
- `github.com/golang-jwt/jwt/v5` v5.3.1
- `github.com/grbit/go-json` v0.11.0
- `github.com/mailru/easyjson` v0.7.7
- `github.com/mymmrac/telego` v1.12.1
- `github.com/netresearch/go-cron` v0.13.4
- `github.com/ollama/ollama` v0.22.1
- `github.com/philhofer/fwd` v1.2.0
- `github.com/tidwall/gjson` v1.18.0
- `github.com/tidwall/match` v1.2.0
- `github.com/tidwall/pretty` v1.2.1
- `github.com/tidwall/sjson` v1.2.5
- `github.com/tinylib/msgp` v1.6.4
- `github.com/valyala/bytebufferpool` v1.0.0
- `github.com/valyala/fasthttp` v1.73.0
- `github.com/valyala/fastjson` v1.6.10
- `github.com/x448/float16` v0.8.4
- `github.com/yuin/goldmark-meta` v1.1.0
- `github.com/yuin/goldmark` v1.7.16
- `go.yaml.in/yaml/v3` v3.0.5
- `gopkg.in/yaml.v3` v3.0.1

## Browser bundle (runtime dependencies)

### 0BSD packages

- `tslib` 2.8.1

### ISC packages

- `@ungap/structured-clone` 1.4.0
- `lucide-react` 1.49.0

### MIT packages

- `bail` 2.0.2
- `ccount` 2.0.1
- `character-entities-html4` 2.1.0
- `character-entities-legacy` 3.0.0
- `character-entities` 2.0.2
- `character-reference-invalid` 2.0.1
- `clsx` 2.1.1
- `comma-separated-tokens` 2.0.3
- `debug` 4.4.3
- `decode-named-character-reference` 1.3.0
- `dequal` 2.0.3
- `devlop` 1.1.0
- `escape-string-regexp` 5.0.0
- `estree-util-is-identifier-name` 3.0.0
- `extend` 3.0.2
- `framer-motion` 13.5.0
- `hast-util-to-jsx-runtime` 2.3.6
- `hast-util-whitespace` 3.0.0
- `html-url-attributes` 3.0.1
- `inline-style-parser` 0.2.7
- `is-alphabetical` 2.0.1
- `is-alphanumerical` 2.0.1
- `is-decimal` 2.0.1
- `is-hexadecimal` 2.0.1
- `is-plain-obj` 4.1.0
- `longest-streak` 3.1.0
- `markdown-table` 3.0.4
- `mdast-util-find-and-replace` 3.0.2
- `mdast-util-from-markdown` 2.0.3
- `mdast-util-gfm-autolink-literal` 2.0.1
- `mdast-util-gfm-footnote` 2.1.0
- `mdast-util-gfm-strikethrough` 2.0.1
- `mdast-util-gfm-table` 2.0.0
- `mdast-util-gfm-task-list-item` 2.0.0
- `mdast-util-gfm` 3.1.0
- `mdast-util-mdx-expression` 2.0.1
- `mdast-util-mdx-jsx` 3.2.0
- `mdast-util-mdxjs-esm` 2.0.1
- `mdast-util-phrasing` 4.1.0
- `mdast-util-to-hast` 13.2.1
- `mdast-util-to-markdown` 2.1.3
- `mdast-util-to-string` 4.0.0
- `micromark-core-commonmark` 2.0.4
- `micromark-extension-gfm-autolink-literal` 2.1.0
- `micromark-extension-gfm-footnote` 2.1.0
- `micromark-extension-gfm-strikethrough` 2.1.0
- `micromark-extension-gfm-table` 2.1.2
- `micromark-extension-gfm-tagfilter` 2.0.0
- `micromark-extension-gfm-task-list-item` 2.1.0
- `micromark-extension-gfm` 3.0.0
- `micromark-factory-destination` 2.0.1
- `micromark-factory-label` 2.0.1
- `micromark-factory-space` 2.1.0
- `micromark-factory-title` 2.0.1
- `micromark-factory-whitespace` 2.0.1
- `micromark-util-character` 2.1.1
- `micromark-util-chunked` 2.0.1
- `micromark-util-classify-character` 2.0.1
- `micromark-util-combine-extensions` 2.0.1
- `micromark-util-decode-numeric-character-reference` 2.0.2
- `micromark-util-decode-string` 2.0.1
- `micromark-util-edit-map` 1.0.0
- `micromark-util-encode` 2.0.1
- `micromark-util-html-tag-name` 2.0.1
- `micromark-util-normalize-identifier` 2.0.1
- `micromark-util-resolve-all` 2.0.1
- `micromark-util-sanitize-uri` 2.0.1
- `micromark-util-subtokenize` 2.1.0
- `micromark-util-symbol` 2.0.1
- `micromark-util-types` 2.0.3
- `micromark` 4.0.3
- `motion-dom` 13.5.0
- `motion-utils` 13.5.0
- `motion` 13.5.0
- `ms` 2.1.3
- `parse-entities` 4.0.2
- `property-information` 7.2.0
- `react-dom` 19.3.0
- `react-markdown` 10.1.0
- `react` 19.3.0
- `remark-gfm` 4.0.1
- `remark-parse` 11.0.0
- `remark-rehype` 11.1.2
- `remark-stringify` 11.0.0
- `scheduler` 0.28.0
- `space-separated-tokens` 2.0.2
- `stringify-entities` 4.0.4
- `style-to-js` 1.1.21
- `style-to-object` 1.0.14
- `trim-lines` 3.0.1
- `trough` 2.2.0
- `unified` 11.0.5
- `unist-util-is` 6.0.1
- `unist-util-position` 5.0.0
- `unist-util-stringify-position` 4.0.0
- `unist-util-visit-parents` 6.0.2
- `unist-util-visit` 5.1.0
- `vfile-message` 4.0.3
- `vfile` 6.0.3
- `zwitch` 2.0.4

### OFL-1.1 packages

- `@fontsource-variable/recursive` 5.3.0

## Type-only packages

The install also carries these for the type checker. They are not bundled into the browser build;
they are MIT, like everything around them.

- `@types/debug` 4.1.13
- `@types/estree-jsx` 1.0.5
- `@types/estree` 1.0.9
- `@types/hast` 3.0.5
- `@types/mdast` 4.0.4
- `@types/ms` 2.1.0
- `@types/unist` 3.0.3

## Container base images

| Image                                           | Used for                         | Licence                           |
| ----------------------------------------------- | -------------------------------- | --------------------------------- |
| `debian:bookworm-slim`                          | the agent, proxy and web images  | Debian's own mix of free licences |
| `gcr.io/distroless/static-debian12:nonroot`     | the web image                    | Apache-2.0                        |
| `nixos/nix:latest`                              | the executor image               | LGPL-2.1 (Nix itself)             |
| `golang:1.26-bookworm`, `node:24-bookworm-slim` | build stages only, not published | BSD-3-Clause, MIT and others      |

None of these link into a q15 binary; they are separate programs in an image.
