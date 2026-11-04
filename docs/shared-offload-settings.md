# Shared Offload settings

ClawFlow follows Offload's default profile when no machine, environment or role
override is configured. The execution settings page displays the Offload default
separately from explicit ClawFlow overrides.

Saving a default profile calls Offload's `default` command and reads its profile
snapshot back. The change affects other consumers following that same Offload
configuration. Existing execution checkpoints retain their pinned profile.

The API exposes `default_profile` on `GET /api/providers`; `PUT /api/providers`
accepts `{"default_profile":"existing-profile-name"}`. Offload validates and
persists the selection. Credentials are never returned or copied to ClawFlow.
