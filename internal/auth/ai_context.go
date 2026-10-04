package auth

import (
	"context"
	"fmt"
)

// ContextAccess adds the source-specific gate to the ordinary action policy.
// Admins still need membership to read source; configuring the feature is a
// separate authority. An unowned automation token has nobody's source access.
func (s *Service) ContextAccess(ctx context.Context, id *Identity, repositoryID string) (bool, error) {
	if !Allowed(id, ActionContextRead) || id.UserID == "" {
		return false, nil
	}
	switch id.Kind {
	case KindUser, KindToken:
		return s.store.AIContextUserAccess(ctx, repositoryID, id.UserID)
	case KindConnection:
		return s.store.AIContextConnectionAccess(ctx, repositoryID, id.ID, id.UserID)
	default:
		return false, nil
	}
}

// ContextPublishAccess is the gate on writing a note about a repository. A
// person or their own token needs only membership: they are the author. A
// connection needs its owner's separate consent to publish to this
// repository, because reading consent must never become writing consent.
func (s *Service) ContextPublishAccess(ctx context.Context, id *Identity, repositoryID string) (bool, error) {
	if !Allowed(id, ActionContextPublish) || id.UserID == "" {
		return false, nil
	}
	switch id.Kind {
	case KindUser, KindToken:
		return s.store.AIContextUserAccess(ctx, repositoryID, id.UserID)
	case KindConnection:
		return s.store.AIContextConnectionPublishAccess(ctx, repositoryID, id.ID, id.UserID)
	default:
		return false, nil
	}
}

// SetContextConnectionRepositories is a person's explicit source consent,
// independent of the role already approved for fleet tools. An agent must not
// be able to widen its own access by using the same token it wants to widen.
//
// publish is the subset the connection may also write notes to; nil keeps the
// publish consent already given (see store.ReplaceAIContextConnectionAccess).
func (s *Service) SetContextConnectionRepositories(ctx context.Context, actor *Identity, grantID string, repositories, publish []string) error {
	if actor == nil || actor.Kind != KindUser || actor.UserID == "" {
		return fmt.Errorf("%w: sign in as the connection's owner to choose its source repositories", ErrInvalidInput)
	}
	readable := map[string]bool{}
	for _, id := range repositories {
		readable[id] = true
	}
	for _, id := range publish {
		if !readable[id] {
			return fmt.Errorf("%w: a connection can only publish notes to repositories it may also read; choose %s for reading too", ErrInvalidInput, id)
		}
	}
	if err := s.store.ReplaceAIContextConnectionAccess(ctx, grantID, actor.UserID, repositories, publish); err != nil {
		return err
	}
	s.audit.Act(ctx, actor, "mcp_connection.context", "mcp_connection", grantID, map[string]any{"repositories": len(repositories), "publish": publish})
	return nil
}
