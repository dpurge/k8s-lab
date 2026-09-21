---
version: 1
status: approved
updated: 2026-09-21
---

## Problem

This project provides a local, k3d-based Kubernetes cluster for development. It solves the problem of developing cloud-native applications in isolation without requiring external cloud accounts or manual Kubernetes setup. Developers can test against realistic cloud-like infrastructure — PostgreSQL, vector databases, S3-compatible storage, message queues, and semantic search with LLMs — all running locally in a containerized cluster.

## Users

Developers who need a local environment for building and testing cloud-native applications. The project is developed and tested on Linux and macOS; Windows is also supported, via Git for Windows or WSL for the required shell tooling (`jq`, `sed`, `awk`, etc.). Each of the three primary applications (dictionary, phraseforge, knowledge) exposes HTTP-based REST APIs and browser GUIs accessible via Traefik ingress on `localhost:8080`.

## Value proposition

The project eliminates friction in cloud-native development: no need to manually set up k3d, kubectl, or complex infrastructure; no risk to system kubeconfig; cluster isolation is built-in and destroyed on `task stop`. Developers provision only the services they're actively using (database, vector DB, object storage, message queue, etc.) via isolated task commands (`task deploy-postgres`, `task deploy-qdrant`, etc.), keeping development lean and reproducible. This enables rapid local iteration on multi-service applications before integration testing or cloud deployment.

## Non-goals

- **TLS/HTTPS**: HTTP only for development simplicity.
- **Production durability**: Garage and NATS are configured with `replication_factor = 1` and are explicitly not a durability story; this is a dev tool, not a data persistence platform.
- **Production authentication**: Headlamp uses a hardcoded dev-only token with no login gate; Qdrant and NATS have no authentication configured.
