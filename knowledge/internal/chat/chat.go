package chat

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"k8s-lab/shared/llm"
	"knowledge/internal/config"
	"knowledge/internal/qdrant"
	"knowledge/internal/queue"
)

// KindReply is the queue.Service operation kind registered for producing a
// chat reply. It is exported so main.go can call queue.Register(chat.KindReply, chatSvc.HandleReply).
const KindReply = "chat_reply"

type Service struct {
	db    *pgxpool.Pool
	kb    *qdrant.Client
	cfg   config.Config
	llm   *llm.Client
	queue *queue.Service
}

func New(db *pgxpool.Pool, kb *qdrant.Client, cfg config.Config, q *queue.Service) *Service {
	return &Service{db: db, kb: kb, cfg: cfg, queue: q, llm: llm.New(llm.Config{Provider: cfg.ChatProvider, BaseURL: cfg.ChatBaseURL, APIKey: cfg.ChatAPIKey, Model: cfg.ChatModel, NumCtx: cfg.ChatNumCtx})}
}

// replyPayload is the queue.Operation payload for a KindReply operation. It
// carries a full snapshot of everything HandleReply needs, captured by Send
// before the current message is inserted, so the async handler never has
// to re-derive retrieval query or history from a chat_messages table that
// may have changed by the time it runs.
type replyPayload struct {
	ChatID         string    `json:"chat_id"`
	Text           string    `json:"text"`
	RetrievalQuery string    `json:"retrieval_query"`
	Tags           []string  `json:"tags"`
	History        []Message `json:"history"`
}

