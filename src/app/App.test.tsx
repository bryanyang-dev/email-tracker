import { fireEvent, render, screen, within } from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";

describe("App", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("scrolls database-wide conversation views with global counts", async () => {
    let sessionRequests = 0;
    let syncRequests = 0;
    const requestedPaths: string[] = [];
    let finishSync: (() => void) | undefined;
    const syncCanFinish = new Promise<void>((resolve) => {
      finishSync = resolve;
    });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input);
      requestedPaths.push(path);
      if (path === "/api/v1/session") {
        sessionRequests += 1;
        return jsonResponse({ status: "ready" }, 201);
      }
      if (path === "/api/v1/auth/gmail/status") {
        return jsonResponse({ configured: true, connected: true, emailAddress: "person@example.com" });
      }
      if (path === "/api/v1/ollama/status") {
        return jsonResponse({
          available: true,
          models: [{ name: "llama3.2:3b", parameterSize: "3.2B", size: 2019393189 }],
          triageModel: "llama3.2:3b",
        });
      }
      if (path === "/api/v1/conversations?view=active") {
        return jsonResponse({
          conversations: [
            conversationSummary({
              id: "conversation-1",
              title: "Vendor renewal",
              state: "active",
              category: "action_required",
              reasonCodes: ["direct_request"],
              preview: "Legal approved the revised terms.",
            }),
            conversationSummary({
              id: "conversation-3",
              title: "Planning update",
              state: "active",
              category: "important_update",
              reasonCodes: ["important_update"],
              preview: "The revised schedule is ready.",
              needsAttention: false,
            }),
          ],
          counts: conversationCounts(),
        });
      }
	  if (path === "/api/v1/conversations?view=all") {
		return jsonResponse({
		  conversations: [
			conversationSummary({
			  id: "conversation-1", title: "Vendor renewal", state: "active",
			  category: "action_required", reasonCodes: ["direct_request"],
			  preview: "Legal approved the revised terms.",
			}),
			conversationSummary({
			  id: "conversation-2", title: "Contract question", state: "suggested",
			  category: "important_update", reasonCodes: ["important_update"],
			  preview: "A contract detail may need review.", needsAttention: false,
			}),
			conversationSummary({
			  id: "conversation-3", title: "Planning update", state: "active",
			  category: "important_update", reasonCodes: ["important_update"],
			  preview: "The revised schedule is ready.", needsAttention: false,
			}),
		  ],
		  counts: conversationCounts(),
		});
	  }
      if (path === "/api/v1/conversations/conversation-1") {
        return jsonResponse(conversationDetail(
          conversationSummary({
            id: "conversation-1",
            title: "Vendor renewal",
            state: "active",
            category: "action_required",
            reasonCodes: ["direct_request"],
            preview: "Legal approved the revised terms.",
          }),
          "Please approve the revised terms by Friday.",
        ));
      }
      if (path === "/api/v1/conversations/conversation-2") {
        return jsonResponse(conversationDetail(
          conversationSummary({
            id: "conversation-2",
            title: "Contract question",
            state: "suggested",
            category: "important_update",
            reasonCodes: ["important_update"],
            preview: "A contract detail may need review.",
          }),
          "Could you review this contract detail?",
        ));
      }
      if (path === "/api/v1/conversations/conversation-3") {
        return jsonResponse(conversationDetail(
          conversationSummary({
            id: "conversation-3",
            title: "Planning update",
            state: "active",
            category: "important_update",
            reasonCodes: ["important_update"],
            preview: "The revised schedule is ready.",
          }),
          "The revised schedule is ready for review.",
        ));
      }
      if (path === "/api/v1/gmail/sync") {
        syncRequests += 1;
        if (syncRequests === 1) {
          return jsonResponse({
            phase: "discovering",
            complete: false,
            messagesCached: 20,
            hasMore: true,
            processed: 20,
            onboardingProcessed: 20,
            estimatedTotal: 20,
            pendingConversations: 1,
            conversationsProcessed: 0,
            conversationsCreated: 0,
          });
        }
        await syncCanFinish;
        return jsonResponse({
          phase: "complete",
          complete: true,
          messagesCached: 40,
          hasMore: false,
          processed: 0,
          onboardingProcessed: 40,
          estimatedTotal: 40,
          pendingConversations: 0,
          conversationsProcessed: 1,
          conversationsCreated: 1,
        });
      }
      return jsonResponse({}, 500);
    }));

    render(
      <StrictMode>
        <App />
      </StrictMode>,
    );

    expect(screen.getByRole("heading", { name: "Active" })).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "Vendor renewal" })).toBeInTheDocument();
    expect(screen.getByText("Connected as person@example.com")).toBeInTheDocument();
    expect(screen.getByText("Local AI ready")).toBeInTheDocument();
    expect(screen.getByText("llama3.2:3b")).toBeInTheDocument();
    expect(await screen.findByText("Please approve the revised terms by Friday.")).toBeInTheDocument();
    expect(screen.queryByText("Contract question")).not.toBeInTheDocument();
		const workspaceNavigation = within(screen.getByLabelText("Workspace navigation"));
		expect(workspaceNavigation.getByRole("button", { name: /Active 2/ })).toBeInTheDocument();
		expect(workspaceNavigation.getByRole("button", { name: /Needs attention 1/ })).toBeInTheDocument();
		expect(workspaceNavigation.getByRole("button", { name: /Suggested 1/ })).toBeInTheDocument();
		expect(workspaceNavigation.getByRole("button", { name: /All threads 3/ })).toBeInTheDocument();
    expect(await screen.findByRole("progressbar", { name: "Mailbox sync progress" })).not.toHaveAttribute("aria-valuenow");
    expect(screen.getByText("Indexed 20 messages; discovering more…")).toBeInTheDocument();
    finishSync?.();
    expect(await screen.findByText("Local mailbox index ready")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /All threads/ }));
    expect(await screen.findByText("Contract question")).toBeInTheDocument();
	expect(screen.getByText("Planning update")).toBeInTheDocument();
	expect(screen.queryByRole("button", { name: "Next" })).not.toBeInTheDocument();
    expect(sessionRequests).toBe(1);
    expect(requestedPaths.some((path) => path.startsWith("/api/v1/gmail/messages"))).toBe(false);
    expect(requestedPaths.some((path) => path.startsWith("/api/v1/gmail/threads"))).toBe(false);
  });

  it("resumes mailbox indexing after a rate-limit pause", async () => {
    let syncRequests = 0;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input);
      if (path === "/api/v1/session") {
        return jsonResponse({ status: "ready" }, 201);
      }
      if (path === "/api/v1/auth/gmail/status") {
        return jsonResponse({ configured: true, connected: true, emailAddress: "person@example.com" });
      }
      if (path === "/api/v1/ollama/status") {
        return jsonResponse({ available: false, models: [] });
      }
      if (path === "/api/v1/conversations?view=active") {
        return jsonResponse({ conversations: [], counts: emptyConversationCounts() });
      }
      if (path === "/api/v1/gmail/sync") {
        syncRequests += 1;
        if (syncRequests === 1) {
          return jsonResponse({
            error: {
              code: "gmail_rate_limited",
              message: "Gmail temporarily rate-limited indexing. Wait a moment, then resume.",
            },
          }, 429);
        }
        return jsonResponse({
          phase: "complete",
          complete: true,
          messagesCached: 20,
          hasMore: false,
          processed: 0,
          onboardingProcessed: 20,
          estimatedTotal: 20,
          pendingConversations: 0,
          conversationsProcessed: 0,
          conversationsCreated: 0,
        });
      }
      return jsonResponse({}, 500);
    }));

    render(
      <StrictMode>
        <App />
      </StrictMode>,
    );

    expect(await screen.findByText("Gmail temporarily rate-limited indexing. Wait a moment, then resume.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Resume indexing" }));
    expect(await screen.findByText("Local mailbox index ready")).toBeInTheDocument();
    expect(syncRequests).toBe(2);
  });
});

