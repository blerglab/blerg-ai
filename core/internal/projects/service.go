package projects

import (
	"context"
	"errors"
	"fmt"

	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/jackc/pgx/v5"
)

// RoleCaps maps a project role to its capability set (§4.1 RBAC).
var RoleCaps = map[string][]string{
	"owner":      {"card.read", "card.write", "column.write", "session.start", "secrets.read", "membership.write"},
	"maintainer": {"card.read", "card.write", "column.write", "session.start"},
	"member":     {"card.read", "card.write"},
	"viewer":     {"card.read"},
}

type Project struct {
	ID, Name, SecretScope string
}

type Service struct{ st db.Store }

func NewService(st db.Store) *Service { return &Service{st: st} }

func (s *Service) Create(ctx context.Context, name string) (Project, error) {
	var p Project
	err := s.st.Pool().QueryRow(ctx,
		`INSERT INTO projects(name) VALUES ($1) RETURNING id::text, name, secret_scope`, name).
		Scan(&p.ID, &p.Name, &p.SecretScope)
	return p, err
}

func (s *Service) AddMember(ctx context.Context, projectID, sub, role string) error {
	_, err := s.st.Pool().Exec(ctx,
		`INSERT INTO project_members(project_id, principal_sub, role) VALUES ($1,$2,$3)
		 ON CONFLICT (project_id, principal_sub) DO UPDATE SET role = EXCLUDED.role`,
		projectID, sub, role)
	return err
}

func (s *Service) Capabilities(ctx context.Context, projectID, sub string) ([]string, error) {
	var role string
	err := s.st.Pool().QueryRow(ctx,
		`SELECT role FROM project_members WHERE project_id=$1 AND principal_sub=$2`, projectID, sub).
		Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // non-member → no capabilities (not an error)
	}
	if err != nil {
		// A failed lookup is not a verdict on membership: report it, so an outage is not
		// mistaken for "this principal has no access".
		return nil, fmt.Errorf("projects: capabilities: %w", err)
	}
	return RoleCaps[role], nil
}
