package cloud

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// ---- SQLiteStore: projects ----

func (s *SQLiteStore) CreateProject(req CreateProjectRequest) (*Project, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	now := time.Now().UTC()
	id := newID("proj")
	if _, err := s.db.Exec(
		`INSERT INTO projects (id, name, description, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		id, req.Name, req.Description, sqliteTime(now), sqliteTime(now),
	); err != nil {
		return nil, fmt.Errorf("insert project: %w", err)
	}
	return s.getProject(id), nil
}

func (s *SQLiteStore) getProject(id string) *Project {
	var p Project
	var createdAt, updatedAt string
	err := s.db.QueryRow(
		`SELECT id, name, description, created_at, updated_at
		 FROM projects WHERE id = ?`, id,
	).Scan(&p.ID, &p.Name, &p.Description, &createdAt, &updatedAt)
	if err != nil {
		return nil
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &p
}

func (s *SQLiteStore) DeleteProject(id string) error {
	res, err := s.db.Exec(`DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("project %q not found", id)
	}
	return nil
}

func (s *SQLiteStore) listProjects() []*Project {
	rows, err := s.db.Query(
		`SELECT id, name, description, created_at, updated_at
		 FROM projects ORDER BY created_at`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*Project
	for rows.Next() {
		var p Project
		var createdAt, updatedAt string
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &createdAt, &updatedAt); err != nil {
			continue
		}
		p.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		out = append(out, &p)
	}
	return out
}

// ---- SQLiteStore: repos ----

func (s *SQLiteStore) CreateRepo(req CreateRepoRequest) (*Repo, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if req.ProjectID != "" && s.getProject(req.ProjectID) == nil {
		return nil, fmt.Errorf("project %q not found", req.ProjectID)
	}
	platform := req.Platform
	if platform == "" {
		platform = "github"
	}
	now := time.Now().UTC()
	id := newID("repo")
	if _, err := s.db.Exec(
		`INSERT INTO repos (id, name, platform, project_id, base_branch, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, req.Name, platform, nullableString(req.ProjectID), req.BaseBranch,
		sqliteTime(now), sqliteTime(now),
	); err != nil {
		return nil, fmt.Errorf("insert repo: %w", err)
	}
	return s.getRepo(id), nil
}

func (s *SQLiteStore) getRepo(id string) *Repo {
	var r Repo
	var projectID sql.NullString
	var createdAt, updatedAt string
	err := s.db.QueryRow(
		`SELECT id, name, platform, project_id, base_branch, created_at, updated_at
		 FROM repos WHERE id = ?`, id,
	).Scan(&r.ID, &r.Name, &r.Platform, &projectID, &r.BaseBranch, &createdAt, &updatedAt)
	if err != nil {
		return nil
	}
	if projectID.Valid {
		r.ProjectID = projectID.String
	}
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &r
}

func (s *SQLiteStore) UpdateRepo(id string, req UpdateRepoRequest) (*Repo, error) {
	r := s.getRepo(id)
	if r == nil {
		return nil, fmt.Errorf("repo %q not found", id)
	}
	if req.ProjectID != nil {
		if *req.ProjectID != "" && s.getProject(*req.ProjectID) == nil {
			return nil, fmt.Errorf("project %q not found", *req.ProjectID)
		}
		r.ProjectID = *req.ProjectID
	}
	if req.BaseBranch != nil {
		r.BaseBranch = *req.BaseBranch
	}
	r.UpdatedAt = time.Now().UTC()
	if _, err := s.db.Exec(
		`UPDATE repos SET project_id = ?, base_branch = ?, updated_at = ? WHERE id = ?`,
		nullableString(r.ProjectID), r.BaseBranch, sqliteTime(r.UpdatedAt), id,
	); err != nil {
		return nil, fmt.Errorf("update repo: %w", err)
	}
	return r, nil
}

func (s *SQLiteStore) DeleteRepo(id string) error {
	res, err := s.db.Exec(`DELETE FROM repos WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("repo %q not found", id)
	}
	return nil
}

