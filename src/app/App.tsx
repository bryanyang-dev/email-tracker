import { useEffect, useMemo, useState } from "react";
import {
  APIError,
  beginGmailAuthorization,
  createLocalSession,
  gmailConnectionStatus,
  loadGmailInbox,
  ollamaStatus,
  type GmailInboxMessage,
} from "./api";
import { navigationItems } from "./navigation";
import type { ConnectionState, EmailThread, LocalAIState, ThreadView } from "./types";

function threadsForView(view: ThreadView, allThreads: EmailThread[]): EmailThread[] {
  if (view === "all") return allThreads;
  if (view === "attention") {
    return allThreads.filter((thread) => thread.needsAttention);
  }
  return allThreads.filter((thread) => thread.state === view);
}

function displayName(view: ThreadView): string {
  return navigationItems.find((item) => item.id === view)?.label ?? "Threads";
}

export function App() {
  const [view, setView] = useState<ThreadView>("active");
  const [allThreads, setAllThreads] = useState<EmailThread[]>([]);
  const [connection, setConnection] = useState<ConnectionState>({ status: "loading" });
  const [localAI, setLocalAI] = useState<LocalAIState>({ status: "checking" });
  const [notice, setNotice] = useState<string | null>(null);
  const [inboxLoading, setInboxLoading] = useState(false);
  const [selectedId, setSelectedId] = useState("");
  const threads = useMemo(() => threadsForView(view, allThreads), [view, allThreads]);
  const selected =
    threads.find((thread) => thread.id === selectedId) ?? threads[0] ?? null;

  useEffect(() => {
    const oauthResult = new URLSearchParams(window.location.search);
    if (oauthResult.get("gmail") === "connected") {
      setNotice("Gmail connected. Your inbox is ready.");
      window.history.replaceState({}, "", window.location.pathname);
    } else if (oauthResult.get("gmail") === "error") {
      setNotice(`Gmail connection failed (${oauthResult.get("code") ?? "unknown_error"}).`);
      window.history.replaceState({}, "", window.location.pathname);
    }

    void initialize();
  }, []);

  async function initialize() {
    try {
      await createLocalSession();
      void refreshLocalAI();
      const status = await gmailConnectionStatus();
      if (!status.configured) {
        setConnection({ status: "not-configured" });
        return;
      }
      if (!status.connected) {
        setConnection({ status: "disconnected" });
        return;
      }
      setConnection({ status: "connected", emailAddress: status.emailAddress ?? "Gmail" });
      await refreshInbox();
    } catch (error) {
      setConnection({ status: "offline", message: errorMessage(error) });
    }
  }

  async function refreshLocalAI() {
    try {
      const status = await ollamaStatus();
      setLocalAI(
        status.available
          ? { status: "available", models: status.models.map((model) => model.name) }
          : { status: "unavailable", message: status.message ?? "Ollama is unavailable." },
      );
    } catch {
      setLocalAI({ status: "unavailable", message: "Could not check Ollama." });
    }
  }

  async function refreshInbox() {
    setInboxLoading(true);
    try {
      const inbox = await loadGmailInbox();
      const nextThreads = inbox.messages.map(messageToThread);
      setAllThreads(nextThreads);
      setSelectedId((current) =>
        nextThreads.some((thread) => thread.id === current) ? current : (nextThreads[0]?.id ?? ""),
      );
      setNotice(null);
    } catch (error) {
      setNotice(errorMessage(error));
    } finally {
      setInboxLoading(false);
    }
  }

  async function connectGmail() {
    try {
      const authorization = await beginGmailAuthorization();
      window.location.assign(authorization.authorizationUrl);
    } catch (error) {
      setNotice(errorMessage(error));
    }
  }

  function selectView(nextView: ThreadView) {
    setView(nextView);
    const nextThreads = threadsForView(nextView, allThreads);
    setSelectedId(nextThreads[0]?.id ?? "");
  }

  return (
    <main className="workspace">
      <aside className="sidebar" aria-label="Workspace navigation">
        <div className="brand">
          <span className="brand-mark" aria-hidden="true">
            L
          </span>
          <span>
            <strong>Local</strong>
            <small>Email workspace</small>
          </span>
        </div>

        <nav>
          <p className="nav-label">Workspace</p>
          <ul>
            {navigationItems.map((item) => {
              const count =
                item.id === "search" || item.id === "settings"
                  ? null
                  : threadsForView(item.id, allThreads).length;
              const available = item.id !== "search" && item.id !== "settings";

              return (
                <li key={item.id}>
                  <button
                    className={view === item.id ? "nav-item active" : "nav-item"}
                    type="button"
                    disabled={!available}
                    aria-current={view === item.id ? "page" : undefined}
                    onClick={() => {
                      if (item.id !== "search" && item.id !== "settings") {
                        selectView(item.id);
                      }
                    }}
                  >
                    <span>{item.label}</span>
                    {count !== null && <span className="count">{count}</span>}
                    {!available && <span className="soon">Soon</span>}
                  </button>
                </li>
              );
            })}
          </ul>
        </nav>

        <div className="service-statuses">
          <div className="service-status">
            <span
              className={connection.status === "connected" ? "status-dot connected" : "status-dot"}
              aria-hidden="true"
            />
            <span>
              <strong>{connection.status === "connected" ? "Gmail connected" : "Local service"}</strong>
              <small>
                {connection.status === "connected" ? connection.emailAddress : connectionLabel(connection)}
              </small>
            </span>
          </div>
          <div className="service-status">
            <span
              className={localAI.status === "available" ? "status-dot connected" : "status-dot"}
              aria-hidden="true"
            />
            <span>
              <strong>{localAITitle(localAI)}</strong>
              <small>{localAILabel(localAI)}</small>
            </span>
          </div>
        </div>
      </aside>

      <section className="thread-list" aria-labelledby="thread-list-heading">
        <header className="list-header">
          <div>
            <p className="eyebrow">Inbox</p>
            <h1 id="thread-list-heading">{displayName(view)}</h1>
          </div>
          <button
            className="icon-button"
            type="button"
            aria-label="Refresh threads"
            disabled={connection.status !== "connected" || inboxLoading}
            onClick={() => void refreshInbox()}
          >
            ↻
          </button>
        </header>

        {notice && <div className="notice" role="status">{notice}</div>}

        <ConnectionBanner connection={connection} inboxLoading={inboxLoading} />

        <div className="thread-items">
          {connection.status !== "connected" ? (
            <ConnectionCard connection={connection} onConnect={() => void connectGmail()} />
          ) : threads.length === 0 ? (
            <div className="empty-state">
              <h2>{inboxLoading ? "Loading inbox…" : "Nothing here yet"}</h2>
              <p>{inboxLoading ? "Retrieving recent Gmail messages." : "Threads in this state will appear here."}</p>
            </div>
          ) : (
            threads.map((thread) => (
              <button
                type="button"
                className={selected?.id === thread.id ? "thread-card selected" : "thread-card"}
                key={thread.id}
                onClick={() => setSelectedId(thread.id)}
                aria-pressed={selected?.id === thread.id}
              >
                <span className="thread-title-line">
                  <span>
                    {thread.unread && <span className="unread-dot" aria-label="Unread" />}
                    <strong>{thread.title}</strong>
                  </span>
                  <time>{thread.updatedAt}</time>
                </span>
                <span className="participants">{thread.participants.join(", ")}</span>
                <span className="preview">{thread.preview}</span>
                <span className="thread-meta">
                  {thread.needsAttention && <span className="attention-tag">Needs attention</span>}
                  <span>{thread.messageCount} messages</span>
                </span>
              </button>
            ))
          )}
        </div>
      </section>

      <section className="thread-detail" aria-label="Selected thread">
        {selected ? (
          <ThreadDetail thread={selected} />
        ) : connection.status === "disconnected" ? (
          <ConnectDetail onConnect={() => void connectGmail()} />
        ) : (
          <NoSelection />
        )}
      </section>
    </main>
  );
}

