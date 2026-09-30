import { useEffect, useMemo, useRef, useState } from "react";
import {
  APIError,
  beginGmailAuthorization,
  createLocalSession,
  gmailConnectionStatus,
  loadGmailConversation,
  loadGmailInbox,
  ollamaStatus,
  triageGmailConversation,
  type GmailConversation,
  type GmailInboxMessage,
  type GmailThreadTriage,
  type GmailTriageAssessment,
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
  const [pageIndex, setPageIndex] = useState(0);
  const [pageTokens, setPageTokens] = useState<string[]>([""]);
  const [nextPageToken, setNextPageToken] = useState("");
  const [conversations, setConversations] = useState<Record<string, GmailConversation>>({});
  const [conversationLoadingId, setConversationLoadingId] = useState("");
  const triageCache = useRef(new Map<string, GmailThreadTriage>());
  const threads = useMemo(() => threadsForView(view, allThreads), [view, allThreads]);
  const selected =
    threads.find((thread) => thread.id === selectedId) ?? threads[0] ?? null;
  const selectedConversation = selected ? conversations[selected.gmailThreadId] : undefined;

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
    const threadId = selected?.gmailThreadId;
    if (connection.status !== "connected" || !threadId || conversations[threadId]) return;

    let cancelled = false;
    setConversationLoadingId(threadId);
    void loadGmailConversation(threadId)
      .then((conversation) => {
        if (!cancelled) {
          setConversations((current) => ({ ...current, [threadId]: conversation }));
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
  }, [connection.status, conversations, selected?.gmailThreadId]);

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
      await loadInboxPage(0, "");
    } catch (error) {
      setConnection({ status: "offline", message: errorMessage(error) });
    }
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

  async function loadInboxPage(nextPageIndex: number, pageToken: string) {
    setInboxLoading(true);
    try {
      const inbox = await loadGmailInbox(pageToken);
      const nextThreads = inbox.messages.map(messageToThread);
      setAllThreads(nextThreads);
      setPageIndex(nextPageIndex);
      setNextPageToken(inbox.nextPageToken ?? "");
      setSelectedId((current) =>
        nextThreads.some((thread) => thread.id === current) ? current : (nextThreads[0]?.id ?? ""),
      );
      const triageResults: Array<{
        thread: EmailThread;
        conversation?: GmailConversation;
      }> = [];
      for (const thread of nextThreads) {
        try {
          const cacheKey = `${thread.gmailThreadId}:${thread.id}`;
          const result =
            triageCache.current.get(cacheKey) ??
            (await triageGmailConversation(thread.gmailThreadId));
          triageCache.current.set(cacheKey, result);
          triageResults.push({
            thread: applyTriage(thread, result.triage),
            conversation: result.conversation,
          });
          setConversations((current) => ({
            ...current,
            [thread.gmailThreadId]: result.conversation,
          }));
        } catch {
          triageResults.push({ thread: { ...thread, triageStatus: "failed" } });
        }
        setAllThreads([
          ...triageResults.map((result) => result.thread),
          ...nextThreads.slice(triageResults.length),
        ]);
      }
      setAllThreads(triageResults.map((result) => result.thread));
      setNotice(
        triageResults.some((result) => result.thread.triageStatus === "failed")
          ? "Some conversations could not be triaged and remain in Suggested."
          : null,
      );
    } catch (error) {
      setNotice(errorMessage(error));
    } finally {
      setInboxLoading(false);
    }
  }

  function refreshInbox() {
    void loadInboxPage(pageIndex, pageTokens[pageIndex] ?? "");
  }

  function showNextPage() {
    if (!nextPageToken || inboxLoading) return;
    const targetIndex = pageIndex + 1;
    const targetToken = nextPageToken;
    setPageTokens((current) => {
      const next = current.slice(0, targetIndex);
      next[targetIndex] = targetToken;
      return next;
    });
    void loadInboxPage(targetIndex, targetToken);
  }

  function showPreviousPage() {
    if (pageIndex === 0 || inboxLoading) return;
    const targetIndex = pageIndex - 1;
    void loadInboxPage(targetIndex, pageTokens[targetIndex] ?? "");
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
            onClick={refreshInbox}
          >
            ↻
          </button>
        </header>

        {notice && <div className="notice" role="status">{notice}</div>}

        <ConnectionBanner connection={connection} inboxLoading={inboxLoading} />

        <div className={threads.length ? "thread-items paginated" : "thread-items"}>
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
                  {thread.state === "suggested" && <span className="attention-tag">Suggested</span>}
                  {thread.state === "ordinary" && <span className="low-priority-tag">Low priority</span>}
                  <span>{thread.messageCount} messages</span>
                </span>
              </button>
            ))
          )}
        </div>

        {connection.status === "connected" && (
          <nav className="pagination" aria-label="Inbox pages">
            <button
              type="button"
              disabled={pageIndex === 0 || inboxLoading}
              onClick={showPreviousPage}
            >
              Previous
            </button>
            <span aria-live="polite">Page {pageIndex + 1}</span>
            <button
              type="button"
              disabled={!nextPageToken || inboxLoading}
              onClick={showNextPage}
            >
              Next
            </button>
          </nav>
        )}
      </section>

      <section className="thread-detail" aria-label="Selected thread">
        {selected ? (
          <ThreadDetail
            thread={selected}
            conversation={selectedConversation}
            loading={conversationLoadingId === selected.gmailThreadId}
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
  conversation?: GmailConversation;
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
            ? "Loading and triaging recent Gmail conversations…"
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
    gmailThreadId: message.threadId,
    title: message.subject,
    participants: [message.from || "Unknown sender"],
    updatedAt: displayMessageDate(message),
    preview: message.snippet,
    latestUpdate: message.snippet || "No message preview is available.",
    summary: "Local AI enrichment has not run for this conversation yet.",
    state: "suggested",
    needsAttention: false,
    unread: message.unread,
    actionItems: [],
    attachmentCount: 0,
    messageCount: 1,
    triageCategory: "other",
    triageReasons: [],
    triageStatus: "pending",
  };
}

function applyTriage(thread: EmailThread, assessment: GmailTriageAssessment): EmailThread {
  return {
    ...thread,
    state: assessment.visibility === "all" ? "ordinary" : assessment.visibility,
    needsAttention: assessment.needsAction || assessment.urgent,
    triageCategory: assessment.category,
    triageReasons: assessment.reasonCodes,
    triageStatus: assessment.aiStatus,
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
    explanation = `Kept out of Active because it was classified as ${thread.triageCategory.replaceAll("_", " ")}. It remains available in All threads.`;
  }
  const reasons = thread.triageReasons.map((reason) => reason.replaceAll("_", " "));
  return reasons.length ? `${explanation} Signals: ${reasons.join(", ")}.` : explanation;
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
