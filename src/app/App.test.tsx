import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";

describe("App", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("loads Gmail inbox messages into the active workspace", async () => {
    const responses = [
      jsonResponse({ status: "ready" }, 201),
      jsonResponse({ configured: true, connected: true, emailAddress: "person@example.com" }),
      jsonResponse({
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
      }),
    ];
    vi.stubGlobal("fetch", vi.fn(async () => responses.shift() ?? jsonResponse({}, 500)));

    render(<App />);

    expect(screen.getByRole("heading", { name: "Active" })).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "Vendor renewal" })).toBeInTheDocument();
    expect(screen.getByText("Connected as person@example.com")).toBeInTheDocument();
  });
});

function jsonResponse(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
