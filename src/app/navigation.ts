import type { ThreadView } from "./types";

export interface NavigationItem {
  id: ThreadView | "search" | "settings";
  label: string;
}

export const navigationItems: NavigationItem[] = [
  { id: "active", label: "Active" },
  { id: "attention", label: "Needs attention" },
  { id: "suggested", label: "Suggested" },
  { id: "snoozed", label: "Snoozed" },
  { id: "resolved", label: "Resolved" },
  { id: "all", label: "All threads" },
  { id: "search", label: "Search" },
  { id: "settings", label: "Settings" },
];
