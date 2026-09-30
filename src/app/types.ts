export type ThreadView =
  | "active"
  | "attention"
  | "suggested"
  | "snoozed"
  | "resolved"
  | "all";

export interface ActionItem {
  id: string;
  description: string;
  owner: string;
  due: string | null;
  completed: boolean;
}

export interface EmailThread {
  id: string;
  title: string;
  participants: string[];
  updatedAt: string;
  preview: string;
  latestUpdate: string;
  summary: string;
  state: "active" | "suggested" | "snoozed" | "resolved";
  needsAttention: boolean;
  unread: boolean;
  actionItems: ActionItem[];
  attachmentCount: number;
  messageCount: number;
  triageCategory: string;
  triageReasons: string[];
  triageStatus: "pending" | "applied" | "rules" | "unavailable" | "failed";
}

export type ConnectionState =
  | { status: "loading" }
  | { status: "offline"; message: string }
  | { status: "not-configured" }
  | { status: "disconnected" }
  | { status: "connected"; emailAddress: string };

export type LocalAIState =
  | { status: "checking" }
  | { status: "unavailable"; message: string }
  | { status: "available"; models: string[]; triageModel?: string; message?: string };
