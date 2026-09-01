import React from "react";
import ReactDOM from "react-dom/client";
import { createBrowserRouter, RouterProvider, Navigate } from "react-router-dom";
import "./index.css";
import { App } from "./App";
import { AuthGate } from "./AuthGate";
import { LibraryPage } from "./pages/Library";
import { BooksPage } from "./pages/Books";
import { BookDetailPage } from "./pages/BookDetail";
import { OrganizePage } from "./pages/Organize";
import { SettingsPage } from "./pages/Settings";
import { ActivityPage } from "./pages/Activity";

const router = createBrowserRouter([
  {
    path: "/",
    element: <App />,
    children: [
      { index: true, element: <Navigate to="/library" replace /> },
      { path: "library", element: <LibraryPage /> },
      { path: "books", element: <BooksPage /> },
      { path: "books/:id", element: <BookDetailPage /> },
      { path: "organize", element: <OrganizePage /> },
      { path: "activity", element: <ActivityPage /> },
      { path: "settings", element: <SettingsPage /> },
    ],
  },
]);

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <AuthGate>
      <RouterProvider router={router} />
    </AuthGate>
  </React.StrictMode>,
);
