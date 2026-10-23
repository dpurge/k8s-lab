package chat

import (
	"context"
	"errors"
	"fmt"

	"assistant/internal/tasks"
)

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

// Remove ends the conversation's run, then deletes the conversation and its
// messages. The task record and its trace stay (cancelled, or as they were if
// the run had finished): a run still stopping ends later and finds no
// conversation to tell. A run that is already finished or gone needs no cancel.
// If the cancel fails for another reason the conversation is not deleted. With
// no Canceller the conversation is just deleted and its run is not touched.
func (s *Service) Remove(ctx context.Context, id string) error {
	v, err := s.Store.Get(ctx, id)
	if err != nil {
		return err
	}
	if v.TaskID != nil && s.Canceller != nil {
		_, _, err := s.Canceller.Cancel(ctx, *v.TaskID)
		if err != nil && !errors.Is(err, tasks.ErrNotCancellable) && !errors.Is(err, tasks.ErrNotFound) {
			return fmt.Errorf("cancel the conversation's run before removing it: %w", err)
		}
	}
	return s.Store.Remove(ctx, id)
}
