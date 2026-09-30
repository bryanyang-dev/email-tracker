import { useEffect, useRef, useState } from "react";
import {
  APIError,
  beginGmailAuthorization,
  createLocalSession,
  gmailConnectionStatus,
  loadWorkspaceConversation,
  loadWorkspaceConversations,
  ollamaStatus,
  synchronizeGmailMailbox,
  type GmailSyncStatus,
  type WorkspaceConversation,
  type WorkspaceConversationSummary,
} from "./api";
import { navigationItems } from "./navigation";
import type { ConnectionState, EmailThread, LocalAIState, ThreadView } from "./types";

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
  const [mailboxSync, setMailboxSync] = useState<"idle" | "syncing" | "ready" | "failed">("idle");
  const [mailboxSyncStatus, setMailboxSyncStatus] = useState<GmailSyncStatus | null>(null);
  const [mailboxSyncError, setMailboxSyncError] = useState<string | null>(null);
  const [selectedId, setSelectedId] = useState("");
  const [counts, setCounts] = useState<Record<ThreadView, number>>({
    active: 0,
    attention: 0,
    suggested: 0,
    snoozed: 0,
    resolved: 0,
    all: 0,
  });
  const [conversations, setConversations] = useState<Record<string, WorkspaceConversation>>({});
  const [conversationLoadingId, setConversationLoadingId] = useState("");
  const viewRequest = useRef(0);
  const threads = allThreads;
  const selected =
    threads.find((thread) => thread.id === selectedId) ?? threads[0] ?? null;
  const selectedConversation = selected ? conversations[selected.id] : undefined;

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

  useEffect(() => {
    const conversationId = selected?.id;
    if (connection.status !== "connected" || !conversationId || conversations[conversationId]) return;

    let cancelled = false;
    setConversationLoadingId(conversationId);
    void loadWorkspaceConversation(conversationId)
      .then((conversation) => {
        if (!cancelled) {
          setConversations((current) => ({ ...current, [conversationId]: conversation }));
        }
      })
      .catch((error) => {
        if (!cancelled) setNotice(errorMessage(error));
      })
      .finally(() => {
        if (!cancelled) setConversationLoadingId("");
      });

    return () => {
      cancelled = true;
    };
  }, [connection.status, conversations, selected?.id]);

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
      startMailboxSync();
      await loadConversationView("active");
    } catch (error) {
      setConnection({ status: "offline", message: errorMessage(error) });
    }
  }

  function startMailboxSync() {
    setMailboxSync("syncing");
    setMailboxSyncError(null);
    void synchronizeGmailMailbox(setMailboxSyncStatus).then(
      () => {
        setMailboxSync("ready");
        void loadConversationView(view);
      },
      (error) => {
        setMailboxSync("failed");
        setMailboxSyncError(errorMessage(error));
      },
    );
  }

  async function refreshLocalAI() {
    try {
      const status = await ollamaStatus();
      setLocalAI(
        status.available
          ? {
              status: "available",
              models: status.models.map((model) => model.name),
              triageModel: status.triageModel,
              message: status.message,
            }
          : { status: "unavailable", message: status.message ?? "Ollama is unavailable." },
      );
    } catch {
      setLocalAI({ status: "unavailable", message: "Could not check Ollama." });
    }
  }

  async function loadConversationView(nextView: ThreadView) {
    const requestID = viewRequest.current + 1;
    viewRequest.current = requestID;
    setInboxLoading(true);
    try {
      const page = await loadWorkspaceConversations(nextView);
      if (requestID !== viewRequest.current) return;
      const nextThreads = page.conversations.map(conversationToThread);
      setAllThreads(nextThreads);
      setCounts(page.counts);
      setSelectedId((current) =>
        nextThreads.some((thread) => thread.id === current) ? current : (nextThreads[0]?.id ?? ""),
      );
    } catch (error) {
      if (requestID === viewRequest.current) setNotice(errorMessage(error));
    } finally {
      if (requestID === viewRequest.current) setInboxLoading(false);
    }
  }

  function refreshInbox() {
    void loadConversationView(view);
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
    setSelectedId("");
    void loadConversationView(nextView);
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
                  : counts[item.id];
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
            onClick={refreshInbox}
          >
            ↻
          </button>
        </header>

        {notice && <div className="notice" role="status">{notice}</div>}

        <ConnectionBanner
          connection={connection}
          inboxLoading={inboxLoading}
          mailboxSync={mailboxSync}
          mailboxSyncStatus={mailboxSyncStatus}
          mailboxSyncError={mailboxSyncError}
          onResumeSync={startMailboxSync}
        />

        <div className="thread-items">
          {connection.status !== "connected" ? (
            <ConnectionCard connection={connection} onConnect={() => void connectGmail()} />
          ) : threads.length === 0 ? (
            <div className="empty-state">
              <h2>{inboxLoading ? "Loading inbox…" : "Nothing here yet"}</h2>
              <p>{inboxLoading ? "Reading the local conversation index." : "Threads in this state will appear here."}</p>
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
                  {thread.state === "suggested" && <span className="attention-tag">Suggested</span>}
                  <span>{thread.messageCount} messages</span>
                </span>
              </button>
            ))
          )}
        </div>

      </section>

      <section className="thread-detail" aria-label="Selected thread">
        {selected ? (
          <ThreadDetail
            thread={selected}
            conversation={selectedConversation}
            loading={conversationLoadingId === selected.id}
          />
        ) : connection.status === "disconnected" ? (
          <ConnectDetail onConnect={() => void connectGmail()} />
        ) : (
          <NoSelection />
        )}
      </section>
    </main>
  );
}

