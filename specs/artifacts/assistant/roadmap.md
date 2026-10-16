---
version: 3
status: approved
updated: 2026-10-16
---

## Now

## Next

- `assistant-provider-openai` — OpenAI-compatible provider (OpenRouter, OpenAI) with token usage and structured output, behind the router's provider interface
- `assistant-provider-anthropic` — Anthropic provider behind the router's provider interface
- `assistant-provider-vertex` — Vertex AI provider behind the router's provider interface
- `assistant-response-review-loop` — structured response documents rendered from templates, with a reviewer model and a bounded fix loop before sending
- `assistant-telegram` — Telegram as a task source and response channel, user tracking from Telegram identity, a Telegram-shaped mock endpoint and then the real channel (the mock chat page, several conversations per user and `/help` exist), a `/learn` command
- `assistant-memory` — long-term memory on pgvector with bge-m3 embeddings through trpc-agent-go
- `assistant-rag` — markdown knowledge documents with tags, import and export, search as a tool, plus web search
- `assistant-nats` — tasks in and results out over NATS
- `assistant-scheduler` — in-app scheduled tasks for outside-world awareness and user chores
- `assistant-auth` — authentication through a shared external microservice
- `assistant-prod-deploy` — Helm chart in jdp-helm, Prometheus scrape job and deployment to jdpct101

## Later

## Out of scope
