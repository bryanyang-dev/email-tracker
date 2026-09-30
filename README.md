# Local Email Workspace

A macOS-first, local email workspace. The React client is served by a local Go
service and communicates with it through a same-origin `/api/v1` API.

## Frontend development

The frontend uses React, TypeScript, and Vite. Dependencies are intentionally
kept small; server state will be accessed through feature-scoped hooks rather
than a global client-side store.

```sh
npm install
npm run dev
```

Other useful commands:

```sh
npm run build
npm run test
npm run typecheck
```

The React client now reads Gmail connection status and inbox metadata from the
local Go API. SQLite-backed synchronization and thread enrichment are later
milestones.

## Gmail development setup

The local Go service uses Google's desktop OAuth flow with PKCE and requests the
read-only Gmail scope. OAuth tokens are stored in macOS Keychain and are never
returned to the browser.

1. In Google Cloud Console, enable the Gmail API.
2. Configure an OAuth consent screen and add your Gmail account as a test user.
3. Create an OAuth client with the **Desktop app** application type.
4. Copy `.env.example` to `.env` and add the generated client credentials:

```sh
GMAIL_CLIENT_ID="your-client-id.apps.googleusercontent.com"
GMAIL_CLIENT_SECRET="your-desktop-client-secret"
```

5. Start the local service with `go run ./cmd/email-workspace`.

Process environment variables take precedence over `.env`. The client secret is
optional for desktop clients. The OAuth callback is
`http://127.0.0.1:8787/api/v1/auth/gmail/callback`. In another terminal, run
`npm run dev` and open `http://127.0.0.1:5173`.
