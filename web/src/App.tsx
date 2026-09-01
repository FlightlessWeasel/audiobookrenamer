import { NavLink, Outlet } from "react-router-dom";
import { client } from "./api/client";
import { useAuth } from "./AuthGate";
import { useAction } from "./lib/useAction";

const tabs = [
  { to: "/library", label: "Library" },
  { to: "/books", label: "Books" },
  { to: "/organize", label: "Organize" },
  { to: "/activity", label: "Activity" },
  { to: "/settings", label: "Settings" },
];

export function App() {
  const { status, refresh } = useAuth();
  // A successful logout makes `refresh()` swap the tree back to <Login>, which
  // unmounts <App> before the action settles — useAction drops the trailing
  // state update when that happens.
  const { run, busy: signingOut, error: signOutErr } = useAction();

  function signOut() {
    run(async () => {
      await client.logout();
      refresh();
    });
  }

  return (
    <div className="min-h-screen">
      <header className="border-b border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
        <div className="mx-auto flex max-w-7xl items-center gap-6 px-4 py-3">
          <span className="text-lg font-semibold">Audiobook Library Manager</span>
          <nav className="flex gap-1">
            {tabs.map((t) => (
              <NavLink
                key={t.to}
                to={t.to}
                className={({ isActive }) =>
                  `rounded px-3 py-1.5 text-sm font-medium transition ${
                    isActive
                      ? "bg-slate-900 text-white dark:bg-slate-100 dark:text-slate-900"
                      : "text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
                  }`
                }
              >
                {t.label}
              </NavLink>
            ))}
          </nav>
          {status.enabled && (
            <div className="ml-auto flex items-center gap-2">
              {signOutErr && (
                <span role="alert" className="text-xs text-red-600">
                  {signOutErr}
                </span>
              )}
              <button
                onClick={signOut}
                disabled={signingOut}
                className="rounded border border-slate-300 px-3 py-1.5 text-sm disabled:opacity-50 dark:border-slate-700"
              >
                {status.username ? `Sign out (${status.username})` : "Sign out"}
              </button>
            </div>
          )}
        </div>
      </header>
      <main className="mx-auto max-w-7xl px-4 py-6">
        <Outlet />
      </main>
    </div>
  );
}
