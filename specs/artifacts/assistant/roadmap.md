---
version: 10
status: approved
updated: 2026-10-23
---

## Now


## Next

- `assistant-response-review-loop` — structured response documents rendered from templates, with a reviewer model and a bounded fix loop before sending
- `assistant-telegram` — Telegram as a task source and response channel, user tracking from Telegram identity, a Telegram-shaped mock endpoint and then the real channel (the mock chat page, several conversations per user and `/help` exist), a `/learn` command
- `assistant-chat-port` — Make the assistant the only chat interface: port knowledge's chat retrieval (Qdrant search with tags), citations, history window and session auth gate to the assistant, then remove chat from knowledge
- `assistant-chat-source` — A task source `chat` separate from `telegram`: each request says whether its channel supports A2UI, and the assistant turns an interface into plain text for channels that do not
- `assistant-topic-conversation` — A workflow for a simple conversation about a topic that remembers the user's answers in the run state
- `assistant-memory` — long-term memory on pgvector with bge-m3 embeddings through trpc-agent-go
- `assistant-rag` — markdown knowledge documents with tags, import and export, search as a tool, plus web search
- `assistant-nats` — tasks in and results out over NATS
- `assistant-scheduler` — in-app scheduled tasks for outside-world awareness and user chores
- `assistant-auth` — authentication through a shared external microservice
- `assistant-prod-deploy` — Helm chart in the chart repository, Prometheus scrape job and deployment to the production cluster

## Later

## Out of scope