function jsonResponse(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function conversationSummary(overrides: {
  id: string;
  title: string;
  state: "active" | "suggested";
  category: string;
  reasonCodes: string[];
  preview: string;
	needsAttention?: boolean;
}) {
  return {
    ...overrides,
    importance: overrides.state === "active" ? "important" : "possibly_important",
    importanceScore: overrides.state === "active" ? 0.95 : 0.7,
    updatedAt: "2026-09-29T14:00:00Z",
    messageCount: 1,
    unread: true,
	needsAttention: overrides.needsAttention ?? true,
    participants: ["Morgan Lee <morgan@example.com>"],
    latestUpdate: overrides.preview,
    aiStatus: "applied",
  };
}

function conversationCounts() {
	return { active: 2, attention: 1, suggested: 1, snoozed: 0, resolved: 0, all: 3 };
}

function emptyConversationCounts() {
	return { active: 0, attention: 0, suggested: 0, snoozed: 0, resolved: 0, all: 0 };
}

function conversationDetail(summary: ReturnType<typeof conversationSummary>, body: string) {
  return {
    ...summary,
    messages: [
      {
        id: `message-${summary.id}`,
        threadId: `thread-${summary.id}`,
        subject: summary.title,
        from: summary.participants[0],
        to: "person@example.com",
        date: "Tue, 29 Sep 2026 10:00:00 -0400",
        internalAt: "1790683200000",
        labelIds: ["INBOX", "UNREAD"],
        body,
        bodySource: "plain",
        bodyTruncated: false,
        suspiciousContent: false,
      },
    ],
  };
}
