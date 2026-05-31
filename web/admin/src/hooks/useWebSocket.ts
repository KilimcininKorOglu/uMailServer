import { useEffect, useRef, useState, useCallback } from "react";
import type { RealtimeMetrics, Activity } from "@/types";

interface UseWebSocketOptions {
  onMetrics?: (metrics: RealtimeMetrics) => void;
  onActivity?: (activity: Activity) => void;
  onStatus?: (status: unknown) => void;
  onHealth?: (health: unknown) => void;
  onError?: (error: Error) => void;
}

// Human-readable labels for the mailbox events the SSE server emits.
const MAIL_EVENT_LABELS: Record<string, string> = {
  new_mail: "New message delivered",
  expunge: "Message expunged",
  flags_changed: "Message flags changed",
  folder_update: "Folder updated",
};

function activityId(prefix: string): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `${prefix}-${performance.now()}`;
}

export function useWebSocket(options: UseWebSocketOptions = {}) {
  const [isConnected, setIsConnected] = useState(false);
  const esRef = useRef<EventSource | null>(null);

  const { onMetrics, onActivity, onError } = options;

  const connect = useCallback(() => {
    // The server exposes a Server-Sent Events stream at /api/v1/events and
    // authenticates via the jwt HttpOnly cookie. EventSource sends that cookie
    // on same-origin requests, so it is the correct client (a raw WebSocket
    // could neither complete the SSE handshake nor send the auth header).
    const es = new EventSource("/api/v1/events", { withCredentials: true });
    esRef.current = es;

    es.onopen = () => setIsConnected(true);
    es.addEventListener("connected", () => setIsConnected(true));
    es.onerror = () => {
      setIsConnected(false);
      onError?.(new Error("EventSource error"));
      // EventSource reconnects automatically; no manual retry loop needed.
    };

    const emitMailActivity = (
      type: Activity["type"],
      event: MessageEvent,
      label: string,
    ) => {
      let details: string | undefined;
      try {
        const data = JSON.parse(event.data) as { mailbox?: string; user?: string };
        details = [data.user, data.mailbox].filter(Boolean).join(" · ") || undefined;
      } catch {
        // Ignore malformed payloads; still surface the activity.
      }
      onActivity?.({
        id: activityId(label),
        type,
        message: label,
        details,
        timestamp: new Date().toISOString(),
        severity: "info",
      });
    };

    es.addEventListener("new_mail", (e) =>
      emitMailActivity("message", e as MessageEvent, MAIL_EVENT_LABELS.new_mail),
    );
    es.addEventListener("expunge", (e) =>
      emitMailActivity("message", e as MessageEvent, MAIL_EVENT_LABELS.expunge),
    );
    es.addEventListener("flags_changed", (e) =>
      emitMailActivity("message", e as MessageEvent, MAIL_EVENT_LABELS.flags_changed),
    );
    es.addEventListener("folder_update", (e) =>
      emitMailActivity("system", e as MessageEvent, MAIL_EVENT_LABELS.folder_update),
    );

    // If the server ever emits realtime metrics over the same channel, surface
    // them; today metrics are fetched over REST instead.
    es.addEventListener("metrics", (e) => {
      try {
        onMetrics?.(JSON.parse((e as MessageEvent).data) as RealtimeMetrics);
      } catch {
        // Ignore malformed payloads.
      }
    });
  }, [onActivity, onMetrics, onError]);

  const disconnect = useCallback(() => {
    esRef.current?.close();
    esRef.current = null;
    setIsConnected(false);
  }, []);

  // SSE is a one-way channel; kept as a no-op for API compatibility.
  const sendMessage = useCallback(() => {}, []);

  useEffect(() => {
    connect();
    return () => disconnect();
  }, [connect, disconnect]);

  return {
    isConnected,
    sendMessage,
    connect,
    disconnect,
  };
}
