export interface GmailConnectionStatus {
  configured: boolean;
  connected: boolean;
  emailAddress?: string;
}

export interface GmailSyncStatus {
  phase: "not_started" | "discovering" | "catching_up" | "processing" | "complete" | "reconciling";
  complete: boolean;
  messagesCached: number;
  hasMore: boolean;
  processed: number;
  onboardingProcessed: number;
  estimatedTotal: number;
  pendingConversations: number;
  conversationsProcessed: number;
  conversationsCreated: number;
}

export interface WorkspaceConversationMessage {
  id: string;
  threadId: string;
  rfcMessageId?: string;
  subject: string;
  from: string;
  to: string;
  cc?: string;
  date: string;
  internalAt: string;
  labelIds: string[];
  body: string;
  bodySource: "plain" | "html" | "mixed" | "none";
  bodyTruncated: boolean;
  suspiciousContent: boolean;
  hasListUnsubscribe?: boolean;
  hasListId?: boolean;
  precedence?: string;
  autoSubmitted?: boolean;
  hasFeedbackId?: boolean;
}

export interface WorkspaceConversationSummary {
  id: string;
  title: string;
  state: "active" | "suggested" | "snoozed" | "resolved";
  importance: "important" | "possibly_important";
  importanceScore: number;
  updatedAt: string;
  messageCount: number;
  unread: boolean;
  needsAttention: boolean;
  participants: string[];
  preview: string;
  latestUpdate: string;
  category: string;
  reasonCodes: string[];
  aiStatus: "pending" | "applied" | "rules" | "unavailable" | "failed";
}

export interface WorkspaceConversation extends WorkspaceConversationSummary {
  messages: WorkspaceConversationMessage[];
}

export interface WorkspaceConversationPage {
  conversations: WorkspaceConversationSummary[];
  counts: {
    active: number;
    attention: number;
    suggested: number;
    snoozed: number;
    resolved: number;
    all: number;
  };
}

export interface OllamaModel {
  name: string;
  parameterSize?: string;
  quantizationLevel?: string;
  size: number;
}

export interface OllamaStatus {
  available: boolean;
  models: OllamaModel[];
  triageModel?: string;
  message?: string;
}

interface APIErrorEnvelope {
  error?: {
    code?: string;
    message?: string;
  };
}

interface GmailAuthorization {
  authorizationUrl: string;
}

export class APIError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code: string,
  ) {
    super(message);
    this.name = "APIError";
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    ...init,
    credentials: "include",
    headers: {
      Accept: "application/json",
      ...init?.headers,
    },
  });

  if (!response.ok) {
    let envelope: APIErrorEnvelope = {};
    try {
      envelope = (await response.json()) as APIErrorEnvelope;
    } catch {
      // The typed fallback below is safer than exposing an upstream response.
    }
    throw new APIError(
      envelope.error?.message ?? "The local service could not complete the request.",
      response.status,
      envelope.error?.code ?? "request_failed",
    );
  }

  if (response.status === 204) {
    return undefined as T;
  }
  return (await response.json()) as T;
}

let pendingLocalSession: Promise<{ status: string }> | undefined;

export function createLocalSession(): Promise<{ status: string }> {
  if (pendingLocalSession) return pendingLocalSession;

  const attempt = request<{ status: string }>("/api/v1/session", { method: "POST" });
  pendingLocalSession = attempt;
  void attempt.then(
    () => {
      if (pendingLocalSession === attempt) pendingLocalSession = undefined;
    },
    () => {
      if (pendingLocalSession === attempt) pendingLocalSession = undefined;
    },
  );
  return attempt;
}

export function gmailConnectionStatus(): Promise<GmailConnectionStatus> {
  return request("/api/v1/auth/gmail/status");
}

export function beginGmailAuthorization(): Promise<GmailAuthorization> {
  return request("/api/v1/auth/gmail/start", { method: "POST" });
}

let pendingGmailSync: Promise<GmailSyncStatus> | undefined;
const gmailSyncListeners = new Set<(status: GmailSyncStatus) => void>();

export function synchronizeGmailMailbox(
  onProgress?: (status: GmailSyncStatus) => void,
): Promise<GmailSyncStatus> {
  if (onProgress) gmailSyncListeners.add(onProgress);

  if (!pendingGmailSync) {
    const run = async () => {
      let status: GmailSyncStatus;
      do {
        status = await request<GmailSyncStatus>("/api/v1/gmail/sync", { method: "POST" });
        gmailSyncListeners.forEach((listener) => listener(status));
      } while (status.hasMore);
      return status;
    };
    const attempt = run();
    pendingGmailSync = attempt;
    void attempt.then(
      () => {
        if (pendingGmailSync === attempt) pendingGmailSync = undefined;
      },
      () => {
        if (pendingGmailSync === attempt) pendingGmailSync = undefined;
      },
    );
  }

  const sync = pendingGmailSync;
  if (!onProgress) return sync;
  return sync.finally(() => gmailSyncListeners.delete(onProgress));
}

export function loadWorkspaceConversations(
  view: string,
): Promise<WorkspaceConversationPage> {
  const query = new URLSearchParams({ view });
  return request(`/api/v1/conversations?${query.toString()}`);
}

export function loadWorkspaceConversation(id: string): Promise<WorkspaceConversation> {
  return request(`/api/v1/conversations/${encodeURIComponent(id)}`);
}

export function ollamaStatus(): Promise<OllamaStatus> {
  return request("/api/v1/ollama/status");
}
