# TaskNotes 接入

Independent Apache-2.0 plugin for official Paca v0.18.6.

Status: phase 0, host compatibility baseline; task ingestion and delivery are not yet implemented.
All builds and automated checks run in GitHub Actions. No Paca core fork is required by the baseline.

Each release contains its own WASM, frontend, migrations and independent worker image. Never mix versions.
The TaskNotes integration is desktop-only and one-way; the PushGo plugin can run without TaskNotes.
Production credentials are runtime configuration only.
