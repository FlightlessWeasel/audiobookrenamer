import { useCallback, useState } from "react";
import { GROUP_OPTIONS, type GroupBy } from "./groupBooks";

// One shared "Group by" preference, persisted in localStorage so it survives a
// tab switch (each page unmounts on navigation) and a reload. Books and
// Organize both read it through this hook, so a choice made on one page is
// already applied on the other.
const STORAGE_KEY = "abr:books-group-by";
const VALID = new Set<string>(GROUP_OPTIONS.map((o) => o.value));

function readStored(): GroupBy {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw !== null && VALID.has(raw)) return raw as GroupBy;
  } catch {
    // localStorage can be unavailable (private mode, storage disabled); fall
    // back to the unset default rather than failing to render.
  }
  return "";
}

export function useGroupBy(): [GroupBy, (value: GroupBy) => void] {
  const [groupBy, setLocal] = useState<GroupBy>(readStored);

  const setGroupBy = useCallback((value: GroupBy) => {
    setLocal(value);
    try {
      localStorage.setItem(STORAGE_KEY, value);
    } catch {
      // Best-effort persistence; the in-memory value still updates.
    }
  }, []);

  return [groupBy, setGroupBy];
}