function ThreadDetail({ thread }: { thread: EmailThread }) {
  return (
    <>
      <header className="detail-header">
        <div>
          <p className="eyebrow">Conversation</p>
          <h2>{thread.title}</h2>
          <p>{thread.participants.join(" · ")}</p>
        </div>
        <div className="detail-actions">
          <button className="secondary-button" type="button">Snooze</button>
          <button className="primary-button" type="button">Resolve</button>
        </div>
      </header>

      <div className="detail-scroll">
        <section className="latest-update panel">
          <span className="panel-kicker">Latest update</span>
          <p>{thread.latestUpdate}</p>
        </section>

        <section className="panel">
          <div className="panel-heading">
            <h3>Summary</h3>
            <span className="local-ai">Local AI</span>
          </div>
          <p>{thread.summary}</p>
        </section>

        <section className="panel">
          <div className="panel-heading">
            <h3>Action items</h3>
            <span>{thread.actionItems.length}</span>
          </div>
          {thread.actionItems.length ? (
            <ul className="action-list">
              {thread.actionItems.map((item) => (
                <li key={item.id}>
                  <input
                    type="checkbox"
                    checked={item.completed}
                    readOnly
                    aria-label={`Complete ${item.description}`}
                  />
                  <span>
                    <strong>{item.description}</strong>
                    <small>
                      {item.owner}{item.due ? ` · ${item.due}` : ""}
                    </small>
                  </span>
                </li>
              ))}
            </ul>
          ) : (
            <p className="muted">No action items were found.</p>
          )}
        </section>

        <section className="detail-grid">
          <div className="panel compact-panel">
            <span className="panel-kicker">Participants</span>
            <strong>{thread.participants.length}</strong>
          </div>
          <div className="panel compact-panel">
            <span className="panel-kicker">Attachments</span>
            <strong>{thread.attachmentCount}</strong>
          </div>
          <div className="panel compact-panel">
            <span className="panel-kicker">Source messages</span>
            <strong>{thread.messageCount}</strong>
          </div>
        </section>

        <button className="source-button" type="button">
          View source-message timeline
          <span aria-hidden="true">→</span>
        </button>
      </div>
    </>
  );
}

