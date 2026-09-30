import { fireEvent, render, screen } from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";

describe("App", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("loads Gmail inbox messages into the active workspace", async () => {
    let sessionRequests = 0;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input);
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
      if (path === "/api/v1/gmail/messages?limit=5") {
        return jsonResponse({
          messages: [
            {
              id: "message-1",
              threadId: "thread-1",
              subject: "Vendor renewal",
              from: "Morgan Lee <morgan@example.com>",
              to: "person@example.com",
              date: "Tue, 29 Sep 2026 10:00:00 -0400",
              snippet: "Legal approved the revised terms.",
              unread: true,
              labelIds: ["INBOX", "UNREAD"],
              internalAt: "1790683200000",
            },
            {
              id: "message-ad",
              threadId: "thread-ad",
              subject: "Weekly deals",
              from: "Store <offers@example.com>",
              to: "person@example.com",
              date: "Tue, 29 Sep 2026 09:00:00 -0400",
              snippet: "Save twenty percent this week.",
              unread: true,
              labelIds: ["INBOX", "CATEGORY_PROMOTIONS"],
              internalAt: "1790679600000",
            },
          ],
          nextPageToken: "next-page-token",
          resultSize: 3,
        });
      }
      if (path === "/api/v1/gmail/threads/thread-1/triage") {
        return jsonResponse({
          conversation: conversation("thread-1", "message-1", "Please approve the revised terms by Friday."),
          triage: {
            visibility: "active",
            category: "action_required",
            needsAction: true,
            urgent: false,
            confidence: 0.95,
            reasonCodes: ["direct_request"],
            sourceMessageIds: ["message-1"],
            aiStatus: "applied",
            model: "llama3.2:3b",
          },
        });
      }
      if (path === "/api/v1/gmail/threads/thread-ad/triage") {
        return jsonResponse({
          conversation: conversation("thread-ad", "message-ad", "Save twenty percent this week."),
          triage: {
            visibility: "all",
            category: "promotion",
            needsAction: false,
            urgent: false,
            confidence: 0.99,
            reasonCodes: ["promotion", "no_user_action"],
            sourceMessageIds: ["message-ad"],
            aiStatus: "applied",
            model: "llama3.2:3b",
          },
        });
      }
      if (path === "/api/v1/gmail/threads/thread-1") {
        return jsonResponse({
          id: "thread-1",
          historyId: "history-1",
          messages: [
            {
              id: "message-1",
              threadId: "thread-1",
              subject: "Vendor renewal",
              from: "Morgan Lee <morgan@example.com>",
              to: "person@example.com",
              date: "Tue, 29 Sep 2026 10:00:00 -0400",
              internalAt: "1790683200000",
              labelIds: ["INBOX", "UNREAD"],
              body: "Please approve the revised terms by Friday.",
              bodySource: "plain",
              bodyTruncated: false,
              suspiciousContent: false,
            },
          ],
          truncated: false,
        });
      }
      if (path === "/api/v1/gmail/messages?limit=5&pageToken=next-page-token") {
        return jsonResponse({
          messages: [
            {
              id: "message-2",
              threadId: "thread-2",
              subject: "Planning update",
              from: "Alex Kim <alex@example.com>",
              to: "person@example.com",
              date: "Tue, 29 Sep 2026 11:00:00 -0400",
              snippet: "The revised schedule is ready.",
              unread: false,
              labelIds: ["INBOX"],
              internalAt: "1790686800000",
            },
          ],
          resultSize: 2,
        });
      }
      if (path === "/api/v1/gmail/threads/thread-2/triage") {
        return jsonResponse({
          conversation: conversation("thread-2", "message-2", "The revised schedule is ready for review."),
          triage: {
            visibility: "active",
            category: "important_update",
            needsAction: false,
            urgent: false,
            confidence: 0.91,
            reasonCodes: ["important_update"],
            sourceMessageIds: ["message-2"],
            aiStatus: "applied",
            model: "llama3.2:3b",
          },
        });
      }
      if (path === "/api/v1/gmail/threads/thread-2") {
        return jsonResponse({
          id: "thread-2",
          historyId: "history-2",
          messages: [
            {
              id: "message-2",
              threadId: "thread-2",
              subject: "Planning update",
              from: "Alex Kim <alex@example.com>",
              to: "person@example.com",
              date: "Tue, 29 Sep 2026 11:00:00 -0400",
              internalAt: "1790686800000",
              labelIds: ["INBOX"],
              body: "The revised schedule is ready for review.",
              bodySource: "plain",
              bodyTruncated: false,
              suspiciousContent: false,
            },
          ],
          truncated: false,
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
    expect(screen.queryByText("Weekly deals")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /All threads/ }));
    expect(await screen.findByText("Weekly deals")).toBeInTheDocument();
    expect(screen.getByText("Low priority")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Active/ }));

    fireEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(await screen.findByRole("heading", { name: "Planning update" })).toBeInTheDocument();
    expect(screen.getByText("Page 2")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Previous" }));
    expect(await screen.findByRole("heading", { name: "Vendor renewal" })).toBeInTheDocument();
    expect(screen.getByText("Page 1")).toBeInTheDocument();
    expect(sessionRequests).toBe(1);
  });
});

function jsonResponse(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function conversation(threadId: string, messageId: string, body: string) {
  return {
    id: threadId,
    historyId: `history-${threadId}`,
    messages: [
      {
        id: messageId,
        threadId,
        subject: "Test conversation",
        from: "Sender <sender@example.com>",
        to: "person@example.com",
        date: "Tue, 29 Sep 2026 10:00:00 -0400",
        internalAt: "1790683200000",
        labelIds: ["INBOX"],
        body,
        bodySource: "plain",
        bodyTruncated: false,
        suspiciousContent: false,
      },
    ],
    truncated: false,
  };
}