function ThreadDetail({
  thread,
  conversation,
  loading,
}: {
  thread: EmailThread;
  conversation?: WorkspaceConversation;
  loading: boolean;
}) {
  const latestMessage = conversation?.messages.at(-1);

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
          <p>{loading ? "Loading conversation…" : latestMessage?.body || thread.latestUpdate}</p>
          {latestMessage?.bodyTruncated && <small>Message body was truncated for safety.</small>}
          {latestMessage?.suspiciousContent && (
            <small>Invisible or malformed content was removed during normalization.</small>
          )}
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
            <h3>Triage</h3>
            <span>{triageStatusLabel(thread.triageStatus)}</span>
          </div>
          <p>{triageExplanation(thread)}</p>
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
            <strong>{conversation?.messages.length ?? thread.messageCount}</strong>
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
  mailboxSync,
  mailboxSyncStatus,
  mailboxSyncError,
  onResumeSync,
}: {
  connection: ConnectionState;
  inboxLoading: boolean;
  mailboxSync: "idle" | "syncing" | "ready" | "failed";
  mailboxSyncStatus: GmailSyncStatus | null;
  mailboxSyncError: string | null;
  onResumeSync: () => void;
}) {
  const connected = connection.status === "connected";
  const onboarding = mailboxSyncStatus?.phase === "discovering" || mailboxSyncStatus?.phase === "reconciling";
  const determinate = onboarding &&
    (mailboxSyncStatus?.estimatedTotal ?? 0) > (mailboxSyncStatus?.onboardingProcessed ?? 0);
  const percent = determinate
    ? Math.min(99, Math.floor(
        ((mailboxSyncStatus?.onboardingProcessed ?? 0) / (mailboxSyncStatus?.estimatedTotal ?? 1)) * 100,
      ))
    : undefined;
  const progressLabel = syncProgressLabel(mailboxSyncStatus, percent);
  return (
    <div className="sync-note" role="status">
      <span className={connected ? "sync-icon" : "sync-icon waiting"} aria-hidden="true">
        {connected ? "✓" : "·"}
      </span>
      <span>
        {connected
          ? inboxLoading
            ? "Loading conversations from the local index…"
            : `Connected as ${connection.emailAddress}`
          : connectionLabel(connection)}
        {connected && mailboxSync === "syncing" && (
          <>
            <small>{progressLabel}</small>
            <div
              className={`sync-progress${determinate ? "" : " indeterminate"}`}
              role="progressbar"
              aria-label="Mailbox sync progress"
              aria-valuemin={determinate ? 0 : undefined}
              aria-valuemax={determinate ? 100 : undefined}
              aria-valuenow={percent}
              aria-valuetext={progressLabel}
            >
              <span style={determinate ? { width: `${percent}%` } : undefined} />
            </div>
          </>
        )}
        {connected && mailboxSync === "ready" && <small>Local mailbox index ready</small>}
        {connected && mailboxSync === "failed" && (
          <div className="sync-failed">
            <small>{mailboxSyncError ?? "Local indexing paused; indexed conversations remain available."}</small>
            <button type="button" onClick={onResumeSync}>Resume indexing</button>
          </div>
        )}
      </span>
    </div>
  );
}