function NoSelection() {
  return (
    <div className="no-selection">
      <span aria-hidden="true">◇</span>
      <h2>Select a thread</h2>
      <p>Conversation details will appear here.</p>
    </div>
  );
}

function ConnectionBanner({
  connection,
  inboxLoading,
}: {
  connection: ConnectionState;
  inboxLoading: boolean;
}) {
  const connected = connection.status === "connected";
  return (
    <div className="sync-note" role="status">
      <span className={connected ? "sync-icon" : "sync-icon waiting"} aria-hidden="true">
        {connected ? "✓" : "·"}
      </span>
      <span>
        {connected
          ? inboxLoading
            ? "Refreshing Gmail inbox…"
            : `Connected as ${connection.emailAddress}`
          : connectionLabel(connection)}
      </span>
    </div>
  );
}

function ConnectionCard({
  connection,
  onConnect,
}: {
  connection: ConnectionState;
  onConnect: () => void;
}) {
  if (connection.status === "loading") {
    return <div className="connection-card"><strong>Starting local service session…</strong></div>;
  }
  if (connection.status === "not-configured") {
    return (
      <div className="connection-card">
        <strong>Gmail OAuth is not configured</strong>
        <p>Set <code>GMAIL_CLIENT_ID</code> on the local Go service, then restart it.</p>
      </div>
    );
  }
  if (connection.status === "offline") {
    return (
      <div className="connection-card error-card">
        <strong>Local service unavailable</strong>
        <p>{connection.message}</p>
      </div>
    );
  }
  return (
    <div className="connection-card">
      <strong>Bring your inbox into your private workspace</strong>
      <p>Connect one Gmail account with read-only access. Tokens stay in macOS Keychain.</p>
      <button className="primary-button" type="button" onClick={onConnect}>Connect Gmail</button>
    </div>
  );
}

function ConnectDetail({ onConnect }: { onConnect: () => void }) {
  return (
    <div className="connect-detail">
      <span className="connect-mark" aria-hidden="true">@</span>
      <p className="eyebrow">First step</p>
      <h2>Connect Gmail</h2>
      <p>
        Sign in through Google to retrieve inbox messages. The app requests read-only access and
        keeps authorization credentials out of the browser.
      </p>
      <button className="primary-button" type="button" onClick={onConnect}>Connect Gmail</button>
    </div>
  );
}

function messageToThread(message: GmailInboxMessage): EmailThread {
  return {
    id: message.id,
    title: message.subject,
    participants: [message.from || "Unknown sender"],
    updatedAt: displayMessageDate(message),
    preview: message.snippet,
    latestUpdate: message.snippet || "No message preview is available.",
    summary: "Local AI enrichment has not run for this conversation yet.",
    state: "active",
    needsAttention: false,
    unread: message.unread,
    actionItems: [],
    attachmentCount: 0,
    messageCount: 1,
  };
}

function displayMessageDate(message: GmailInboxMessage): string {
  const millis = Number(message.internalAt);
  const date = Number.isFinite(millis) && millis > 0 ? new Date(millis) : new Date(message.date);
  if (Number.isNaN(date.getTime())) return "";
  const today = new Date();
  if (date.toDateString() === today.toDateString()) {
    return new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit" }).format(date);
  }
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" }).format(date);
}

function connectionLabel(connection: ConnectionState): string {
  switch (connection.status) {
    case "loading":
      return "Connecting to local service…";
    case "offline":
      return "Service unavailable";
    case "not-configured":
      return "Gmail setup required";
    case "disconnected":
      return "Gmail not connected";
    case "connected":
      return connection.emailAddress;
  }
}

function localAILabel(localAI: LocalAIState): string {
  switch (localAI.status) {
    case "checking":
      return "Checking Ollama…";
    case "unavailable":
      return localAI.message;
    case "available":
      if (localAI.models.length === 0) return "No models installed";
      if (localAI.models.length === 1) return localAI.models[0] ?? "1 model installed";
      return `${localAI.models.length} models installed`;
  }
}

function localAITitle(localAI: LocalAIState): string {
  if (localAI.status === "checking") return "Checking local AI";
  if (localAI.status === "unavailable") return "Local AI unavailable";
  return localAI.models.length ? "Local AI ready" : "Ollama running";
}

function errorMessage(error: unknown): string {
  if (error instanceof APIError || error instanceof Error) {
    return error.message;
  }
  return "An unexpected local error occurred.";
}
