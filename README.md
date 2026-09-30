# Local Email Workspace

A macOS-first, local email workspace. The React client is served by a local Go
service and communicates with it through a same-origin `/api/v1` API.

## Prerequisites

- Node.js and npm
- Go 1.24 or newer
- macOS with Xcode Command Line Tools and the Xcode license accepted, required
  by the native Keychain adapter
- A Google Cloud Desktop OAuth client configured as described below
- Ollama installed locally for optional AI features; core Gmail functionality
  remains available when Ollama is stopped

Install the frontend dependencies once:

```sh
npm install
```

## Local configuration

- `config/local.env.template` is the committed starter file. It contains only
  variable names, placeholders, and safe local defaults.
- `.env` is the developer-specific file read by the Go service. It may contain
  credentials and is ignored by Git.
- Process environment variables take precedence over values in `.env`, which
  allows packaged or automated environments to supply configuration without a
  file.

Never copy real client secrets or tokens back into the committed template.

## Start the application

Development uses two independent foreground processes. Both terminals must
remain open; `npm run dev` does not start or supervise the Go API.

In terminal 1, build and start the Go API at a stable local path:

```sh
npm run dev:api
```

The launcher rebuilds `.local/bin/email-workspace` and then runs it. This avoids
the changing temporary executable path created by `go run` and makes Keychain
authorization behavior more predictable during development.

Wait for this message:

```text
INFO local email service listening address=http://127.0.0.1:8787 gmail_configured=true
```

In terminal 2, start the React development server:

```sh
npm run dev
```

Open <http://127.0.0.1:5173>. Vite forwards `/api` requests to the Go service
at `http://127.0.0.1:8787`.

Stop each process with `Ctrl+C` in its terminal. Closing either terminal stops
that part of the application.

### Startup troubleshooting

- `ECONNREFUSED 127.0.0.1:8787` means Vite is running but the Go API is not.
  Start or restart `npm run dev:api` in terminal 1.
- `gmail_configured=false` means `GMAIL_CLIENT_ID` was not loaded from `.env`
  or the process environment.
- `address already in use` means another process is already bound to the port.
- An Xcode license error must be resolved before Go can compile the native
  macOS Keychain adapter.
- macOS may display two login Keychain password prompts when the API first
  reads the saved Gmail authorization. Approving both prompts is a known,
  non-blocking development behavior; the service caches the credential after
  that initial read.
- Do not use `go run` for normal startup. Its temporary executable identity can
  make Keychain ask again on every run.

## Development checks

Useful verification commands:

```sh
npm run build
npm run test
npm run typecheck
go test ./...
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
4. If `.env` does not already exist, create your untracked local configuration
   from the committed template:

```sh
cp config/local.env.template .env
```

Then add the generated client credentials to `.env`:

```sh
GMAIL_CLIENT_ID="your-client-id.apps.googleusercontent.com"
GMAIL_CLIENT_SECRET="your-desktop-client-secret"
```

5. Follow the two-terminal instructions in **Start the application**.

Process environment variables take precedence over `.env`. The client secret is
optional for desktop clients. The OAuth callback is
`http://127.0.0.1:8787/api/v1/auth/gmail/callback`.

## Ollama development setup

The Go service checks Ollama at `http://127.0.0.1:11434` and exposes only model
availability to the React client. React never connects to Ollama directly.

When the Go API starts, it checks the configured endpoint and runs the installed
`ollama serve` command only if Ollama is unavailable. The child process is bound
to the configured loopback address and stopped when the Go API shuts down. The
sidebar reports whether Ollama is available and how many local models are
installed. The application does not install Ollama, download models, or fall
back to a cloud model.

To use a different local port, set `OLLAMA_BASE_URL` in `.env`. The URL must use
plain HTTP and the literal `127.0.0.1` host. Set `OLLAMA_AUTO_START=false` to
require Ollama to be started independently.