function syncProgressLabel(status: GmailSyncStatus | null, percent?: number): string {
  if (!status || status.phase === "not_started") return "Preparing the two-week mail index…";
  if (status.phase === "catching_up") return "Checking for mail that arrived during setup…";
  if (status.phase === "processing") {
    return status.pendingConversations > 0
      ? `Classifying cached conversations… ${status.pendingConversations} remaining`
      : "Finishing conversation processing…";
  }
  if (status.phase === "reconciling" && percent === undefined && status.onboardingProcessed === 0) {
    return "Preparing a two-week mailbox refresh…";
  }
  if (percent === undefined && status.onboardingProcessed > 0) {
    const verb = status.phase === "reconciling" ? "Refreshed" : "Indexed";
    return `${verb} ${status.onboardingProcessed} messages; discovering more…`;
  }
  if (percent === undefined) return "Indexing the last two weeks of mail…";
  return `Indexed ${status.onboardingProcessed} of about ${status.estimatedTotal} messages (${percent}%)`;
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

function conversationToThread(conversation: WorkspaceConversationSummary): EmailThread {
  return {
    id: conversation.id,
    title: conversation.title,
    participants: conversation.participants.length ? conversation.participants : ["Unknown sender"],
    updatedAt: displayConversationDate(conversation.updatedAt),
    preview: conversation.preview,
    latestUpdate: conversation.latestUpdate || conversation.preview || "No message preview is available.",
    summary: "This conversation was selected from the local importance index.",
    state: conversation.state,
    needsAttention: conversation.needsAttention,
    unread: conversation.unread,
    actionItems: [],
    attachmentCount: 0,
    messageCount: conversation.messageCount,
    triageCategory: conversation.category || "other",
    triageReasons: conversation.reasonCodes,
    triageStatus: conversation.aiStatus,
  };
}

function triageStatusLabel(status: EmailThread["triageStatus"]): string {
  switch (status) {
    case "pending":
      return "Reviewing";
    case "applied":
      return "Local AI";
    case "rules":
      return "Rules";
    case "unavailable":
      return "Rules only";
    case "failed":
      return "Needs review";
  }
}

function triageExplanation(thread: EmailThread): string {
  if (thread.triageStatus === "pending") return "This conversation is waiting for local triage.";
  if (thread.triageStatus === "failed") {
    return "Local triage failed, so this conversation remains visible in Suggested and All threads.";
  }
  let explanation: string;
  if (thread.state === "active") {
    explanation = thread.needsAttention
      ? "Shown in Active because it appears urgent or requires an action."
      : "Shown in Active because it met an importance floor or contains an important update.";
  } else if (thread.state === "suggested") {
    explanation = "Kept in Suggested because its importance or required action is uncertain.";
  } else {
    explanation = "This conversation is no longer active.";
  }
  const reasons = thread.triageReasons.map((reason) => reason.replaceAll("_", " "));
  return reasons.length ? `${explanation} Signals: ${reasons.join(", ")}.` : explanation;
}

function displayConversationDate(value: string): string {
  const date = new Date(value);
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
      if (!localAI.triageModel && localAI.message) return localAI.message;
      if (localAI.triageModel) return localAI.triageModel;
      if (localAI.models.length === 0) return "No models installed";
      if (localAI.models.length === 1) return localAI.models[0] ?? "1 model installed";
      return `${localAI.models.length} models installed`;
  }
}

function localAITitle(localAI: LocalAIState): string {
  if (localAI.status === "checking") return "Checking local AI";
  if (localAI.status === "unavailable") return "Local AI unavailable";
  return localAI.triageModel ? "Local AI ready" : "Ollama running";
}

function errorMessage(error: unknown): string {
  if (error instanceof APIError || error instanceof Error) {
    return error.message;
  }
  return "An unexpected local error occurred.";
}
