# q15 Compose Examples

This directory contains the checked-in Compose-facing config, policy, and secret templates for q15.

- [docker-compose.image-first.yml](/deploy/compose/docker-compose.image-first.yml) is the canonical
  downstream deployment example. It uses published `ghcr.io/q15co/q15-*` images for q15 services,
  requires `Q15_IMAGE_TAG`, and mounts persistent storage for `/workspace`, `/memory`, `/skills`,
  `/nix`, `/var/lib/q15/proxy`, LightRAG data, and Ollama models.
- [docker-compose.yml](/docker-compose.yml) in the repo root is the local-development stack. It
  keeps `build:` enabled and uses a named `q15_workspace` volume for `/workspace`; it is not the
  image-first deployment example for downstream consumers.
- [agent-config.yaml](/deploy/compose/agent-config.yaml),
  [proxy-policy.yaml](/deploy/compose/proxy-policy.yaml), and
  [secrets/\*.example](/deploy/compose/secrets) are generic templates that downstream repos can copy
  or adapt.

For a long-running image-first deployment:

```bash
make compose-secrets-init
Q15_IMAGE_TAG=sha-<short-sha> docker compose -f deploy/compose/docker-compose.image-first.yml up -d
```

Notes:

- Pin `Q15_IMAGE_TAG` to one immutable published tag across `q15-agent`, `q15-exec`, and
  `q15-proxy`. Do not use `main` as the default for long-running stacks.
- `/workspace` is expected to persist long-term for one stack. It may be empty on first startup.
- `/memory` should also persist across updates. `q15-agent` eagerly upgrades stored turn history to
  the latest transcript schema on startup.
- LightRAG is available to `q15-agent` at `http://lightrag:9621` and is exposed to the host only on
  `127.0.0.1:${LIGHTRAG_PORT:-9621}`. The default LLM provider is the Z.AI Coding Plan
  OpenAI-compatible endpoint at `https://api.z.ai/api/coding/paas/v4/`, using `glm-4.7` and the
  local `secrets/zai_api_key` file.
- `glm-4.7` is the default LightRAG LLM because it is strong enough for routine ingestion and avoids
  the higher GLM-5.1/GLM-5-Turbo quota multiplier. Set `LIGHTRAG_LLM_MODEL=glm-5.1` only for complex
  documents or evaluation runs where maximum extraction quality is worth the extra quota burn.
- The default embedding provider is the stack's Ollama service at `http://ollama:11434`, using
  `qwen3-embedding:4b` and `LIGHTRAG_EMBEDDING_DIM=2560`.
- The Compose defaults disable query reranking with `LIGHTRAG_RERANK_BY_DEFAULT=false`, and
  `rag_query` also sends `enable_rerank=false` unless requested. Configure `LIGHTRAG_RERANK_*`
  values and call `rag_query` with `enable_rerank=true` only after adding a Jina, Cohere/vLLM, or
  Aliyun reranker.
- PostgreSQL vector storage defaults to `LIGHTRAG_POSTGRES_VECTOR_INDEX_TYPE=HNSW_HALFVEC`, which
  matches LightRAG's current recommendation for embeddings above 2000 dimensions.
- Chunking is explicit through `LIGHTRAG_CHUNK_SIZE=1200` and `LIGHTRAG_CHUNK_OVERLAP_SIZE=100`.
  LightRAG chunks by token windows, not by document type, so changing these values after ingestion
  requires re-ingesting affected documents.
- `LIGHTRAG_DOCUMENT_LOADING_ENGINE` defaults to `DEFAULT`. Use `DOCLING` only with a LightRAG image
  that has Docling installed; otherwise PDF/DOCX/PPTX/XLSX extraction falls back to built-in
  libraries.
- Ollama is exposed to the host only on `127.0.0.1:${OLLAMA_PORT:-11435}` and uses the NVIDIA CDI
  device `nvidia.com/gpu=all` by default. Override `OLLAMA_GPU_DEVICE` if the host uses a different
  CDI device name.
- On an 8GB production GPU, test `qwen3-embedding:8b` by setting `OLLAMA_EMBEDDING_MODEL` and
  `LIGHTRAG_EMBEDDING_MODEL` to `qwen3-embedding:8b`, and `LIGHTRAG_EMBEDDING_DIM=4096`, before
  first ingestion.
- Initial LightRAG ingestion is manual. Use `rag_ingest` for files under
  `/workspace/library-markdown/` and `/memory/notes/`, then use `rag_status` with returned track IDs
  to watch indexing progress.
- Convert EPUBs and scanned or layout-heavy journal PDFs to Markdown or text before ingestion.
  Current LightRAG accepts `.epub` by extension but treats uploaded EPUBs as UTF-8 text files rather
  than unpacking the EPUB container into chapters.
- In `agent-config.yaml`, `agent.models` order defines the deterministic per-turn fallback
  preference order. q15 filters out models that do not satisfy the currently inferred request
  requirements before any provider call. Current inference is text-first; image-input and
  tool-calling requirement inference are staged for the corresponding canonical request signals. The
  checked-in Compose example uses OpenAI `gpt-5.4` first and Moonshot/Kimi second.
- `agent.cognition.models` is optional and defines the background-cognition fallback order. If it is
  omitted, cognition jobs inherit `agent.models`.
- The checked-in Compose config reads the Telegram allow-list from `Q15_TELEGRAM_ALLOWED_USER_IDS`
  or `Q15_TELEGRAM_ALLOWED_USER_IDS_FILE`, so local user IDs stay out of tracked YAML.
- Update or rollback by changing the pinned tag and redeploying while preserving the persistent
  volumes.
- GHCR runtime images are intended to be publicly pullable without registry auth for normal
  self-hosted consumption. Maintain the package visibility for these GHCR packages as public outside
  this repo.