type Chat struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Tags      []string  `json:"tags"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Messages  []Message `json:"messages,omitempty"`
}
type Message struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	Sources   []Source  `json:"sources,omitempty"`
}
type Source struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Summary string   `json:"summary"`
	Tags    []string `json:"tags"`
	Score   float64  `json:"score"`
	URL     string   `json:"url"`
}

func uuid() (string, error) {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func (s *Service) Create(ctx context.Context, title string, tags []string) (Chat, error) {
	if strings.TrimSpace(title) == "" {
		title = "New chat"
	}
	id, _ := uuid()
	tags = qdrant.NormalizeTags(tags)
	var c Chat
	err := s.db.QueryRow(ctx, "INSERT INTO chats(id,title,tags) VALUES($1,$2,$3) RETURNING id,title,tags,created_at,updated_at", id, title, tags).Scan(&c.ID, &c.Title, &c.Tags, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}
func (s *Service) List(ctx context.Context) ([]Chat, error) {
	rows, err := s.db.Query(ctx, "SELECT id,title,tags,created_at,updated_at FROM chats ORDER BY updated_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Chat{}
	for rows.Next() {
		var c Chat
		if err := rows.Scan(&c.ID, &c.Title, &c.Tags, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) Delete(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, "DELETE FROM chats WHERE id=$1", id)
	return err
}

func (s *Service) Get(ctx context.Context, id string) (Chat, error) {
	var c Chat
	err := s.db.QueryRow(ctx, "SELECT id,title,tags,created_at,updated_at FROM chats WHERE id=$1", id).Scan(&c.ID, &c.Title, &c.Tags, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return c, err
	}
	rows, err := s.db.Query(ctx, "SELECT id,role,content,created_at FROM chat_messages WHERE chat_id=$1 ORDER BY created_at", id)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return c, err
		}
		m.Sources, _ = s.sources(ctx, m.ID)
		c.Messages = append(c.Messages, m)
	}
	return c, rows.Err()
}

// Send persists the user's message and returns immediately — it never
// waits on the LLM. The reply is produced by a queued, interactive-priority
// KindReply operation (see HandleReply) and becomes visible through the
// existing GET /chats/{id} polling once it lands, so sending stays fast
// even while the LLM call itself is slow or the queue is backed up behind
// other work.
func (s *Service) Send(ctx context.Context, chatID, text string) (Message, error) {
	c, err := s.Get(ctx, chatID)
	if err != nil {
		return Message{}, err
	}
	// Retrieval uses the current message plus the immediately preceding user
	// message, not the current message alone: a short follow-up like
	// "answer my last question" embeds to a near-random, weakly-scored
	// match on its own — concatenating the prior question fixed this,
	// confirmed live (0.339 vs 0.342, both weak and tied -> 0.696 vs 0.268,
	// clearly separated, same two candidate documents). This must be
	// computed here, against the snapshot of c.Messages taken before the
	// current message is inserted below — not recomputed later inside
	// HandleReply, where a second message sent in quick succession could
	// already be the newest row by the time this operation is claimed.
	retrievalQuery := text
	if prev := lastUserMessage(c.Messages); prev != "" {
		retrievalQuery = prev + " " + text
	}
	history := recentHistory(c.Messages)

	uid, _ := uuid()
	var msg Message
	err = s.db.QueryRow(ctx, "INSERT INTO chat_messages(id,chat_id,role,content) VALUES($1,$2,'user',$3) RETURNING id,role,content,created_at", uid, chatID, text).Scan(&msg.ID, &msg.Role, &msg.Content, &msg.CreatedAt)
	if err != nil {
		return Message{}, err
	}
	_, _ = s.db.Exec(ctx, "UPDATE chats SET updated_at=now() WHERE id=$1", chatID)

	payload := replyPayload{ChatID: chatID, Text: text, RetrievalQuery: retrievalQuery, Tags: c.Tags, History: history}
	if _, err := s.queue.Enqueue(ctx, KindReply, queue.PriorityInteractive, payload); err != nil {
		return msg, err
	}
	return msg, nil
}

// HandleReply is the queue.HandlerFunc registered for KindReply — it does
// everything Send used to do inline (retrieval, the LLM call, persisting
// the assistant message and its sources) once the queue's single worker
// gets to it. It operates entirely on the payload snapshot Send captured,
// never re-deriving retrieval query or history from the live chat_messages
// table, since those could have shifted by the time this runs.
func (s *Service) HandleReply(ctx context.Context, rawPayload json.RawMessage) (json.RawMessage, error) {
	var p replyPayload
	if err := json.Unmarshal(rawPayload, &p); err != nil {
		return nil, err
	}
	items, err := s.kb.Search(ctx, qdrant.Search{Query: p.RetrievalQuery, Tags: p.Tags, Limit: 5})
	if err != nil {
		return nil, err
	}
	sources := make([]Source, 0, len(items))
	for _, it := range items {
		score := 0.0
		if it.Score != nil {
			score = *it.Score
		}
		sources = append(sources, Source{ID: it.ID, Title: it.Title, Summary: it.Summary, Tags: it.Tags, Score: score, URL: "/?knowledge=" + it.ID})
	}

	start := time.Now()
	answer, err := s.answer(ctx, p.History, p.Text, sources)
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	slog.Info("chat llm call", "model", s.cfg.ChatModel, "duration_ms", time.Since(start).Milliseconds(), "outcome", outcome)
	if err != nil {
		return nil, err
	}

	aid, _ := uuid()
	var msg Message
	err = s.db.QueryRow(ctx, "INSERT INTO chat_messages(id,chat_id,role,content) VALUES($1,$2,'assistant',$3) RETURNING id,role,content,created_at", aid, p.ChatID, answer).Scan(&msg.ID, &msg.Role, &msg.Content, &msg.CreatedAt)
	if err != nil {
		return nil, err
	}
	for _, src := range sources {
		_, _ = s.db.Exec(ctx, "INSERT INTO chat_message_sources(message_id,knowledge_id,title,summary,tags,score) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING", msg.ID, src.ID, src.Title, src.Summary, src.Tags, src.Score)
	}
	_, _ = s.db.Exec(ctx, "UPDATE chats SET updated_at=now() WHERE id=$1", p.ChatID)
	msg.Sources = sources
	return json.Marshal(msg)
}

// lastUserMessage returns the most recent user-role message's content, or
// "" if there isn't one. messages is chronological (oldest first).
func lastUserMessage(messages []Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return messages[i].Content
		}
	}
	return ""
}

// maxHistoryMessages bounds how much prior conversation is replayed to the
// model each turn (3 exchanges), so prompt size doesn't grow unbounded over
// a long chat. Older turns are simply dropped, not summarized.
const maxHistoryMessages = 6

// recentHistory returns the last maxHistoryMessages entries of messages
// (chronological, oldest first), or all of them if there are fewer.
func recentHistory(messages []Message) []Message {
	if len(messages) > maxHistoryMessages {
		return messages[len(messages)-maxHistoryMessages:]
	}
	return messages
}

func (s *Service) sources(ctx context.Context, msgID string) ([]Source, error) {
	rows, err := s.db.Query(ctx, "SELECT knowledge_id,title,summary,tags,score FROM chat_message_sources WHERE message_id=$1 ORDER BY score DESC", msgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Source{}
	for rows.Next() {
		var src Source
		if err := rows.Scan(&src.ID, &src.Title, &src.Summary, &src.Tags, &src.Score); err != nil {
			return nil, err
		}
		src.URL = "/?knowledge=" + src.ID
		out = append(out, src)
	}
	return out, rows.Err()
}

// maxDocumentChars caps each retrieved document's body injected into the
// prompt, sized to fit 5 documents plus the system prompt, question, and
// response comfortably within CHAT_NUM_CTX=8192 (see deployment.yaml).
const maxDocumentChars = 4000

func (s *Service) answer(ctx context.Context, history []Message, question string, sources []Source) (string, error) {
	sys := strings.TrimSpace(s.cfg.ChatPrompt)
	// Plain concatenated document text, deliberately with no id/score/title/
	// tags annotations: llama3-chatqa (NVIDIA ChatQA) is a completion-style
	// QA model trained on flowing context passages, not an enumerated
	// metadata list — confirmed by direct testing against Ollama, where the
	// annotated form made it return an empty or refusal answer even when the
	// relevant document was present. Sources still carry id/score/title/url
	// for appendReferences below; the model itself never needs them.
	bodies := make([]string, 0, len(sources))
	for _, src := range sources {
		body := src.Summary
		// Priming with the document's existing Summary before its full body
		// measurably helped the model locate the right content for an
		// abstractly-phrased question — confirmed live (a vague "physical
		// appearance" question went from a one-sentence non-answer to a
		// substantive, multi-fact one with this priming, same document).
		if it, err := s.kb.Get(ctx, src.ID); err == nil {
			body = "Summary: " + src.Summary + "\n\n" + truncateBody(it.Body, maxDocumentChars)
		}
		bodies = append(bodies, body)
	}
	ctxText := strings.Join(bodies, "\n\n")
	userMsg := ctxText + "\n\nQuestion: " + question
	messages := []llm.Message{{Role: "system", Content: sys}}
	for _, m := range history {
		messages = append(messages, llm.Message{Role: m.Role, Content: m.Content})
	}
	messages = append(messages, llm.Message{Role: "user", Content: userMsg})
	resp, err := s.llm.Chat(ctx, messages, nil)
	if err != nil {
		return "", err
	}
	return appendReferences(resp.Content, sources), nil
}

// truncateBody cuts body at the nearest whitespace at or before maxChars, so
// a word (and, since whitespace is always single-byte ASCII, any preceding
// multi-byte rune) is never split mid-way.
func truncateBody(body string, maxChars int) string {
	if len(body) <= maxChars {
		return body
	}
	cut := maxChars
	if idx := strings.LastIndexAny(body[:maxChars], " \n\t"); idx > 0 {
		cut = idx
	}
	return strings.TrimSpace(body[:cut]) + " [truncated]"
}

func appendReferences(answer string, sources []Source) string {
	answer = strings.TrimSpace(answer)
	if len(sources) == 0 {
		return answer
	}
	var b strings.Builder
	b.WriteString(answer)
	b.WriteString("\n\nKnowledge item links:\n")
	for _, src := range sources {
		b.WriteString(fmt.Sprintf("- [%s](%s) — score %.3f\n", src.Title, src.URL, src.Score))
	}
	return strings.TrimSpace(b.String())
}
