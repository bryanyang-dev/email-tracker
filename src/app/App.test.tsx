import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";

describe("App", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("loads Gmail inbox messages into the active workspace", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input);
      if (path === "/api/v1/session") return jsonResponse({ status: "ready" }, 201);
      if (path === "/api/v1/auth/gmail/status") {
        return jsonResponse({ configured: true, connected: true, emailAddress: "person@example.com" });
      }
      if (path === "/api/v1/ollama/status") {
        return jsonResponse({
          available: true,
          models: [{ name: "llama3.2:3b", parameterSize: "3.2B", size: 2019393189 }],
        });
      }
      if (path === "/api/v1/gmail/messages?limit=25") {
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
          ],
          resultSize: 1,
        });
      }
      return jsonResponse({}, 500);
    }));

    render(<App />);

    expect(screen.getByRole("heading", { name: "Active" })).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "Vendor renewal" })).toBeInTheDocument();
    expect(screen.getByText("Connected as person@example.com")).toBeInTheDocument();
    expect(screen.getByText("Local AI ready")).toBeInTheDocument();
    expect(screen.getByText("llama3.2:3b")).toBeInTheDocument();
  });
});

function jsonResponse(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
