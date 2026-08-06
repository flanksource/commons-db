import { useQuery } from "@tanstack/react-query";
import type { ProfileDocument } from "./reconcileModel";

/**
 * The one react-query key for `GET /api/v1/profiles`.
 *
 * Three surfaces used to fetch the same endpoint under three different keys, so
 * a rename invalidated some of them and left the others stale. Anything that
 * writes a profile must invalidate this key — see editProfileAction.
 */
export const PROFILES_QUERY_KEY = ["profiles"] as const;

// asProfiles pulls the document array out of whatever envelope the endpoint
// returns: a bare array, or a { rows } / { data } / { items } wrapper.
export function asProfiles(payload: unknown): ProfileDocument[] {
  if (Array.isArray(payload)) return payload as ProfileDocument[];
  if (payload && typeof payload === "object") {
    for (const key of ["rows", "data", "items"]) {
      const rows = (payload as Record<string, unknown>)[key];
      if (Array.isArray(rows)) return rows as ProfileDocument[];
    }
  }
  return [];
}

export async function fetchProfiles(): Promise<ProfileDocument[]> {
  const response = await fetch("/api/v1/profiles", {
    headers: { Accept: "application/json" },
  });
  if (!response.ok) throw new Error(`Unable to list profiles: ${response.status}`);
  return asProfiles(await response.json());
}

/** Every stored profile document, shared by the destination picker and the logs renderer. */
export function useProfiles() {
  return useQuery({ queryKey: PROFILES_QUERY_KEY, queryFn: fetchProfiles });
}
