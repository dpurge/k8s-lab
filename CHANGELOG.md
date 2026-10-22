# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Added

- `task llm-status`, `llm-enable` and `llm-disable` cover knowledge as well (its chat, title, summary, translate and embeddings), and `llm-status` prints the id `lan-embed` when the LAN Ollama also has `bge-m3`, which adds a second copy of the embedding model in the same space.
- `task llm-status`, `llm-enable`, `llm-disable` and `llm-refresh-vertex-token` add and remove a LAN Ollama, OpenRouter and Vertex AI for the assistant and phraseforge in the dev cluster; settings come from the git-ignored `.env` (copy `.env.example`), read by the new `scripts/` shell scripts, which `task test-llm-scripts` tests.

### Changed

- `task llm-enable` and `task llm-disable` restart a deployed app only when its models file or key changed.

### Removed

- `task refresh-assistant-vertex-token` and the variable `ASSISTANT_VERTEX_ACCOUNT`, replaced by `task llm-refresh-vertex-token` and `LLM_VERTEX_ACCOUNT` in `.env` (no fallback to the old name).
