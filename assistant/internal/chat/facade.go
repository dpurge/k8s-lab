package chat

import "context"

// Get returns a conversation with its run's state.
func (s *Service) Get(ctx context.Context, id string) (View, error) { return s.Store.Get(ctx, id) }

// List returns conversations, most recently active first.
func (s *Service) List(ctx context.Context, userID string) ([]View, error) {
	return s.Store.List(ctx, userID)
}

// Messages returns a conversation's messages, after the given id if above 0.
func (s *Service) Messages(ctx context.Context, conversationID string, after int64) ([]Message, error) {
	return s.Store.Messages(ctx, conversationID, after)
}

// Remove deletes a conversation and its messages.
func (s *Service) Remove(ctx context.Context, id string) error { return s.Store.Remove(ctx, id) }
