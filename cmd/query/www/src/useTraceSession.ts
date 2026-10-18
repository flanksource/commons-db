import { useQuery } from "@tanstack/react-query";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  createTraceSession,
  errorMessage,
  fetchTraceSnapshot,
  isTerminalSession,
  stopSession,
  type TraceFilters,
  type TraceRow,
  type TraceSnapshot,
  type TraceState,
} from "./connectionTraceApi";

const POLL_INTERVAL_MS = 1_000;
const NO_ROWS: TraceRow[] = [];

type View = { state: TraceState; error: string };

/** Folds the latest poll outcome over the state the user's actions put the trace in. */
function traceView(
  phase: TraceState,
  actionError: string,
  poll: { data?: TraceSnapshot; error: unknown; isError: boolean },
): View {
  const session = poll.data?.session;
  if (session && isTerminalSession(session)) {
    const failed =
      session.error || ["failed", "error"].includes(session.state.toLowerCase());
    return failed
      ? { state: "error", error: session.error || `Trace ${session.state}` }
      : { state: "stopped", error: actionError };
  }
  if (poll.isError) {
    return {
      state: "error",
      error: errorMessage(poll.error, "Failed to read trace session"),
    };
  }
  return { state: phase, error: actionError };
}

/** Owns one SQL Server trace session and tears it down with its connection view. */
export function useTraceSession(connectionID: string) {
  const [phase, setPhase] = useState<TraceState>("idle");
  const [sessionID, setSessionID] = useState<string>();
  const [actionError, setActionError] = useState("");
  const sessionRef = useRef<string>();
  const startPending = useRef(false);
  const stopRequested = useRef(false);
  const mounted = useRef(true);

  const poll = useQuery({
    queryKey: ["trace-session", sessionID],
    enabled: Boolean(sessionID) && (phase === "running" || phase === "stopping"),
    queryFn: async () => {
      const snapshot = await fetchTraceSnapshot(sessionID!);
      if (isTerminalSession(snapshot.session)) sessionRef.current = undefined;
      return snapshot;
    },
    refetchInterval: (query) =>
      query.state.status === "error" ||
      (query.state.data && isTerminalSession(query.state.data.session))
        ? false
        : POLL_INTERVAL_MS,
    refetchIntervalInBackground: true,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
    retry: false,
    gcTime: 0,
  });

  const stop = useCallback(async () => {
    stopRequested.current = true;
    const active = sessionRef.current;
    if (!active) {
      if (startPending.current) setPhase("stopping");
      return;
    }
    setPhase("stopping");
    try {
      await stopSession(active);
      if (mounted.current) setActionError("");
    } catch (reason) {
      if (mounted.current) {
        setActionError(errorMessage(reason, "Failed to stop trace"));
        setPhase("error");
      }
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      stopRequested.current = true;
      const active = sessionRef.current;
      if (active) {
        void stopSession(active).catch(() => undefined);
      }
    };
  }, []);

  const start = async (filters: TraceFilters) => {
    if (startPending.current || sessionRef.current) return;
    startPending.current = true;
    stopRequested.current = false;
    setPhase("starting");
    setActionError("");
    setSessionID(undefined);
    try {
      const created = await createTraceSession(connectionID, filters);
      sessionRef.current = created.id;
      if (!mounted.current) {
        await stopSession(created.id).catch(() => undefined);
        return;
      }
      setSessionID(created.id);
      if (stopRequested.current) {
        setPhase("stopping");
        await stopSession(created.id);
      } else {
        setPhase("running");
      }
    } catch (reason) {
      if (mounted.current) {
        setActionError(errorMessage(reason, "Failed to start trace"));
        setPhase("error");
      }
    } finally {
      startPending.current = false;
    }
  };

  const retryPolling = () => {
    setActionError("");
    setPhase(stopRequested.current ? "stopping" : "running");
    if (poll.isError) void poll.refetch();
  };

  const view = traceView(phase, actionError, poll);
  return {
    state: view.state,
    error: view.error,
    sessionID,
    rows: poll.data?.rows ?? NO_ROWS,
    hasActiveSession: Boolean(sessionRef.current),
    start,
    stop,
    retryPolling,
  };
}
