package cloud

// GitHubAppInstallation holds the metadata needed to authenticate as a GitHub
// App installation and to verify incoming webhook deliveries. Secrets are
// stored by reference (SecretRef) rather than inline so that a future
// database-backed store can swap the resolution mechanism without changing
// this struct.
type GitHubAppInstallation struct {
	// AppID is the numeric GitHub App identifier.
	AppID int64 `json:"app_id"`

	// InstallationID is the per-organisation/user installation identifier
	// returned by the GitHub API when the App is installed on a repository.
	InstallationID int64 `json:"installation_id"`

	// WebhookSecret is the shared secret configured in the GitHub App
	// settings that ClawFlow uses to verify the HMAC signature on every
	// incoming delivery. Never log or include in HTTP responses.
	WebhookSecret string `json:"webhook_secret,omitempty"`

	// PrivateKeyRef is an opaque reference to the PEM private key for the
	// App. Empty means "not configured / use token auth only".
	PrivateKeyRef string `json:"private_key_ref,omitempty"`
}

// VCSConnection represents one GitHub repository connected to the ClawFlow
// cloud. It carries the installation credentials needed by the webhook
// handler and job scheduler.
type VCSConnection struct {
	// ID is a stable opaque identifier assigned by the store.
	ID string `json:"id"`

	// Repo is the canonical "owner/repo" string, e.g. "acme/backend".
	Repo string `json:"repo"`

	// Platform is always "github" for now; reserved for future GitLab support.
	Platform string `json:"platform"`

	// BoundMachineID, when non-empty, restricts job execution to the named
	// machine. Empty means "any capable worker".
	BoundMachineID string `json:"bound_machine_id,omitempty"`

	// GitHubApp carries the App-level credentials for this connection.
	GitHubApp *GitHubAppInstallation `json:"github_app,omitempty"`
}