func (s *SQLiteStore) listRepos() []*Repo {
	rows, err := s.db.Query(
		`SELECT id, name, platform, project_id, base_branch, created_at, updated_at
		 FROM repos ORDER BY created_at`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*Repo
	for rows.Next() {
		var r Repo
		var projectID sql.NullString
		var createdAt, updatedAt string
		if err := rows.Scan(&r.ID, &r.Name, &r.Platform, &projectID, &r.BaseBranch,
			&createdAt, &updatedAt); err != nil {
			continue
		}
		if projectID.Valid {
			r.ProjectID = projectID.String
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		out = append(out, &r)
	}
	return out
}

// ---- SQLiteStore: bindings ----

func (s *SQLiteStore) CreateBinding(req CreateBindingRequest) (*Binding, error) {
	if req.MachineID == "" {
		return nil, fmt.Errorf("machine_id is required")
	}
	if req.RepoID == "" && req.ProjectID == "" {
		return nil, fmt.Errorf("one of repo_id or project_id is required")
	}
	// Validate FKs explicitly so we return a friendly error rather than a
	// sqlite "FOREIGN KEY constraint failed".
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM machines WHERE id = ?`, req.MachineID).Scan(&count); err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, fmt.Errorf("machine %q not found", req.MachineID)
	}
	if req.RepoID != "" && s.getRepo(req.RepoID) == nil {
		return nil, fmt.Errorf("repo %q not found", req.RepoID)
	}
	if req.ProjectID != "" && s.getProject(req.ProjectID) == nil {
		return nil, fmt.Errorf("project %q not found", req.ProjectID)
	}

	now := time.Now().UTC()
	id := newID("binding")
	if _, err := s.db.Exec(
		`INSERT INTO bindings (id, machine_id, repo_id, project_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, req.MachineID, nullableString(req.RepoID), nullableString(req.ProjectID),
		sqliteTime(now), sqliteTime(now),
	); err != nil {
		return nil, fmt.Errorf("insert binding: %w", err)
	}
	return s.getBinding(id), nil
}

func (s *SQLiteStore) getBinding(id string) *Binding {
	var b Binding
	var repoID, projectID sql.NullString
	var createdAt, updatedAt string
	err := s.db.QueryRow(
		`SELECT id, machine_id, repo_id, project_id, created_at, updated_at
		 FROM bindings WHERE id = ?`, id,
	).Scan(&b.ID, &b.MachineID, &repoID, &projectID, &createdAt, &updatedAt)
	if err != nil {
		return nil
	}
	if repoID.Valid {
		b.RepoID = repoID.String
	}
	if projectID.Valid {
		b.ProjectID = projectID.String
	}
	b.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	b.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &b
}

func (s *SQLiteStore) UpdateBinding(id string, req UpdateBindingRequest) (*Binding, error) {
	b := s.getBinding(id)
	if b == nil {
		return nil, fmt.Errorf("binding %q not found", id)
	}
	if req.MachineID != "" {
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM machines WHERE id = ?`, req.MachineID).Scan(&count); err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, fmt.Errorf("machine %q not found", req.MachineID)
		}
		b.MachineID = req.MachineID
	}
	if req.RepoID != "" {
		if s.getRepo(req.RepoID) == nil {
			return nil, fmt.Errorf("repo %q not found", req.RepoID)
		}
		b.RepoID = req.RepoID
	}
	if req.ProjectID != "" {
		if s.getProject(req.ProjectID) == nil {
			return nil, fmt.Errorf("project %q not found", req.ProjectID)
		}
		b.ProjectID = req.ProjectID
	}
	b.UpdatedAt = time.Now().UTC()
	if _, err := s.db.Exec(
		`UPDATE bindings SET machine_id = ?, repo_id = ?, project_id = ?, updated_at = ?
		 WHERE id = ?`,
		b.MachineID, nullableString(b.RepoID), nullableString(b.ProjectID),
		sqliteTime(b.UpdatedAt), id,
	); err != nil {
		return nil, fmt.Errorf("update binding: %w", err)
	}
	return b, nil
}

func (s *SQLiteStore) DeleteBinding(id string) error {
	res, err := s.db.Exec(`DELETE FROM bindings WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("binding %q not found", id)
	}
	return nil
}

func (s *SQLiteStore) listBindings() []*Binding {
	rows, err := s.db.Query(
		`SELECT id, machine_id, repo_id, project_id, created_at, updated_at
		 FROM bindings ORDER BY created_at`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*Binding
	for rows.Next() {
		var b Binding
		var repoID, projectID sql.NullString
		var createdAt, updatedAt string
		if err := rows.Scan(&b.ID, &b.MachineID, &repoID, &projectID, &createdAt, &updatedAt); err != nil {
			continue
		}
		if repoID.Valid {
			b.RepoID = repoID.String
		}
		if projectID.Valid {
			b.ProjectID = projectID.String
		}
		b.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		b.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		out = append(out, &b)
	}
	return out
}

// ---- SQLiteStore: vcs_connections ----

// RegisterConnection upserts the per-repo App installation. The github_app
// sub-struct is serialised as JSON because its fields are opaque references.
func (s *SQLiteStore) RegisterConnection(conn VCSConnection) (*VCSConnection, error) {
	if conn.Repo == "" {
		return nil, fmt.Errorf("connection requires a non-empty repo")
	}
	if conn.Platform == "" {
		conn.Platform = "github"
	}

	var existingID string
	err := s.db.QueryRow(`SELECT id FROM vcs_connections WHERE repo = ?`, conn.Repo).Scan(&existingID)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if existingID != "" {
		conn.ID = existingID
	}
	if conn.ID == "" {
		conn.ID = newID("conn")
	}

	appJSON := ""
	if conn.GitHubApp != nil {
		b, _ := json.Marshal(conn.GitHubApp)
		appJSON = string(b)
	}

	if existingID == "" {
		_, err = s.db.Exec(
			`INSERT INTO vcs_connections (id, repo, platform, bound_machine_id, github_app_json)
			 VALUES (?, ?, ?, ?, ?)`,
			conn.ID, conn.Repo, conn.Platform, conn.BoundMachineID, appJSON,
		)
	} else {
		_, err = s.db.Exec(
			`UPDATE vcs_connections SET platform = ?, bound_machine_id = ?, github_app_json = ?
			 WHERE id = ?`,
			conn.Platform, conn.BoundMachineID, appJSON, conn.ID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("upsert connection: %w", err)
	}
	return s.GetConnectionByRepo(conn.Repo), nil
}

// GetConnectionByRepo looks up the connection for an "owner/repo" slug.
// Returns nil when no row matches.
func (s *SQLiteStore) GetConnectionByRepo(repo string) *VCSConnection {
	var c VCSConnection
	var appJSON string
	err := s.db.QueryRow(
		`SELECT id, repo, platform, bound_machine_id, github_app_json
		 FROM vcs_connections WHERE repo = ?`, repo,
	).Scan(&c.ID, &c.Repo, &c.Platform, &c.BoundMachineID, &appJSON)
	if err != nil {
		return nil
	}
	if appJSON != "" {
		var app GitHubAppInstallation
		if json.Unmarshal([]byte(appJSON), &app) == nil {
			c.GitHubApp = &app
		}
	}
	return &c
}

// ---- SQLiteStore: cloud summary + list views ----

func (s *SQLiteStore) Summary() CloudConfigSummary {
	projects := s.listProjects()
	repos := s.listRepos()
	bindings := s.listBindings()
	machines := s.ListMachines()
	jobs := s.ListJobs()
	runs := s.ListRuns()
	return CloudConfigSummary{
		Projects: projects,
		Repos:    repos,
		Machines: machines,
		Bindings: bindings,
		Counts: ConfigCounts{
			Projects: len(projects),
			Repos:    len(repos),
			Machines: len(machines),
			Bindings: len(bindings),
			Jobs:     len(jobs),
			Runs:     len(runs),
		},
	}
}

func (s *SQLiteStore) ListMachines() []*Machine {
	rows, err := s.db.Query(
		`SELECT id, hostname, display_name, version, capabilities_json, last_seen_at
		 FROM machines ORDER BY created_at IS NULL, hostname`)
	if err != nil {
		// Some test rows may have no created_at; ignore order errors.
		rows, err = s.db.Query(
			`SELECT id, hostname, display_name, version, capabilities_json, last_seen_at FROM machines`)
		if err != nil {
			return nil
		}
	}
	defer rows.Close()
	var out []*Machine
	for rows.Next() {
		var m Machine
		var capsJSON, lastSeen string
		if err := rows.Scan(&m.ID, &m.Hostname, &m.DisplayName, &m.Version, &capsJSON, &lastSeen); err != nil {
			continue
		}
		if capsJSON != "" && capsJSON != "[]" {
			_ = json.Unmarshal([]byte(capsJSON), &m.Capabilities)
		}
		m.LastSeenAt, _ = time.Parse(time.RFC3339Nano, lastSeen)
		out = append(out, &m)
	}
	return out
}

func (s *SQLiteStore) ListJobs() []*JobRecord {
	rows, err := s.db.Query(
		`SELECT id, spec_json, status, bound_machine_id, lease_worker_id,
		        lease_expires_at, attempt_count, created_at, updated_at
		 FROM jobs ORDER BY created_at DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*JobRecord
	for rows.Next() {
		var specJSON, status, boundMachineID, leaseWorkerID string
		var leaseExpiresAt sql.NullString
		var attemptCount int
		var createdAt, updatedAt string
		var id string
		if err := rows.Scan(&id, &specJSON, &status, &boundMachineID, &leaseWorkerID,
			&leaseExpiresAt, &attemptCount, &createdAt, &updatedAt); err != nil {
			continue
		}
		var spec JobSpec
		if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
			continue
		}
		rec := &JobRecord{
			Spec:           spec,
			Status:         status,
			BoundMachineID: boundMachineID,
			LeaseWorkerID:  leaseWorkerID,
			AttemptCount:   attemptCount,
		}
		rec.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		rec.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		if leaseExpiresAt.Valid {
			rec.LeaseExpiresAt, _ = time.Parse(time.RFC3339Nano, leaseExpiresAt.String)
		}
		out = append(out, rec)
	}
	return out
}

func (s *SQLiteStore) ListRuns() []*RunRecord {
	rows, err := s.db.Query(
		`SELECT id, job_id, status, outcome, summary, error, started_at, ended_at
		 FROM runs ORDER BY started_at DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*RunRecord
	for rows.Next() {
		var rec RunRecord
		var endedAt sql.NullString
		var startedAt string
		if err := rows.Scan(&rec.ID, &rec.JobID, &rec.Status, &rec.Outcome,
			&rec.Summary, &rec.Error, &startedAt, &endedAt); err != nil {
			continue
		}
		rec.StartedAt, _ = time.Parse(time.RFC3339Nano, startedAt)
		if endedAt.Valid {
			t, _ := time.Parse(time.RFC3339Nano, endedAt.String)
			rec.EndedAt = &t
		}
		out = append(out, &rec)
	}
	return out
}

// nullableString returns sql.NullString{String: s, Valid: s != ""} encoded as
// the interface{} sqlite driver wants — empty string becomes SQL NULL, so
// foreign-key columns that allow NULL don't fail on the FK constraint.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
