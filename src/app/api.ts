export interface GmailConnectionStatus {
  configured: boolean;
  connected: boolean;
  emailAddress?: string;
}

export interface GmailInboxMessage {
  id: string;
  threadId: string;
  subject: string;
  from: string;
  to: string;
  date: string;
  snippet: string;
  unread: boolean;
  labelIds: string[];
  internalAt: string;
}

export interface GmailInboxPage {
  messages: GmailInboxMessage[];
  nextPageToken?: string;
  resultSize: number;
}

export interface GmailConversationMessage {
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
}

export interface GmailConversation {
  id: string;
  historyId: string;
  messages: GmailConversationMessage[];
  truncated: boolean;
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

export function createLocalSession(): Promise<{ status: string }> {
  return request("/api/v1/session", { method: "POST" });
}

export function gmailConnectionStatus(): Promise<GmailConnectionStatus> {
  return request("/api/v1/auth/gmail/status");
}

export function beginGmailAuthorization(): Promise<GmailAuthorization> {
  return request("/api/v1/auth/gmail/start", { method: "POST" });
}

export function loadGmailInbox(pageToken = ""): Promise<GmailInboxPage> {
  const query = new URLSearchParams({ limit: "5" });
  if (pageToken) query.set("pageToken", pageToken);
  return request(`/api/v1/gmail/messages?${query.toString()}`);
}

export function loadGmailConversation(threadId: string): Promise<GmailConversation> {
  return request(`/api/v1/gmail/threads/${encodeURIComponent(threadId)}`);
}

export function ollamaStatus(): Promise<OllamaStatus> {
  return request("/api/v1/ollama/status");
}
