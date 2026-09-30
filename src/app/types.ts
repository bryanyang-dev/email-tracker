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
  state: Exclude<ThreadView, "attention" | "all">;
  needsAttention: boolean;
  unread: boolean;
  actionItems: ActionItem[];
  attachmentCount: number;
  messageCount: number;
}

export type ConnectionState =
  | { status: "loading" }
  | { status: "offline"; message: string }
  | { status: "not-configured" }
  | { status: "disconnected" }
  | { status: "connected"; emailAddress: string };
