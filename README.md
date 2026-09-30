# Local Email Workspace

A macOS-first, local email workspace. The React client is served by a local Go
service and communicates with it through a same-origin `/api/v1` API.

## Prerequisites

- Node.js and npm
- Go 1.26 or newer
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

The service stores its workspace database under the operating system's user
configuration directory. On macOS, the default is
`~/Library/Application Support/Local Email Workspace/workspace.sqlite`. Set
`APP_DATA_DIR` to an absolute path to override the directory for development.
The current database foundation uses restrictive file permissions but is not
yet SQLCipher-encrypted. During development it stores Gmail metadata and the
normalized bodies of conversations selected for local importance processing.
Do not copy or share the database file; database encryption must be completed
before packaging the application for general use.

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

The React client reads conversation lists and message bodies only from SQLite
through the local Go API. Switching views, refreshing, and selecting a conversation do
not call Gmail or Ollama and do not perform classification on demand. Initial
onboarding builds a resumable, metadata-only index for a
fixed two-week window in pages of 20 messages. Message IDs, Gmail thread IDs,
reply headers, participants, labels, snippets, onboarding completion, and sync
cursors are cached in SQLite. Returning users use Gmail History changes from
the last committed checkpoint instead of rescanning the two-week window. An
expired History checkpoint triggers a bounded two-week reconciliation without
deleting the existing local workspace. After synchronization, obvious bulk mail
is resolved from metadata; remaining provider conversations are hydrated once,
classified locally, and persisted as Active or Suggested workspace
conversations only when the importance policy allows it.

Mailbox synchronization runs separately in the background. It is the only UI
flow that fetches new Gmail data and invokes local classification; after a sync
batch finishes, the client refreshes the current view from SQLite.

Each workspace view is a scrollable database-backed list. Its sidebar count is
computed across the full local workspace rather than from the currently visible
rows. After importance processing, a deterministic reconciliation pass combines
separate Gmail threads when reply headers link them or when a bounded set of
corroborating subject, recruiting-topic, organization, sender-domain, and time
signals establishes a high-confidence match. Locked threads and recorded
never-merge relationships are excluded.

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

The Go service checks Ollama at `http://127.0.0.1:11434`. React never connects
to Ollama directly. The service sends one bounded, normalized conversation at a
time for structured local triage and validates the model response before using
it to choose Active, Suggested, or All Threads visibility.

When the Go API starts, it checks the configured endpoint and runs the installed
`ollama serve` command only if Ollama is unavailable. The child process is bound
to the configured loopback address and stopped when the Go API shuts down. The
sidebar reports whether Ollama is available and how many local models are
installed. The application does not install Ollama, download models, or fall
back to a cloud model.

To use a different local port, set `OLLAMA_BASE_URL` in `.env`. The URL must use
plain HTTP and the literal `127.0.0.1` host. Set `OLLAMA_AUTO_START=false` to
require Ollama to be started independently.

Set `OLLAMA_MODEL` to the installed model name used for email triage. When it is
empty, the service uses the only installed model. If several models are
installed, set this value explicitly so model selection is predictable.
