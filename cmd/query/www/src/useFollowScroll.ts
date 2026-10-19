import { isPinnedToBottom } from "@flanksource/clicky-ui";
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { BOTTOM_THRESHOLD_PX } from "./logsFollowModel";

/** Tracks whether the reader is at the bottom of the log and keeps a followed tail pinned there. */
export function useFollowScroll({
  following,
  rows,
}: {
  following: boolean;
  rows: readonly unknown[];
}) {
  const [pinned, setPinned] = useState(true);
  const scrollRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    const element = scrollRef.current;
    if (!element) return;
    const onScroll = () => setPinned(isPinnedToBottom(element, BOTTOM_THRESHOLD_PX));
    onScroll();
    element.addEventListener("scroll", onScroll, { passive: true });
    return () => element.removeEventListener("scroll", onScroll);
  }, []);

  // Reading wins over chasing: rows keep arriving while the reader is scrolled
  // up, they just stop moving the viewport until the tail is re-pinned.
  useLayoutEffect(() => {
    const element = scrollRef.current;
    if (element && following && pinned) element.scrollTop = element.scrollHeight;
  }, [rows, following, pinned]);

  const scrollToLatest = () => {
    const element = scrollRef.current;
    if (!element) return;
    setPinned(true);
    element.scrollTo({ top: element.scrollHeight, behavior: "smooth" });
  };

  return { scrollRef, pinned, scrollToLatest };
}
