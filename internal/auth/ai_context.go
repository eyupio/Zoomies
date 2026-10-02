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

// SetContextConnectionRepositories is a person's explicit source consent,
// independent of the role already approved for fleet tools. An agent must not
// be able to widen its own access by using the same token it wants to widen.
func (s *Service) SetContextConnectionRepositories(ctx context.Context, actor *Identity, grantID string, repositories []string) error {
	if actor == nil || actor.Kind != KindUser || actor.UserID == "" {
		return fmt.Errorf("%w: sign in as the connection's owner to choose its source repositories", ErrInvalidInput)
	}
	if err := s.store.ReplaceAIContextConnectionAccess(ctx, grantID, actor.UserID, repositories); err != nil {
		return err
	}
	s.audit.Act(ctx, actor, "mcp_connection.context", "mcp_connection", grantID, map[string]any{"repositories": len(repositories)})
	return nil
}
